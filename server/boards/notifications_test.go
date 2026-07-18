// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package boards

import (
	"testing"

	"github.com/mattermost/mattermost-plugin-boards/server/model"
	"github.com/mattermost/mattermost-plugin-boards/server/services/config"
	"github.com/mattermost/mattermost-plugin-boards/server/services/notify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost/server/public/shared/mlog"
)

func TestCreateWebhookNotifyBackend(t *testing.T) {
	params := notifyBackendParams{
		cfg:    &config.Configuration{},
		logger: mlog.CreateConsoleTestLogger(t),
	}

	backend := createWebhookNotifyBackend(params)

	assert.NotNil(t, backend)
	assert.Equal(t, "notifyWebhook", backend.Name())
}

func TestWebhookBackendRegistersWithNotifyService(t *testing.T) {
	params := notifyBackendParams{
		cfg:    &config.Configuration{},
		logger: mlog.CreateConsoleTestLogger(t),
	}

	service, err := notify.New(mlog.CreateConsoleTestLogger(t), createWebhookNotifyBackend(params))
	require.NoError(t, err)

	evt := notify.BlockChangeEvent{
		BlockChanged: &model.Block{ID: "block-id"},
	}
	assert.NotPanics(t, func() {
		service.BlockChanged(evt)
	})
}
