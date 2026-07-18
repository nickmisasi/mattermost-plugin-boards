// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package notifywebhook

import (
	"testing"

	"github.com/mattermost/mattermost-plugin-boards/server/model"
	"github.com/mattermost/mattermost-plugin-boards/server/services/notify"
	"github.com/stretchr/testify/assert"
)

func TestParseWebhookURLs(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		valid   []string
		invalid []string
	}{
		{name: "empty"},
		{name: "single https", raw: "https://a.example/hook", valid: []string{"https://a.example/hook"}},
		{name: "multiple preserve order", raw: "https://a.example/hook\nhttps://b.example/hook", valid: []string{"https://a.example/hook", "https://b.example/hook"}},
		{name: "CRLF", raw: "https://a.example/hook\r\n", valid: []string{"https://a.example/hook"}},
		{name: "spaces and blank lines", raw: " \n https://a.example/hook \n\t\n", valid: []string{"https://a.example/hook"}},
		{name: "localhost", raw: "http://localhost:8065/plugins/factory/hook", valid: []string{"http://localhost:8065/plugins/factory/hook"}},
		{name: "IPv4 loopback", raw: "http://127.0.0.1:9000/hook", valid: []string{"http://127.0.0.1:9000/hook"}},
		{name: "IPv6 loopback", raw: "http://[::1]:9000/hook", valid: []string{"http://[::1]:9000/hook"}},
		{name: "reject off-box http", raw: "http://example.com/hook", invalid: []string{"http://example.com/hook"}},
		{name: "reject unsupported scheme", raw: "ftp://example.com/hook", invalid: []string{"ftp://example.com/hook"}},
		{name: "reject malformed and empty host", raw: "not a url\nhttps://", invalid: []string{"not a url", "https://"}},
		{
			name:  "duplicate configured lines each get delivery",
			raw:   "https://a.example/hook\nhttps://a.example/hook",
			valid: []string{"https://a.example/hook", "https://a.example/hook"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			valid, invalid := parseWebhookURLs(tt.raw)
			assert.Equal(t, tt.valid, valid)
			assert.Equal(t, tt.invalid, invalid)
		})
	}
}

func TestParseEventFilter(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantActions []notify.Action
		wantTypes   []model.BlockType
		wantUnknown []string
	}{
		{name: "empty"},
		{name: "whitespace only", raw: "   "},
		{name: "action", raw: "add", wantActions: []notify.Action{notify.Add}},
		{name: "types", raw: "card,comment", wantTypes: []model.BlockType{model.TypeCard, model.TypeComment}},
		{name: "both dimensions", raw: "add,delete,card", wantActions: []notify.Action{notify.Add, notify.Delete}, wantTypes: []model.BlockType{model.TypeCard}},
		{name: "mixed case and spaces", raw: "Add, CARD", wantActions: []notify.Action{notify.Add}, wantTypes: []model.BlockType{model.TypeCard}},
		{name: "empty token", raw: "add,,card", wantActions: []notify.Action{notify.Add}, wantTypes: []model.BlockType{model.TypeCard}},
		{name: "unknown token", raw: "add,bogus,card", wantActions: []notify.Action{notify.Add}, wantTypes: []model.BlockType{model.TypeCard}, wantUnknown: []string{"bogus"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter, unknown := parseEventFilter(tt.raw)
			assert.Equal(t, tt.wantUnknown, unknown)
			assert.Len(t, filter.actions, len(tt.wantActions))
			for _, action := range tt.wantActions {
				assert.Contains(t, filter.actions, action)
			}
			assert.Len(t, filter.types, len(tt.wantTypes))
			for _, blockType := range tt.wantTypes {
				assert.Contains(t, filter.types, blockType)
			}
		})
	}

	allBlockTypes := []struct {
		token     string
		blockType model.BlockType
	}{
		{token: "board", blockType: model.TypeBoard},
		{token: "card", blockType: model.TypeCard},
		{token: "view", blockType: model.TypeView},
		{token: "text", blockType: model.TypeText},
		{token: "checkbox", blockType: model.TypeCheckbox},
		{token: "comment", blockType: model.TypeComment},
		{token: "image", blockType: model.TypeImage},
		{token: "attachment", blockType: model.TypeAttachment},
		{token: "divider", blockType: model.TypeDivider},
	}
	for _, tt := range allBlockTypes {
		t.Run("block type "+tt.token, func(t *testing.T) {
			filter, unknown := parseEventFilter(tt.token)
			assert.Empty(t, unknown)
			assert.Equal(t, map[model.BlockType]struct{}{tt.blockType: {}}, filter.types)
		})
	}
}

func TestEventFilterMatches(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		action    notify.Action
		blockType model.BlockType
		want      bool
	}{
		{name: "zero filter add card", action: notify.Add, blockType: model.TypeCard, want: true},
		{name: "zero filter update text", action: notify.Update, blockType: model.TypeText, want: true},
		{name: "zero filter delete comment", action: notify.Delete, blockType: model.TypeComment, want: true},
		{name: "actions only match any type", raw: "add", action: notify.Add, blockType: model.TypeText, want: true},
		{name: "actions only reject", raw: "add", action: notify.Update, blockType: model.TypeCard},
		{name: "types only match any action", raw: "card", action: notify.Delete, blockType: model.TypeCard, want: true},
		{name: "types only reject", raw: "card", action: notify.Add, blockType: model.TypeComment},
		{name: "both accept", raw: "add,card", action: notify.Add, blockType: model.TypeCard, want: true},
		{name: "both reject action", raw: "add,card", action: notify.Update, blockType: model.TypeCard},
		{name: "both reject type", raw: "add,card", action: notify.Add, blockType: model.TypeComment},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter, _ := parseEventFilter(tt.raw)
			assert.Equal(t, tt.want, filter.matches(tt.action, tt.blockType))
		})
	}
}
