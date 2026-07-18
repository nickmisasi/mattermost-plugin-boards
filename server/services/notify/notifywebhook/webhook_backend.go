// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Package notifywebhook is a proof-of-concept notify backend that delivers
// block change events to an external HTTP endpoint. PoC scope: the endpoint
// is read from the FOCALBOARD_WEBHOOK_POC_URL environment variable; a real
// implementation would use plugin settings, HMAC signing, and retries.
package notifywebhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/mattermost/mattermost-plugin-boards/server/model"
	"github.com/mattermost/mattermost-plugin-boards/server/services/notify"

	"github.com/mattermost/mattermost/server/public/shared/mlog"
)

const backendName = "notifyWebhook"

type payload struct {
	Action       string             `json:"action"`
	TeamID       string             `json:"teamId"`
	Board        *model.Board       `json:"board,omitempty"`
	Card         *model.Block       `json:"card,omitempty"`
	BlockChanged *model.Block       `json:"blockChanged,omitempty"`
	BlockOld     *model.Block       `json:"blockOld,omitempty"`
	ModifiedBy   *model.BoardMember `json:"modifiedBy,omitempty"`
	Timestamp    int64              `json:"timestamp"`
}

type Backend struct {
	url    string
	client *http.Client
	logger mlog.LoggerIFace
}

func New(logger mlog.LoggerIFace) *Backend {
	return &Backend{
		url:    os.Getenv("FOCALBOARD_WEBHOOK_POC_URL"),
		client: &http.Client{Timeout: 5 * time.Second},
		logger: logger,
	}
}

func (b *Backend) Start() error {
	b.logger.Info("Starting notifyWebhook backend", mlog.String("url", b.url))
	return nil
}

func (b *Backend) ShutDown() error {
	return nil
}

func (b *Backend) Name() string {
	return backendName
}

func (b *Backend) BlockChanged(evt notify.BlockChangeEvent) error {
	if b.url == "" {
		return nil
	}

	body, err := json.Marshal(payload{
		Action:       string(evt.Action),
		TeamID:       evt.TeamID,
		Board:        evt.Board,
		Card:         evt.Card,
		BlockChanged: evt.BlockChanged,
		BlockOld:     evt.BlockOld,
		ModifiedBy:   evt.ModifiedBy,
		Timestamp:    model.GetMillis(),
	})
	if err != nil {
		return fmt.Errorf("notifyWebhook: marshal event: %w", err)
	}

	resp, err := b.client.Post(b.url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("notifyWebhook: deliver event: %w", err)
	}
	defer resp.Body.Close()

	b.logger.Debug("notifyWebhook delivered",
		mlog.String("action", string(evt.Action)),
		mlog.Int("status", resp.StatusCode),
	)
	return nil
}
