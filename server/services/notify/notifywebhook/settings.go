// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package notifywebhook

import (
	"net"
	"net/url"
	"strings"

	"github.com/mattermost/mattermost-plugin-boards/server/model"
	"github.com/mattermost/mattermost-plugin-boards/server/services/notify"
)

// parseWebhookURLs splits the raw newline-separated setting into validated
// endpoint URLs. Lines are trimmed; empty lines are skipped; lines failing
// isValidWebhookURL are returned in invalid for the caller to log.
func parseWebhookURLs(raw string) (valid []string, invalid []string) {
	for _, line := range strings.Split(raw, "\n") {
		entry := strings.TrimSpace(line)
		if entry == "" {
			continue
		}
		if isValidWebhookURL(entry) {
			valid = append(valid, entry)
		} else {
			invalid = append(invalid, entry)
		}
	}
	return valid, invalid
}

// isValidWebhookURL accepts https URLs with a host, and http URLs only for
// localhost/loopback hosts (development). Everything else is rejected.
func isValidWebhookURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		return isLoopbackHost(u.Hostname())
	default:
		return false
	}
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// eventFilter is the parsed form of the event-types setting. An empty set
// means "no restriction" on that dimension, so the zero value delivers
// everything.
type eventFilter struct {
	actions map[notify.Action]struct{}
	types   map[model.BlockType]struct{}
}

// matches reports whether an event with the given action and changed-block
// type passes the filter.
func (f eventFilter) matches(action notify.Action, blockType model.BlockType) bool {
	if len(f.actions) > 0 {
		if _, ok := f.actions[action]; !ok {
			return false
		}
	}
	if len(f.types) > 0 {
		if _, ok := f.types[blockType]; !ok {
			return false
		}
	}
	return true
}

// parseEventFilter parses the raw comma-separated setting. Tokens are
// case-insensitive and trimmed; each is either a notify action or a block
// type. Unrecognized tokens are returned in unknown for the caller to log
// and are otherwise ignored.
func parseEventFilter(raw string) (filter eventFilter, unknown []string) {
	for _, part := range strings.Split(raw, ",") {
		token := strings.ToLower(strings.TrimSpace(part))
		if token == "" {
			continue
		}
		switch notify.Action(token) {
		case notify.Add, notify.Update, notify.Delete:
			if filter.actions == nil {
				filter.actions = make(map[notify.Action]struct{})
			}
			filter.actions[notify.Action(token)] = struct{}{}
			continue
		}
		if blockType, err := model.BlockTypeFromString(token); err == nil {
			if filter.types == nil {
				filter.types = make(map[model.BlockType]struct{})
			}
			filter.types[blockType] = struct{}{}
			continue
		}
		unknown = append(unknown, token)
	}
	return filter, unknown
}
