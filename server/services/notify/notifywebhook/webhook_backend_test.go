// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package notifywebhook

import (
	"testing"

	"github.com/mattermost/mattermost-plugin-boards/server/model"
	"github.com/mattermost/mattermost-plugin-boards/server/services/config"
	"github.com/mattermost/mattermost-plugin-boards/server/services/notify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost/server/public/shared/mlog"
)

var _ notify.Backend = (*Backend)(nil)

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
