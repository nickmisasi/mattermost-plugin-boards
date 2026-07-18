// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package notifywebhook

import (
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
}

// New creates a webhook notification backend.
func New(params BackendParams) *Backend {
	return &Backend{
		cfg:    params.Config,
		logger: params.Logger,
	}
}

func (b *Backend) Start() error {
	return nil
}

func (b *Backend) ShutDown() error {
	_ = b.logger.Flush()
	return nil
}

func (b *Backend) Name() string {
	return backendName
}

// BlockChanged builds the wire envelope for the event and hands it to
// delivery. Delivery is added in a later phase; with no URLs configurable
// yet this is a no-op, so the backend is inert on every install.
func (b *Backend) BlockChanged(evt notify.BlockChangeEvent) error {
	urls := b.configuredURLs()
	if len(urls) == 0 {
		return nil
	}

	env := newEnvelope(evt, utils.NewID(utils.IDTypeWebhookEvent), utils.GetMillis())

	b.logger.Debug("notifyWebhook event built; delivery not yet implemented",
		mlog.String("event_id", env.EventID),
		mlog.String("action", env.Action),
		mlog.String("block_id", env.BlockChanged.ID),
	)
	return nil
}

// configuredURLs returns the outbound endpoint URLs from the shared plugin
// configuration. The pointer is re-read on every event because the server
// mutates the shared Configuration in place on config change
// (boards/configuration.go OnConfigurationChange -> server.UpdateAppConfig);
// backends are never rebuilt. The URL setting is introduced in a later
// phase; until then this always returns nil.
func (b *Backend) configuredURLs() []string { //nolint:unparam // config fields for URLs land in a follow-up; the accessor is the seam
	if b.cfg == nil {
		return nil
	}
	return nil
}
