// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package notifywebhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattermost/mattermost-plugin-boards/server/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost/server/public/shared/mlog"
)

type receivedRequest struct {
	header http.Header
	body   []byte
}

func recordRequest(t *testing.T, r *http.Request) receivedRequest {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	require.NoError(t, err)
	return receivedRequest{
		header: r.Header.Clone(),
		body:   body,
	}
}

func verifySignature(secret, timestampHeader string, body []byte, signatureHeader string) bool {
	if !strings.HasPrefix(signatureHeader, signaturePrefix) {
		return false
	}
	actual, err := hex.DecodeString(strings.TrimPrefix(signatureHeader, signaturePrefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestampHeader + "." + string(body)))
	return hmac.Equal(actual, mac.Sum(nil))
}

func newTestDeliverer(t *testing.T, secret func() string, queueSize int, client *http.Client) *deliverer {
	t.Helper()
	return newDeliverer(delivererParams{
		logger:    mlog.CreateConsoleTestLogger(t),
		secret:    secret,
		queueSize: queueSize,
		workers:   1,
		backoff:   []time.Duration{time.Millisecond, time.Millisecond},
		client:    client,
	})
}

func receiveRequest(t *testing.T, requests <-chan receivedRequest) receivedRequest {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for webhook request")
		return receivedRequest{}
	}
}

func TestDelivery_SignatureRoundTrip(t *testing.T) {
	requests := make(chan receivedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- recordRequest(t, r)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	deliverer := newTestDeliverer(t, func() string { return "test-secret" }, 1, &http.Client{Timeout: 250 * time.Millisecond})
	deliverer.start()
	require.True(t, deliverer.enqueue(deliveryJob{eventID: "event-1", url: server.URL, body: []byte(`{"event":"one"}`)}))

	request := receiveRequest(t, requests)
	require.True(t, deliverer.stop())
	assert.Equal(t, "application/json", request.header.Get("Content-Type"))

	timestampHeader := request.header.Get(HeaderTimestamp)
	timestamp, err := strconv.ParseInt(timestampHeader, 10, 64)
	require.NoError(t, err)
	assert.WithinDuration(t, time.UnixMilli(utils.GetMillis()), time.UnixMilli(timestamp), time.Minute)
	assert.True(t, verifySignature("test-secret", timestampHeader, request.body, request.header.Get(HeaderSignature)))
}

func TestDelivery_UnsignedWhenNoSecret(t *testing.T) {
	requests := make(chan receivedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- recordRequest(t, r)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	deliverer := newTestDeliverer(t, func() string { return "" }, 1, &http.Client{Timeout: 250 * time.Millisecond})
	deliverer.start()
	require.True(t, deliverer.enqueue(deliveryJob{eventID: "event-1", url: server.URL, body: []byte(`{}`)}))

	request := receiveRequest(t, requests)
	require.True(t, deliverer.stop())
	assert.NotEmpty(t, request.header.Get(HeaderTimestamp))
	assert.Empty(t, request.header.Get(HeaderSignature))
}

func TestDelivery_RetryThenSuccess(t *testing.T) {
	requests := make(chan receivedRequest, maxAttempts)
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- recordRequest(t, r)
		if attempts.Add(1) < maxAttempts {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	deliverer := newTestDeliverer(t, func() string { return "test-secret" }, 1, &http.Client{Timeout: 250 * time.Millisecond})
	deliverer.start()
	body := []byte(`{"event":"retry"}`)
	require.True(t, deliverer.enqueue(deliveryJob{eventID: "event-retry", url: server.URL, body: body}))

	for range maxAttempts {
		request := receiveRequest(t, requests)
		assert.Equal(t, body, request.body)
		assert.True(t, verifySignature("test-secret", request.header.Get(HeaderTimestamp), request.body, request.header.Get(HeaderSignature)))
	}
	require.True(t, deliverer.stop())
	assert.Equal(t, int32(maxAttempts), attempts.Load())
	select {
	case <-requests:
		t.Fatal("unexpected fourth delivery attempt")
	default:
	}
}

func TestDelivery_4xxNoRetry(t *testing.T) {
	requests := make(chan receivedRequest, maxAttempts)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- recordRequest(t, r)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	deliverer := newTestDeliverer(t, func() string { return "test-secret" }, 1, &http.Client{Timeout: 250 * time.Millisecond})
	deliverer.start()
	require.True(t, deliverer.enqueue(deliveryJob{eventID: "event-400", url: server.URL, body: []byte(`{}`)}))
	receiveRequest(t, requests)

	select {
	case <-requests:
		t.Fatal("4xx response was retried")
	case <-time.After(25 * time.Millisecond):
	}
	require.True(t, deliverer.stop())
}

func TestDelivery_TimeoutRetries(t *testing.T) {
	requests := make(chan receivedRequest, maxAttempts)
	var attempts atomic.Int32
	clientTimeout := 25 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- recordRequest(t, r)
		if attempts.Add(1) == 1 {
			time.Sleep(2 * clientTimeout)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	deliverer := newTestDeliverer(t, func() string { return "test-secret" }, 1, &http.Client{Timeout: clientTimeout})
	deliverer.start()
	require.True(t, deliverer.enqueue(deliveryJob{eventID: "event-timeout", url: server.URL, body: []byte(`{}`)}))

	receiveRequest(t, requests)
	receiveRequest(t, requests)
	require.True(t, deliverer.stop())
	assert.Equal(t, int32(2), attempts.Load())
}

func TestDelivery_FullQueueDrop(t *testing.T) {
	requests := make(chan receivedRequest, 3)
	gate := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- recordRequest(t, r)
		<-gate
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	deliverer := newTestDeliverer(t, func() string { return "" }, 1, &http.Client{Timeout: 250 * time.Millisecond})
	deliverer.start()
	require.True(t, deliverer.enqueue(deliveryJob{eventID: "A", url: server.URL, body: []byte("A")}))
	first := receiveRequest(t, requests)
	require.True(t, deliverer.enqueue(deliveryJob{eventID: "B", url: server.URL, body: []byte("B")}))
	assert.False(t, deliverer.enqueue(deliveryJob{eventID: "C", url: server.URL, body: []byte("C")}))
	close(gate)

	second := receiveRequest(t, requests)
	require.True(t, deliverer.stop())
	assert.Equal(t, []byte("A"), first.body)
	assert.Equal(t, []byte("B"), second.body)
	select {
	case request := <-requests:
		t.Fatalf("dropped job was delivered: %s", request.body)
	default:
	}
}

func TestDelivery_ShutdownDrainsInFlight(t *testing.T) {
	requests := make(chan receivedRequest, 5)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- recordRequest(t, r)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	deliverer := newTestDeliverer(t, func() string { return "" }, 5, &http.Client{Timeout: 250 * time.Millisecond})
	deliverer.start()
	for i := range 5 {
		require.True(t, deliverer.enqueue(deliveryJob{
			eventID: strconv.Itoa(i),
			url:     server.URL,
			body:    []byte(strconv.Itoa(i)),
		}))
	}

	require.True(t, deliverer.stop())
	assert.Len(t, requests, 5)
}

func TestDelivery_StartStopIdempotent(t *testing.T) {
	requests := make(chan receivedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- recordRequest(t, r)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	deliverer := newTestDeliverer(t, func() string { return "" }, 1, &http.Client{Timeout: 250 * time.Millisecond})
	deliverer.start()
	deliverer.start()
	require.True(t, deliverer.stop())
	require.True(t, deliverer.stop())

	deliverer.start()
	require.True(t, deliverer.enqueue(deliveryJob{eventID: "after-restart", url: server.URL, body: []byte(`{}`)}))
	receiveRequest(t, requests)
	require.True(t, deliverer.stop())
}
