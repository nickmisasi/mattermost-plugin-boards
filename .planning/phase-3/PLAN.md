# Phase 3 Plan: HMAC Signing + Async Bounded-Retry Delivery

> Fully prescriptive implementation plan for Phase 3 of `.planning/PLAN.md`
> ("Signing + async bounded-retry delivery"). Executable by a coding agent with
> no further design decisions. This phase makes the `notifywebhook` backend
> actually deliver: signed HTTP POSTs from a dedicated worker pool behind a
> bounded queue, so `BlockChanged` never blocks the shared notify
> CallbackQueue workers. The signing tuple frozen here is the second half of
> the cross-repo contract (the envelope was frozen in Phase 1) — the Factory
> ingress implements its verifier from this document.

## Metadata

- **Parent plan:** `.planning/PLAN.md` Phase 3 (tasks 3.1–3.3)
- **Requirements:** `planning/projects/software-factory/ideas/001-framework-vision/gaps-20260718.md`
  G3 (signing: HMAC-SHA256 over raw body, timestamp in signed material to bound
  replay) and G4 (delivery semantics: at-least-once / best-effort, in-memory
  queue, unordered — documented, not "fixed")
- **Branch:** `cursor/m2-webhook-plan-ba42`
- **Depends on:** Phase 1 (implemented, commits `b7cc3dc4` + `ea708f91`).
  **Assumes Phase 2 has landed** — this plan rewires the Phase 2 end-state of
  `webhook_backend.go` (`currentSettings()` + filter gate) and reads
  `cfg.NotifyWebhookSecret` (added by Phase 2 Task 3). If executing out of
  order, land Phase 2 first; the two phases collide only in
  `webhook_backend.go` and `doc.go`.
- **Status:** ready for implementation

## Goal / Success State

After Phase 3:

- Every event that passes the URL and filter gates is marshaled once, then
  enqueued as one delivery job **per configured URL** onto a bounded in-memory
  queue. Enqueue is non-blocking: a full queue drops the job with a Warn.
- A small worker pool (owned by this backend, started in `Start`, drained in
  `ShutDown`) POSTs each job with an explicit-timeout `http.Client`, retries
  bounded times on 5xx/network failure with backoff, drops immediately on
  4xx, and closes/drains every response body (bodyclose + gosec clean).
- Every request carries `X-Boards-Webhook-Timestamp`; when a secret is
  configured it also carries `X-Boards-Webhook-Signature: sha256=<hex>`
  computed over the frozen signed material below. Each retry attempt is
  re-signed with a fresh timestamp.
- `ShutDown` drains queued + in-flight jobs within a deadline that fits the
  server's shutdown budget (`notificationService.Shutdown()` at
  `server/server/server.go:328` runs before the app's own 10s
  CallbackQueue drain, `app/app.go:29` + `app/initialize.go:21-29`).
- `doc.go` carries the final delivery-semantics text (seeds the Phase 4
  upstream PR body).
- Full test suite green under `-race`.

## Frozen contract: the signing tuple

**Signed material (FROZEN, byte-exact):**

```
signedMaterial = ASCII-decimal(timestampMillis) || "." || rawRequestBody
```

— the decimal string form of the timestamp (milliseconds since epoch, no
padding, no sign), one literal period (0x2E), then the exact bytes of the
HTTP request body (the compact `json.Marshal` output of the envelope; the
verifier MUST use the raw received bytes, never a re-serialization).

**Signature:** `HMAC-SHA256(secret, signedMaterial)`, lowercase hex.

**Headers (FROZEN):**

| Header | Value | Presence |
|---|---|---|
| `X-Boards-Webhook-Timestamp` | `<timestampMillis>` (same decimal string used in the signed material) | always |
| `X-Boards-Webhook-Signature` | `sha256=<64 lowercase hex chars>` | only when a secret is configured |
| `Content-Type` | `application/json` | always |

**Semantics for the verifier (Factory ingress):** recompute
`HMAC-SHA256(secret, timestampHeader + "." + rawBody)` and compare
constant-time against the hex after `sha256=`; reject if the timestamp is
outside the replay window (5 minutes recommended — matches the Phase 2
System Console help_text). Timestamps are per-attempt: a retry is a new
timestamp and a new signature, so the replay window does not fight the
retry backoff.

**Verification vectors (computed with OpenSSL, an implementation independent
of the Go code under test; mirror these into the Factory ingress fixtures):**

1. secret `test-secret`, timestamp `1784500000000`, body `{"hello":"world"}`
   → `X-Boards-Webhook-Signature: sha256=45a00bc67318bd98177f9a80b7055c6f46e810f7d6112bd4005089a5552034b9`
2. secret `secret`, timestamp `1700000000000`, body empty
   → `sha256=a7494f803b9c6508c0576777e04b19e49501e8f2ef43081ad6d470867ded87f4`
3. secret `factory-ingress-test-secret`, timestamp `1784500000000`, body =
   the exact 3255 bytes of the committed
   `server/services/notify/notifywebhook/testdata/envelope_golden.json`
   (no trailing newline — the file ends on `}`)
   → `sha256=6baa0630f04ccdbbdfa6d0f230261d4a55ae75649339f3bbfc0a73e050a593af`

(Vector 3 ties both halves of the contract together in one fixture. The
golden file is indented JSON while production bodies are compact — irrelevant
to the verifier, which signs raw received bytes.)

## Frozen operational constants

| Constant | Value | Rationale |
|---|---|---|
| `deliveryQueueSize` | `1000` | jobs (envelope × URL); matches `blockChangeNotifierQueueSize` precedent (`app/app.go:27`) |
| `deliveryWorkerCount` | `2` | >1 so one endpoint in retry-backoff cannot head-of-line-block the other URL; small because delivery is I/O-bound and low-volume |
| `requestTimeout` | `10 * time.Second` | per-attempt `http.Client.Timeout`; bounds worst-case job time to ~35s (3 attempts + backoff) |
| `maxAttempts` | `3` | 1 initial + 2 retries |
| backoff schedule | `1s, 4s` | fixed, no jitter (single-node emitter; simplicity + testability beat thundering-herd protection here) |
| `shutdownTimeout` | `5 * time.Second` | drain deadline; leaves headroom inside the server's shutdown sequence, which still owes the app CallbackQueue its own 10s (`app/app.go:29`) |
| `maxResponseDrain` | `64 * 1024` | max response-body bytes read before close (connection reuse without unbounded reads — gosec-friendly) |

Retry classification (FROZEN): **success** = status 200–299. **Retry** =
transport error (including client timeout) or status ≥ 500. **Drop, no
retry** = every other status (300–499; `http.Client` follows redirects
itself, so a surfaced 3xx is a redirect failure; 429 is deliberately in the
drop bucket for now — flag as a possible follow-up in Phase 4 PR notes, do
not implement Retry-After handling here).

---

## Task 1: `signing.go` — Create

**File:** `server/services/notify/notifywebhook/signing.go` — full file:

```go
// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package notifywebhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

const (
	// HeaderSignature carries "sha256=" + lowercase hex
	// HMAC-SHA256(secret, timestamp + "." + body). Sent only when a
	// signing secret is configured.
	HeaderSignature = "X-Boards-Webhook-Signature"

	// HeaderTimestamp carries the delivery attempt time in milliseconds
	// since epoch, as a decimal string. It is the same string that
	// prefixes the signed material, bounding replay. Always sent.
	HeaderTimestamp = "X-Boards-Webhook-Timestamp"

	signaturePrefix = "sha256="
)

// signPayload computes the signature header value for one delivery
// attempt. The signed material is the decimal timestamp, a literal
// period, and the raw request body bytes — this tuple is a frozen
// cross-repo contract; never change the concatenation.
func signPayload(secret string, timestampMillis int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestampMillis, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}
```

Notes:

- Header-name constants are **exported**: they are contract surface and the
  natural import for any future in-repo consumer test. `signPayload` stays
  unexported — the signature scheme is reachable for external implementers
  through this document and the test vectors, not the Go API.
- `hash.Hash.Write` never returns an error (`hash.Hash` doc guarantee) —
  no error handling; `errcheck` is not enabled in `server/.golangci.yml`,
  and `gosec` does not flag hash writes.
- `godot` (`server/.golangci.yml:50`): comments above end in periods; keep it
  that way.

## Task 2: `delivery.go` — Create

**File:** `server/services/notify/notifywebhook/delivery.go` — full file:

```go
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
}

// deliverer owns the bounded queue and worker pool that perform signed,
// bounded-retry webhook deliveries. Modeled on the notifysubscriptions
// notifier lifecycle (notifier.go:59-105) and the CallbackQueue drain
// pattern (utils/callbackqueue.go:56-87), but with drop-on-full enqueue.
type deliverer struct {
	logger  mlog.LoggerIFace
	secret  func() string
	queue   chan deliveryJob
	backoff []time.Duration
	client  *http.Client

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
		params.client = &http.Client{Timeout: requestTimeout}
	}
	return &deliverer{
		logger:  params.logger,
		secret:  params.secret,
		queue:   make(chan deliveryJob, params.queueSize),
		backoff: params.backoff,
		client:  params.client,
		workers: params.workers,
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

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
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
			d.attempt(job)
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
		switch d.attempt(job) {
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
func (d *deliverer) attempt(job deliveryJob) attemptResult {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, job.url, bytes.NewReader(job.body))
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
```

Prescriptive notes:

- **Why not reuse `utils.CallbackQueue`:** its `Enqueue` blocks when the
  queue is full (`utils/callbackqueue.go:96-103`) — exactly the behavior this
  phase must avoid on the notify worker path. The drop-on-full `enqueue`
  above is the point of the custom type; everything else deliberately mirrors
  the CallbackQueue/notifier precedents.
- **Fresh timestamp per attempt** (inside `attempt`, not per job): each retry
  is re-signed, keeping the receiver's 5-minute replay window compatible with
  backoff — this is what the Phase 2 help_text promises operators.
- **Secret read per attempt** via the `secret()` closure: live config changes
  (Phase 2 in-place mutation) apply to the very next attempt without plumbing
  the config package into this file.
- **Body close/drain discipline** (bodyclose `server/.golangci.yml:38`, gosec
  `:43`): the deferred `io.Copy(io.Discard, io.LimitReader(...))` + `Close`
  pattern both satisfies bodyclose and keeps the connection reusable while
  bounding reads. Do not use `io.ReadAll` (unbounded — the legacy client's
  mistake at `services/webhook/webhook.go:30`).
- **gosec and the variable URL:** `http.NewRequestWithContext` with an
  operator-configured URL does not trip gosec's G107 (which targets
  `http.Get`/`http.Post` taint). If a gosec finding does appear, the
  sanctioned suppression is
  `//nolint:gosec // URL is operator-configured via System Console and validated at parse time`
  on the offending line — `nolintlint` (`:59`) requires exactly this shape.
  Do not restructure to dodge the linter.
- **`exhaustive`** (`:41`): the `switch d.attempt(job)` in `deliver` covers
  all three `attemptResult` constants explicitly with no default — keep it
  that way so a new variant fails lint until handled.
- **stop() drain order** mirrors `CallbackQueue.Shutdown`
  (`utils/callbackqueue.go:56-87`): signal, wait for workers (in-flight jobs
  complete), then drain the channel synchronously. Drain attempts are
  single-shot (`attempt`, not `deliver`) — spending backoff time during
  shutdown is wrong.
- **Enqueue-after-stop** leaves the job in the channel undelivered — same
  as CallbackQueue's post-shutdown behavior; acceptable because
  `notify.Service.Shutdown` (`service.go:78-90`) nils the backend list right
  after `ShutDown` returns, so no further `BlockChanged` calls arrive.

## Task 3: `webhook_backend.go` — rewire `BlockChanged` + lifecycle

**File:** `server/services/notify/notifywebhook/webhook_backend.go` — Modify
(the Phase 2 end-state of this file; function bodies below are complete
replacements, so exact line numbers do not matter).

**(a) Imports:** add `"encoding/json"` and `"fmt"` (stdlib group).

**(b) Struct + constructor:** add a `deliverer` field and construct it in
`New` (Phase 1 `New` is at `webhook_backend.go:31-36` pre-Phase-2):

```go
// Backend delivers block change events to configured webhook endpoints.
type Backend struct {
	cfg    *config.Configuration
	logger mlog.LoggerIFace

	deliverer *deliverer

	// settingsMux guards the memoized parse of the raw setting strings
	// (Phase 2). ...retain the Phase 2 fields/comment unchanged...
	settingsMux sync.Mutex
	settings    parsedSettings
}

// New creates a webhook notification backend.
func New(params BackendParams) *Backend {
	b := &Backend{
		cfg:    params.Config,
		logger: params.Logger,
	}
	b.deliverer = newDeliverer(delivererParams{
		logger: params.Logger,
		secret: b.currentSecret,
	})
	return b
}

// currentSecret re-reads the signing secret from the shared configuration
// on every delivery attempt, so live config changes apply immediately.
func (b *Backend) currentSecret() string {
	if b.cfg == nil {
		return ""
	}
	return b.cfg.NotifyWebhookSecret
}
```

**(c) Lifecycle** — replace the Phase 1 stubs (`webhook_backend.go:38-45`
pre-Phase-2):

```go
func (b *Backend) Start() error {
	b.logger.Debug("Starting webhook notification backend")
	b.deliverer.start()
	return nil
}

func (b *Backend) ShutDown() error {
	b.logger.Debug("Stopping webhook notification backend")
	b.deliverer.stop()
	_ = b.logger.Flush()
	return nil
}
```

`ShutDown` returns nil even when `stop()` reports an incomplete drain —
`stop` already logged the Warn, and an error here would only add a redundant
Error from `notify.Service.Shutdown`'s merror (`service.go:82-89`).
At-least-once loss on shutdown is documented behavior, not an error.
`notify.Service.AddBackend` calls `Start()` at registration
(`service.go:67-75`), so workers exist before the first event.

**(d) `BlockChanged`** — full replacement (Phase 2's version ends at the
debug log; that log goes away):

```go
// BlockChanged builds the wire envelope for the event, applies the
// event filter, and enqueues one delivery job per configured URL. It
// runs on the shared notify CallbackQueue workers (service.go:94-108)
// and must never block: a full delivery queue drops with a warning.
func (b *Backend) BlockChanged(evt notify.BlockChangeEvent) error {
	if b.cfg == nil {
		return nil
	}

	urls, filter := b.currentSettings()
	if len(urls) == 0 {
		return nil
	}
	if !filter.matches(evt.Action, evt.BlockChanged.Type) {
		return nil
	}

	env := newEnvelope(evt, utils.NewID(utils.IDTypeWebhookEvent), utils.GetMillis())

	body, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("notifyWebhook marshal envelope: %w", err)
	}

	for _, url := range urls {
		if !b.deliverer.enqueue(deliveryJob{eventID: env.EventID, url: url, body: body}) {
			b.logger.Warn("notifyWebhook delivery queue full, dropping event",
				mlog.String("event_id", env.EventID),
				mlog.String("url", url),
			)
		}
	}
	return nil
}
```

Notes:

- **Marshal once, fan out per URL** — per-URL independence (one slow endpoint
  never affects another) lives at the job level; the body bytes are shared
  (jobs never mutate `body`).
- The marshal error path returns a `%w`-wrapped error (err113-compliant,
  `server/.golangci.yml:51`); `notify.Service` logs it at Error
  (`service.go:99-106`). It is unreachable in practice (model structs are
  plain data) but must not be silently swallowed.
- Drop-on-full is a Warn, not Error: it is load shedding, and the eventual
  G4 story (Factory-side reconcile polling) exists precisely because this
  path is best-effort.

## Task 4: `doc.go` — final semantics text

**File:** `server/services/notify/notifywebhook/doc.go` — Replace the package
comment in full (this text seeds the Phase 4 upstream PR body):

```go
// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Package notifywebhook implements a notification backend that delivers
// board change events to configured external HTTP endpoints as JSON
// envelopes (see Envelope). The envelope's JSON field names are a stable
// contract for external consumers.
//
// Endpoint URLs, signing secret, and event filter are read live from the
// plugin configuration (System Console); no restart is required.
//
// Delivery semantics are at-least-once and best-effort: events are
// queued in memory and delivered asynchronously by a small worker pool
// with bounded retries (network errors and 5xx retry with backoff; other
// non-2xx responses drop). A full queue, a failing endpoint that
// exhausts retries, or a server restart can lose events; retries can
// duplicate them; concurrent workers deliver out of order. Consumers
// must deduplicate by the envelope eventId and treat occurredAt as the
// ordering hint. Events fire only on the node that performed the
// mutation, so multi-node clusters do not duplicate deliveries.
//
// Requests are signed when a secret is configured: the
// X-Boards-Webhook-Signature header carries "sha256=" + lowercase hex
// HMAC-SHA256 over the decimal X-Boards-Webhook-Timestamp value, a
// literal period, and the raw request body. Each attempt is re-signed
// with a fresh timestamp; receivers should enforce a short replay
// window (five minutes recommended).
//
// The delete-event rule that blockOld duplicates blockChanged reflects
// current producer behavior in app/blocks.go:369. A producer-level contract
// test is tracked in the Phase 4 upstream notes.
package notifywebhook
```

(The single-node emission claim was verified in the parent plan against
`ws/plugin_adapter_cluster.go` being WS-only; keep the sentence.)

## Task 5: Tests

Conventions: plain `testing` + testify, table-driven, no mockery; httptest
receivers (precedent `services/webhook/webhook_test.go:18-42`);
`mlog.CreateConsoleTestLogger(t)`; `FOCALBOARD_UNIT_TESTING=1`; everything
must pass `-race`. Tests construct deliverers directly via
`newDeliverer(delivererParams{...})` with shrunk knobs — e.g.
`backoff: []time.Duration{time.Millisecond, time.Millisecond}`,
`client: &http.Client{Timeout: 250 * time.Millisecond}`, `queueSize: 1` —
never `time.Sleep` waits; synchronize on channels fed by the httptest
handler.

### 5.1 `server/services/notify/notifywebhook/signing_test.go` — Create

- `TestSignPayloadVectors` — table-driven over the three frozen vectors in
  this document (vector 3 reads
  `testdata/envelope_golden.json` via `os.ReadFile` and signs its exact
  bytes). Carry a comment: "expected values computed with OpenSSL
  (`printf '%s' '<ts>.<body>' | openssl dgst -sha256 -hmac '<secret>'`);
  these freeze the cross-repo signing contract — changes require
  coordination with consumers."
- `TestSignPayloadMaterialShape` — proves the concatenation order matters:
  `signPayload("s", 12, []byte("3x"))` ≠ `signPayload("s", 123, []byte("x"))`
  would be equal if the period were omitted; assert they differ (the period
  is load-bearing) and that both differ from signing body alone.

### 5.2 `server/services/notify/notifywebhook/delivery_test.go` — Create

Each test builds an `httptest.NewServer` whose handler records
`(*http.Request, body bytes)` onto a buffered channel, then a deliverer with
`secret: func() string { return "test-secret" }` (or empty where stated).

- `TestDelivery_SignatureRoundTrip` — enqueue one job; receiver asserts:
  `Content-Type: application/json`; `X-Boards-Webhook-Timestamp` parses as
  int64 within ±1 minute of `utils.GetMillis()`; signature verifies against
  an **independent in-test HMAC implementation** — a test-local
  `verifySignature(secret, tsHeader string, body []byte, sigHeader string) bool`
  that builds the material as one string
  (`tsHeader + "." + string(body)`), computes `crypto/hmac` over it in a
  single `Write`, hex-encodes, prepends `sha256=`, and compares with
  `hmac.Equal` on the decoded bytes — deliberately not calling
  `signPayload`, so a bug in the production concatenation cannot self-verify.
- `TestDelivery_UnsignedWhenNoSecret` — secret func returns `""`; assert the
  timestamp header is present and the signature header is absent.
- `TestDelivery_RetryThenSuccess` — handler returns 500 for the first two
  requests, 200 after (atomic counter); short backoff. **Trap (hit during
  plan validation): do not call `stop()` right after enqueue** — `stop()`
  closes `done`, and a worker sitting in the backoff select abandons the
  job by design ("delivery abandoned at shutdown"). Instead, receive all
  three requests from the handler channel (each receive with a generous
  timeout) *before* calling `stop()`; then assert the channel is empty
  (no fourth attempt), that each of the three requests carries a valid
  signature over its own timestamp (per-attempt re-signing), and that all
  three bodies are byte-identical.
- `TestDelivery_4xxNoRetry` — handler always 400; assert exactly 1 request
  and that no second request arrives (drain the channel with a short
  timeout).
- `TestDelivery_TimeoutRetries` — handler sleeps 2× the test client timeout
  on the first request, responds 200 instantly afterward; assert a second
  request arrives (transport timeout classified as retry) and total requests
  == 2.
- `TestDelivery_FullQueueDrop` — `queueSize: 1, workers: 1`; handler blocks
  on a gate channel; enqueue job A (worker picks it up), job B (fills the
  queue), job C → `enqueue` returns false; open the gate; assert A and B
  deliver and C never arrives.
- `TestDelivery_ShutdownDrainsInFlight` — fast 200 handler; `workers: 1`;
  enqueue 5 jobs and call `stop()` immediately; assert `stop()` returns true
  and all 5 requests were received (workers finish in-flight, drain
  finishes the queue — within the 5s deadline by orders of magnitude).
- `TestDelivery_StartStopIdempotent` — `start(); start(); stop(); stop()`
  without panic; a second `start()` after `stop()` spawns workers again
  (deliver one job to prove it).

### 5.3 `server/services/notify/notifywebhook/webhook_backend_test.go` — Extend

Keep all Phase 1/2 tests. Add:

- `TestBackend_EndToEndDelivery` — the integration test of the package:
  httptest receiver; real `Backend` via `New` with
  `cfg = &config.Configuration{NotifyWebhookURLs: server.URL, NotifyWebhookSecret: "test-secret", NotifyWebhookEventTypes: "update,card"}`;
  `require.NoError(t, backend.Start())`; call `BlockChanged` with the
  golden-style update/card event (reuse `goldenBlockChangeEvent()` from
  `envelope_test.go:75` — same package) and with a filtered-out comment-add
  event; then `require.NoError(t, backend.ShutDown())` (acts as the flush
  barrier). Assert exactly one request arrived; its body unmarshals to an
  `Envelope` whose `eventId` is 27 chars with prefix `e` and whose
  `blockChanged.id` matches; its signature verifies with the independent
  helper from 5.2.
- `TestBackend_TwoURLsIndependent` — two httptest servers, one always-500,
  one 200; both URLs configured (newline-joined); short backoff; one
  `BlockChanged`; `ShutDown`; assert the healthy endpoint received exactly 1
  request while the failing endpoint received `maxAttempts` — per-URL
  independence.
- Existing `TestBackend_NoopWhenUnconfigured` and `TestBackend_Interface`
  must pass unchanged (Start/ShutDown now spin real goroutines — still
  error-free with empty config).

Race detector is mandatory on the whole package: the suite exercises
enqueue-vs-worker, live secret reads, and start/stop concurrently by
construction.

## Task 6: Definition of Done + commands

```bash
# 1. Build
cd server && go build ./...

# 2. Package tests, race detector on (the phase gate)
cd server && FOCALBOARD_UNIT_TESTING=1 go test -race -count=1 ./services/notify/notifywebhook/...

# 3. Regression: Phase 1/2 surfaces untouched by this phase
cd server && FOCALBOARD_UNIT_TESTING=1 go test -race ./boards/ -run 'TestSetConfiguration|TestCreateWebhookNotifyBackend|TestWebhookBackendRegistersWithNotifyService'

# 4. Style — golangci-lint v1.64.8 (repo-pinned) + license govet
make check-style
# server-only fallback if the webapp toolchain is unavailable:
#   cd server && golangci-lint run ./...
```

**DoD checklist:**

- [ ] `signing.go` implements exactly the frozen signed material
      (`<timestamp>.<body>`) and header names; the three OpenSSL vectors pass
- [ ] `delivery.go` implements the frozen constants (queue 1000, 2 workers,
      10s client timeout, 3 attempts, 1s/4s backoff, 5s drain, 64KB body
      drain) with drop-on-full enqueue and single-attempt shutdown drain
- [ ] Retry classification exactly as frozen: 2xx success; ≥500/transport
      retry; everything else drop
- [ ] `BlockChanged` never blocks: envelope → filter → marshal-once →
      non-blocking per-URL enqueue with Warn on full
- [ ] `Start` spawns workers; `ShutDown` drains within deadline and returns
      nil; both idempotent
- [ ] `doc.go` carries the full Task 4 text verbatim
- [ ] All Task 5 tests green under `go test -race -count=1`; golden envelope
      test and all Phase 1/2 tests still green
- [ ] bodyclose/gosec clean without suppressions (or only the single
      sanctioned gosec suppression, if the linter fires on the request URL)
- [ ] License header (`// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.`
      / `// See LICENSE.txt for license information.`) on every new `.go`
      file including tests — goheader (`server/.golangci.yml:52`) +
      `mattermost-govet -license`
- [ ] `make check-style` (or documented server-only fallback) green
- [ ] No diff outside the File Change Map below
- [ ] Commit checkpoint created (local, not pushed): suggested message
      `Phase 3: HMAC signing + async bounded-retry webhook delivery`.
      Call out the signing vectors in the commit body — this commit is the
      Factory-coordination artifact for the verifier.

## File Change Map (Phase 3 complete set)

| File | Action | Content |
|---|---|---|
| `server/services/notify/notifywebhook/signing.go` | Create | Header consts + `signPayload` (Task 1) |
| `server/services/notify/notifywebhook/delivery.go` | Create | `deliverer`: queue, workers, retries, drain (Task 2) |
| `server/services/notify/notifywebhook/webhook_backend.go` | Modify | deliverer field, lifecycle, `BlockChanged` enqueue (Task 3) |
| `server/services/notify/notifywebhook/doc.go` | Modify | Final semantics text (Task 4) |
| `server/services/notify/notifywebhook/signing_test.go` | Create | Vectors + material-shape test (Task 5.1) |
| `server/services/notify/notifywebhook/delivery_test.go` | Create | httptest delivery suite (Task 5.2) |
| `server/services/notify/notifywebhook/webhook_backend_test.go` | Modify | End-to-end + per-URL tests (Task 5.3) |

## Out-of-scope guardrails (hard NOs for this phase)

- **No `plugin.json` changes beyond Phase 2's** — no new settings, no
  `make apply`, no manifest regeneration. Tunables (queue size, timeouts,
  retry counts) are frozen constants, not settings; if operators ever need
  them configurable, that is a future milestone.
- **No config plumbing changes.** `server/services/config/config.go`,
  `server/boards/configuration.go`, `server/boards/boardsapp_util.go`,
  `server/boards/boardsapp.go` stay exactly as Phase 2 left them.
- **No envelope changes.** `envelope.go`, `envelope_test.go`, and
  `testdata/envelope_golden.json` stay byte-identical.
- **No settings-parsing changes.** `settings.go` and Phase 2's
  `currentSettings()` memoization are consumed, not modified.
- **Do NOT touch the legacy `services/webhook` client**, its 7 call sites
  (`app/blocks.go:112,181,227,325,421`, `app/boards_and_blocks.go:61,149`),
  or the dormant `WebhookUpdate`/`Secret` config fields.
- **No changes to** `server/services/notify/service.go`,
  `server/server/server.go`, `server/utils/callbackqueue.go`,
  `server/app/app.go`, or any other notify backend — the shutdown budget is
  respected from inside this package, not by re-plumbing the server.
- **No persistence, no outbox, no ordering guarantees** — G4's documented
  at-least-once/in-memory/unordered semantics are the spec; the reconcile
  fallback is Factory-side work, not this repo's.
- **No new dependencies; no go.mod changes.** `crypto/hmac`, `crypto/sha256`,
  `net/http`, `httptest` are all stdlib.
- **Do not weaken or skip linters**; the only sanctioned suppression is the
  conditional gosec one named in Task 2's notes.

## Implementation Summary

Implemented the frozen HMAC-SHA256 signing contract and all three OpenSSL test
vectors. Added bounded non-blocking queueing, two-worker asynchronous delivery,
per-attempt signing, retry classification/backoff, bounded response draining,
and context-bounded shutdown draining. The production HTTP client rejects
redirects so signed POST bodies cannot be forwarded to arbitrary targets.
Rewired backend lifecycle and per-URL fanout, finalized the package semantics
documentation, and added signing, redirect, hung-drain, race, delivery, and
end-to-end `httptest` coverage. The required server build, race-enabled notify
and Boards suites, and pinned focused golangci-lint command all pass.
