// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package notifywebhook

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/mattermost/mattermost-plugin-boards/server/utils"

	"github.com/mattermost/mattermost/server/public/shared/mlog"
)

const (
	deliveryQueueSize   = 1000
	deliveryWorkerCount = 2
	requestTimeout      = 10 * time.Second
	maxAttempts         = 3
	shutdownTimeout     = 5 * time.Second
	maxResponseDrain    = 64 * 1024
)

// defaultBackoff is the wait schedule between attempts (len == maxAttempts-1).
var defaultBackoff = []time.Duration{1 * time.Second, 4 * time.Second}

// deliveryJob is one signed POST to one endpoint URL.
type deliveryJob struct {
	eventID string
	url     string
	body    []byte
}

// attemptResult classifies a single delivery attempt.
type attemptResult int

const (
	attemptSuccess attemptResult = iota
	attemptRetry
	attemptDrop
)

// delivererParams configures a deliverer. Zero-value fields take the
// production defaults; tests shrink queue/backoff/timeouts.
type delivererParams struct {
	logger    mlog.LoggerIFace
	secret    func() string
	queueSize int
	workers   int
	backoff   []time.Duration
	client    *http.Client
	stopAfter time.Duration
}

// deliverer owns the bounded queue and worker pool that perform signed,
// bounded-retry webhook deliveries. Modeled on the notifysubscriptions
// notifier lifecycle (notifier.go:59-105) and the CallbackQueue drain
// pattern (utils/callbackqueue.go:56-87), but with drop-on-full enqueue.
type deliverer struct {
	logger    mlog.LoggerIFace
	secret    func() string
	queue     chan deliveryJob
	backoff   []time.Duration
	client    *http.Client
	stopAfter time.Duration

	mux     sync.Mutex
	done    chan struct{}
	wg      sync.WaitGroup
	workers int
}

func newDeliverer(params delivererParams) *deliverer {
	if params.queueSize == 0 {
		params.queueSize = deliveryQueueSize
	}
	if params.workers == 0 {
		params.workers = deliveryWorkerCount
	}
	if params.backoff == nil {
		params.backoff = defaultBackoff
	}
	if params.client == nil {
		params.client = &http.Client{
			Timeout: requestTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	if params.stopAfter == 0 {
		params.stopAfter = shutdownTimeout
	}
	return &deliverer{
		logger:    params.logger,
		secret:    params.secret,
		queue:     make(chan deliveryJob, params.queueSize),
		backoff:   params.backoff,
		client:    params.client,
		stopAfter: params.stopAfter,
		workers:   params.workers,
	}
}

// start spawns the worker pool. Safe to call more than once.
func (d *deliverer) start() {
	d.mux.Lock()
	defer d.mux.Unlock()

	if d.done != nil {
		return
	}
	d.done = make(chan struct{})
	done := d.done
	for i := 0; i < d.workers; i++ {
		d.wg.Add(1)
		go d.loop(done)
	}
}

// stop signals the workers, waits for them, then drains any jobs still
// queued with a single attempt each — all within shutdownTimeout. Returns
// false if the deadline expired with work remaining (at-least-once, not
// exactly-once: documented loss case).
func (d *deliverer) stop() bool {
	d.mux.Lock()
	if d.done == nil {
		d.mux.Unlock()
		return true
	}
	close(d.done)
	d.done = nil
	d.mux.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), d.stopAfter)
	defer cancel()

	workersDone := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(workersDone)
	}()

	select {
	case <-workersDone:
	case <-ctx.Done():
		d.logger.Warn("notifyWebhook shutdown timed out waiting for delivery workers")
		return false
	}

	for {
		select {
		case job := <-d.queue:
			d.attempt(ctx, job)
		case <-ctx.Done():
			d.logger.Warn("notifyWebhook shutdown timed out draining delivery queue",
				mlog.Int("undelivered", len(d.queue)),
			)
			return false
		default:
			return true
		}
	}
}

// enqueue adds a job without blocking. Returns false when the queue is
// full; the caller logs and drops.
func (d *deliverer) enqueue(job deliveryJob) bool {
	select {
	case d.queue <- job:
		return true
	default:
		return false
	}
}

func (d *deliverer) loop(done chan struct{}) {
	defer d.wg.Done()
	for {
		select {
		case job := <-d.queue:
			d.deliverSafe(job, done)
		case <-done:
			return
		}
	}
}

// deliverSafe keeps a panicking delivery from killing the worker
// (CallbackQueue precedent, utils/callbackqueue.go:122-133).
func (d *deliverer) deliverSafe(job deliveryJob, done chan struct{}) {
	defer func() {
		if r := recover(); r != nil {
			d.logger.Error("notifyWebhook delivery panic",
				mlog.String("event_id", job.eventID),
				mlog.Any("panic", r),
				mlog.String("stack", string(debug.Stack())),
			)
		}
	}()
	d.deliver(job, done)
}

// deliver runs the bounded-retry loop for one job. Backoff waits abort
// early on shutdown so in-flight retries cannot stall the drain deadline.
func (d *deliverer) deliver(job deliveryJob, done chan struct{}) {
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		switch d.attempt(context.Background(), job) {
		case attemptSuccess:
			return
		case attemptDrop:
			return
		case attemptRetry:
		}
		if attempt == maxAttempts {
			break
		}
		select {
		case <-done:
			d.logger.Warn("notifyWebhook delivery abandoned at shutdown",
				mlog.String("event_id", job.eventID),
				mlog.String("url", job.url),
				mlog.Int("attempts", attempt),
			)
			return
		case <-time.After(d.backoff[attempt-1]):
		}
	}
	d.logger.Error("notifyWebhook delivery failed after retries",
		mlog.String("event_id", job.eventID),
		mlog.String("url", job.url),
		mlog.Int("attempts", maxAttempts),
	)
}

// attempt performs one signed POST and classifies the outcome.
func (d *deliverer) attempt(ctx context.Context, job deliveryJob) attemptResult {
	requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, job.url, bytes.NewReader(job.body))
	if err != nil {
		d.logger.Error("notifyWebhook building request failed",
			mlog.String("event_id", job.eventID),
			mlog.String("url", job.url),
			mlog.Err(err),
		)
		return attemptDrop
	}

	timestamp := utils.GetMillis()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(timestamp, 10))
	if secret := d.secret(); secret != "" {
		req.Header.Set(HeaderSignature, signPayload(secret, timestamp, job.body))
	}

	resp, err := d.client.Do(req)
	if err != nil {
		d.logger.Debug("notifyWebhook attempt failed",
			mlog.String("event_id", job.eventID),
			mlog.String("url", job.url),
			mlog.Err(err),
		)
		return attemptRetry
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseDrain))
		_ = resp.Body.Close()
	}()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		d.logger.Debug("notifyWebhook delivered",
			mlog.String("event_id", job.eventID),
			mlog.String("url", job.url),
			mlog.Int("status", resp.StatusCode),
		)
		return attemptSuccess
	case resp.StatusCode >= 500:
		d.logger.Debug("notifyWebhook attempt got server error",
			mlog.String("event_id", job.eventID),
			mlog.String("url", job.url),
			mlog.Int("status", resp.StatusCode),
		)
		return attemptRetry
	default:
		d.logger.Warn("notifyWebhook delivery rejected, not retrying",
			mlog.String("event_id", job.eventID),
			mlog.String("url", job.url),
			mlog.Int("status", resp.StatusCode),
		)
		return attemptDrop
	}
}
