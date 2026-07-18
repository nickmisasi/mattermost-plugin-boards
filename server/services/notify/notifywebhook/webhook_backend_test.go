// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package notifywebhook

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattermost/mattermost-plugin-boards/server/model"
	"github.com/mattermost/mattermost-plugin-boards/server/services/config"
	"github.com/mattermost/mattermost-plugin-boards/server/services/notify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost/server/public/shared/mlog"
)

var _ notify.Backend = (*Backend)(nil)

func configWithWebhookSettings(settings config.WebhookSettings) *config.Configuration {
	return &config.Configuration{
		NotifyWebhookSettings: config.NewWebhookSettingsStore(settings),
	}
}

func TestBackend_NoopWhenUnconfigured(t *testing.T) {
	evt := notify.BlockChangeEvent{
		BlockChanged: &model.Block{ID: "block-id"},
	}

	t.Run("empty configuration", func(t *testing.T) {
		backend := New(BackendParams{
			Config: &config.Configuration{},
			Logger: mlog.CreateConsoleTestLogger(t),
		})

		require.NoError(t, backend.BlockChanged(evt))
	})

	t.Run("nil configuration", func(t *testing.T) {
		backend := New(BackendParams{
			Config: nil,
			Logger: mlog.CreateConsoleTestLogger(t),
		})

		require.NoError(t, backend.BlockChanged(evt))
	})
}

func TestBackend_Interface(t *testing.T) {
	backend := New(BackendParams{
		Config: &config.Configuration{},
		Logger: mlog.CreateConsoleTestLogger(t),
	})

	assert.Equal(t, "notifyWebhook", backend.Name())
	require.NoError(t, backend.Start())
	require.NoError(t, backend.ShutDown())
}

func TestBackend_LiveConfigChange(t *testing.T) {
	cfg := configWithWebhookSettings(config.WebhookSettings{})
	backend := New(BackendParams{
		Config: cfg,
		Logger: mlog.CreateConsoleTestLogger(t),
	})

	urls, filter := backend.currentSettings()
	assert.Empty(t, urls)
	assert.True(t, filter.matches(notify.Add, model.TypeCard))

	cfg.NotifyWebhookSettings.Store(config.WebhookSettings{URLs: "https://a.example/hook"})
	urls, _ = backend.currentSettings()
	assert.Equal(t, []string{"https://a.example/hook"}, urls)

	cfg.NotifyWebhookSettings.Store(config.WebhookSettings{URLs: "https://a.example/hook\nhttp://evil.example"})
	urls, _ = backend.currentSettings()
	assert.Equal(t, []string{"https://a.example/hook"}, urls)

	cfg.NotifyWebhookSettings.Store(config.WebhookSettings{
		URLs:       "https://a.example/hook\nhttp://evil.example",
		EventTypes: "add,card",
	})
	_, filter = backend.currentSettings()
	assert.True(t, filter.matches(notify.Add, model.TypeCard))
	assert.False(t, filter.matches(notify.Update, model.TypeText))

	cfg.NotifyWebhookSettings.Store(config.WebhookSettings{})
	urls, filter = backend.currentSettings()
	assert.Empty(t, urls)
	assert.True(t, filter.matches(notify.Update, model.TypeText))
}

func TestBackend_BlockChangedRespectsFilter(t *testing.T) {
	backend := New(BackendParams{
		Config: configWithWebhookSettings(config.WebhookSettings{
			URLs:       "https://a.example/hook",
			EventTypes: "comment",
		}),
		Logger: mlog.CreateConsoleTestLogger(t),
	})

	cardEvent := notify.BlockChangeEvent{
		Action:       notify.Update,
		BlockChanged: &model.Block{ID: "card-id", Type: model.TypeCard},
	}
	commentEvent := notify.BlockChangeEvent{
		Action:       notify.Update,
		BlockChanged: &model.Block{ID: "comment-id", Type: model.TypeComment},
	}

	require.NoError(t, backend.BlockChanged(cardEvent))
	assert.Empty(t, backend.deliverer.queue)
	require.NoError(t, backend.BlockChanged(commentEvent))
	assert.Len(t, backend.deliverer.queue, 1)
}

func TestBackend_EndToEndDelivery(t *testing.T) {
	requests := make(chan receivedRequest, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- recordRequest(t, r)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := configWithWebhookSettings(config.WebhookSettings{
		URLs:       server.URL,
		Secret:     "test-secret",
		EventTypes: "update,card",
	})
	backend := New(BackendParams{
		Config: cfg,
		Logger: mlog.CreateConsoleTestLogger(t),
	})
	require.NoError(t, backend.Start())

	event := goldenBlockChangeEvent()
	require.NoError(t, backend.BlockChanged(event))

	filteredEvent := notify.BlockChangeEvent{
		Action:       notify.Add,
		BlockChanged: &model.Block{ID: "filtered-comment", Type: model.TypeComment},
	}
	require.NoError(t, backend.BlockChanged(filteredEvent))
	require.NoError(t, backend.ShutDown())

	request := receiveRequest(t, requests)
	select {
	case <-requests:
		t.Fatal("filtered event was delivered")
	default:
	}

	var envelope Envelope
	require.NoError(t, json.Unmarshal(request.body, &envelope))
	assert.Len(t, envelope.EventID, 27)
	assert.Equal(t, "e", envelope.EventID[:1])
	assert.Equal(t, event.BlockChanged.ID, envelope.BlockChanged.ID)
	assert.True(t, verifySignature("test-secret", request.header.Get(HeaderTimestamp), request.body, request.header.Get(HeaderSignature)))
}

func TestBackend_TwoURLsIndependent(t *testing.T) {
	var healthyAttempts atomic.Int32
	healthyRequests := make(chan struct{}, 1)
	healthyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		healthyAttempts.Add(1)
		healthyRequests <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer healthyServer.Close()

	var failingAttempts atomic.Int32
	failingRequests := make(chan struct{}, maxAttempts)
	failingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		failingAttempts.Add(1)
		failingRequests <- struct{}{}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failingServer.Close()

	backend := New(BackendParams{
		Config: configWithWebhookSettings(config.WebhookSettings{
			URLs: healthyServer.URL + "\n" + failingServer.URL,
		}),
		Logger: mlog.CreateConsoleTestLogger(t),
	})
	backend.deliverer.backoff = []time.Duration{time.Millisecond, time.Millisecond}
	backend.deliverer.client = &http.Client{Timeout: 250 * time.Millisecond}
	require.NoError(t, backend.Start())
	require.NoError(t, backend.BlockChanged(goldenBlockChangeEvent()))

	select {
	case <-healthyRequests:
	case <-time.After(2 * time.Second):
		t.Fatal("healthy endpoint did not receive its delivery")
	}
	for range maxAttempts {
		select {
		case <-failingRequests:
		case <-time.After(2 * time.Second):
			t.Fatal("failing endpoint did not receive all retry attempts")
		}
	}
	require.NoError(t, backend.ShutDown())

	assert.Equal(t, int32(1), healthyAttempts.Load())
	assert.Equal(t, int32(maxAttempts), failingAttempts.Load())
}

func TestBackend_ConcurrentConfigPublication(t *testing.T) {
	requests := make(chan receivedRequest, 512)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- recordRequest(t, r)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := configWithWebhookSettings(config.WebhookSettings{
		URLs:       server.URL,
		Secret:     "secret-a",
		EventTypes: "update,card",
	})
	backend := New(BackendParams{
		Config: cfg,
		Logger: mlog.CreateConsoleTestLogger(t),
	})
	require.NoError(t, backend.Start())

	event := goldenBlockChangeEvent()
	const iterations = 200
	var concurrentWork sync.WaitGroup
	concurrentWork.Add(2)
	go func() {
		defer concurrentWork.Done()
		for i := range iterations {
			secret := "secret-a"
			if i%2 == 0 {
				secret = "secret-b"
			}
			cfg.NotifyWebhookSettings.Store(config.WebhookSettings{
				URLs:       server.URL,
				Secret:     secret,
				EventTypes: "update,card",
			})
		}
	}()
	go func() {
		defer concurrentWork.Done()
		for range iterations {
			require.NoError(t, backend.BlockChanged(event))
		}
	}()
	concurrentWork.Wait()
	require.NoError(t, backend.ShutDown())

	delivered := len(requests)
	require.Positive(t, delivered)
	for range delivered {
		request := <-requests
		timestamp := request.header.Get(HeaderTimestamp)
		signature := request.header.Get(HeaderSignature)
		assert.True(t,
			verifySignature("secret-a", timestamp, request.body, signature) ||
				verifySignature("secret-b", timestamp, request.body, signature),
		)
	}
}
