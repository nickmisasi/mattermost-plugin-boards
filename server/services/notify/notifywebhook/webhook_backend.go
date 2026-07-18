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

	// settingsMux guards the parsed form of the atomically published settings
	// snapshot. Re-parsing only when the snapshot pointer changes logs
	// validation warnings once per configuration change.
	settingsMux sync.Mutex
	settings    parsedSettings
}

// parsedSettings caches the parsed form of one immutable settings snapshot.
// The zero value is correct for empty config: no URLs, filter that matches
// everything.
type parsedSettings struct {
	snapshot *config.WebhookSettings
	urls     []string
	filter   eventFilter
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
	settings := b.webhookSettings()
	if settings == nil {
		return ""
	}
	return settings.Secret
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

// webhookSettings atomically loads the current immutable settings snapshot.
func (b *Backend) webhookSettings() *config.WebhookSettings {
	if b.cfg == nil || b.cfg.NotifyWebhookSettings == nil {
		return nil
	}
	return b.cfg.NotifyWebhookSettings.Load()
}

// currentSettings returns the endpoint URLs and event filter parsed from the
// current immutable plugin-configuration snapshot.
func (b *Backend) currentSettings() ([]string, eventFilter) {
	snapshot := b.webhookSettings()

	b.settingsMux.Lock()
	defer b.settingsMux.Unlock()

	if snapshot == b.settings.snapshot {
		return b.settings.urls, b.settings.filter
	}

	if snapshot == nil {
		b.settings = parsedSettings{}
		return nil, eventFilter{}
	}

	urls, invalid := parseWebhookURLs(snapshot.URLs)
	for _, entry := range invalid {
		b.logger.Warn("notifyWebhook ignoring invalid webhook URL",
			mlog.String("url", entry),
		)
	}

	filter, unknown := parseEventFilter(snapshot.EventTypes)
	for _, token := range unknown {
		b.logger.Warn("notifyWebhook ignoring unknown event filter token",
			mlog.String("token", token),
		)
	}

	b.settings = parsedSettings{
		snapshot: snapshot,
		urls:     urls,
		filter:   filter,
	}
	return b.settings.urls, b.settings.filter
}
