// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package notifywebhook

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/mattermost/mattermost-plugin-boards/server/model"
	"github.com/mattermost/mattermost-plugin-boards/server/services/notify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "update golden files")

func TestEnvelopeJSON(t *testing.T) {
	evt := goldenBlockChangeEvent()
	env := newEnvelope(evt, "e7g8i9j3k5m6n7p8q9r3s5t6u7w", int64(1784500000000))

	actual, err := json.MarshalIndent(env, "", "  ")
	require.NoError(t, err)

	goldenPath := filepath.Join("testdata", "envelope_golden.json")
	if *update {
		// this file freezes the cross-repo webhook contract; changes require coordination with consumers.
		require.NoError(t, os.WriteFile(goldenPath, actual, 0o600))
		return
	}

	expected, err := os.ReadFile(goldenPath)
	require.NoError(t, err)
	assert.Equal(t, string(expected), string(actual))
}

func TestEnvelopeJSONPresence(t *testing.T) {
	t.Run("add omits blockOld", func(t *testing.T) {
		evt := goldenBlockChangeEvent()
		evt.Action = notify.Add
		evt.BlockOld = nil

		actual, err := json.Marshal(newEnvelope(evt, "event-id", int64(1)))
		require.NoError(t, err)
		assert.NotContains(t, string(actual), `"blockOld"`)
	})

	t.Run("nil card omits card", func(t *testing.T) {
		evt := goldenBlockChangeEvent()
		evt.Card = nil

		actual, err := json.Marshal(newEnvelope(evt, "event-id", int64(1)))
		require.NoError(t, err)
		assert.NotContains(t, string(actual), `"card":`)
	})
}

func goldenBlockChangeEvent() notify.BlockChangeEvent {
	const (
		teamID  = "tjq3g5n8e7y5um6kf9r3s5t6u7w"
		boardID = "b8f6h3k5m7p9r3s5t7v9w3x5y7z"
		userID  = "uo5d8m3k5n7p9r3s5t7v9w3x5y7"
		cardID  = "c4d6f8h3k5m7p9r3s5t7v9w3x5y"
	)

	board := &model.Board{
		ID:              boardID,
		TeamID:          teamID,
		ChannelID:       "",
		CreatedBy:       userID,
		ModifiedBy:      userID,
		Type:            model.BoardTypeOpen,
		MinimumRole:     model.BoardRoleEditor,
		Title:           "Factory Runs",
		Description:     "",
		Icon:            "🏭",
		ShowDescription: false,
		IsTemplate:      false,
		TemplateVersion: 0,
		Properties:      map[string]interface{}{},
		CardProperties: []map[string]interface{}{
			{
				"id":   "prop-stage-id-000000000000000",
				"name": "Stage",
				"type": "select",
				"options": []interface{}{
					map[string]interface{}{
						"id":    "opt-queued-000000000000000000",
						"value": "Queued",
						"color": "propColorGray",
					},
					map[string]interface{}{
						"id":    "opt-inreview-0000000000000000",
						"value": "In Review",
						"color": "propColorYellow",
					},
				},
			},
		},
		CreateAt: 1784400000000,
		UpdateAt: 1784490000000,
		DeleteAt: 0,
	}

	card := &model.Block{
		ID:         cardID,
		ParentID:   boardID,
		CreatedBy:  userID,
		ModifiedBy: userID,
		Schema:     1,
		Type:       model.TypeCard,
		Title:      "Fix login crash (run #42)",
		Fields: map[string]interface{}{
			"contentOrder": []interface{}{},
			"icon":         "🤖",
			"isTemplate":   false,
			"properties": map[string]interface{}{
				"prop-stage-id-000000000000000": "opt-inreview-0000000000000000",
			},
		},
		CreateAt: 1784410000000,
		UpdateAt: 1784500000000,
		DeleteAt: 0,
		BoardID:  boardID,
	}

	blockOld := &model.Block{
		ID:         cardID,
		ParentID:   boardID,
		CreatedBy:  userID,
		ModifiedBy: userID,
		Schema:     1,
		Type:       model.TypeCard,
		Title:      "Fix login crash (run #42)",
		Fields: map[string]interface{}{
			"contentOrder": []interface{}{},
			"icon":         "🤖",
			"isTemplate":   false,
			"properties": map[string]interface{}{
				"prop-stage-id-000000000000000": "opt-queued-000000000000000000",
			},
		},
		CreateAt: 1784410000000,
		UpdateAt: 1784490000000,
		DeleteAt: 0,
		BoardID:  boardID,
	}

	modifiedBy := &model.BoardMember{
		BoardID:         boardID,
		UserID:          userID,
		Roles:           "",
		MinimumRole:     "",
		SchemeAdmin:     true,
		SchemeEditor:    false,
		SchemeCommenter: false,
		SchemeViewer:    false,
		Synthetic:       false,
	}

	return notify.BlockChangeEvent{
		Action:       notify.Update,
		TeamID:       teamID,
		Board:        board,
		Card:         card,
		BlockChanged: card,
		BlockOld:     blockOld,
		ModifiedBy:   modifiedBy,
	}
}
