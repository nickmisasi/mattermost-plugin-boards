// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package notifywebhook

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/mattermost/mattermost-plugin-boards/server/services/config"
	"github.com/mattermost/mattermost-plugin-boards/server/services/notify"
	"github.com/mattermost/mattermost-plugin-boards/server/utils"

	"github.com/mattermost/mattermost/server/public/shared/mlog"
)

const (
	backendName = "notifyWebhook"
)

// BackendParams are the required inputs for creating a webhook backend.
type BackendParams struct {
	Config *config.Configuration
	Logger mlog.LoggerIFace
}

// Backend delivers block change events to configured webhook endpoints.
type Backend struct {
	cfg    *config.Configuration
	logger mlog.LoggerIFace

	deliverer *deliverer

	// settingsMux guards the memoized parse of the raw setting strings.
	// The raw strings themselves live on the shared cfg pointer, which the
	// server mutates in place on config change; re-parsing only when a raw
	// value changes keeps the per-event cost at two string compares and
	// logs validation warnings once per change instead of once per event.
	settingsMux sync.Mutex
	settings    parsedSettings
}

// parsedSettings caches the parsed form of the two parse-worthy settings,
// keyed by the raw strings they were parsed from. The zero value is correct
// for empty config: no URLs, filter that matches everything.
type parsedSettings struct {
	rawURLs   string
	rawFilter string
	urls      []string
	filter    eventFilter
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

func (b *Backend) Name() string {
	return backendName
}

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

// currentSettings returns the endpoint URLs and event filter parsed from
// the shared plugin configuration. The cfg pointer is re-read on every
// event because the server mutates the shared Configuration in place on
// config change (boards/configuration.go OnConfigurationChange ->
// server.UpdateAppConfig); backends are never rebuilt.
func (b *Backend) currentSettings() ([]string, eventFilter) {
	rawURLs := b.cfg.NotifyWebhookURLs
	rawFilter := b.cfg.NotifyWebhookEventTypes

	b.settingsMux.Lock()
	defer b.settingsMux.Unlock()

	if rawURLs != b.settings.rawURLs {
		urls, invalid := parseWebhookURLs(rawURLs)
		for _, entry := range invalid {
			b.logger.Warn("notifyWebhook ignoring invalid webhook URL",
				mlog.String("url", entry),
			)
		}
		b.settings.rawURLs = rawURLs
		b.settings.urls = urls
	}

	if rawFilter != b.settings.rawFilter {
		filter, unknown := parseEventFilter(rawFilter)
		for _, token := range unknown {
			b.logger.Warn("notifyWebhook ignoring unknown event filter token",
				mlog.String("token", token),
			)
		}
		b.settings.rawFilter = rawFilter
		b.settings.filter = filter
	}

	return b.settings.urls, b.settings.filter
}
