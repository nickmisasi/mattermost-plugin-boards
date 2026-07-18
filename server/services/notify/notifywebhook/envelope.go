// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package notifywebhook

import (
	"github.com/mattermost/mattermost-plugin-boards/server/model"
	"github.com/mattermost/mattermost-plugin-boards/server/services/notify"
)

// Envelope is the wire format delivered to configured webhook endpoints.
//
// The JSON field names below are a frozen contract consumed by external
// systems; never rename or remove a field. New fields may be added.
//
// Field presence:
//   - eventId, occurredAt, action, teamId, board, blockChanged, modifiedBy
//     are always present.
//   - card is omitted when the changed block does not belong to a card
//     (e.g. a view or a board-level block).
//   - blockOld is omitted for action "add"; for "update" it carries the
//     block state prior to the change; for "delete" it duplicates
//     blockChanged (the deleted state).
type Envelope struct {
	// EventID uniquely identifies this event ('e'-prefixed, 27 chars).
	// Consumers must deduplicate on it; delivery is at-least-once.
	EventID string `json:"eventId"`

	// OccurredAt is the server-side time the event was observed, in
	// milliseconds since epoch. Assigned by this backend, not the client.
	OccurredAt int64 `json:"occurredAt"`

	// Action is one of "add", "update", "delete" (notify.Action values).
	Action string `json:"action"`

	// TeamID is the ID of the team owning the board.
	TeamID string `json:"teamId"`

	// Board is the full board the change occurred on, including
	// cardProperties (the option-ID -> name mapping consumers need to
	// interpret card property values).
	Board *model.Board `json:"board"`

	// Card is the card ancestor of the changed block, when one exists.
	Card *model.Block `json:"card,omitempty"`

	// BlockChanged is the block that was added, updated, or deleted.
	// Its id and updateAt fields disambiguate edits from creations.
	BlockChanged *model.Block `json:"blockChanged"`

	// BlockOld is the prior state of the block, when available.
	BlockOld *model.Block `json:"blockOld,omitempty"`

	// ModifiedBy is the board membership of the user who made the change.
	ModifiedBy *model.BoardMember `json:"modifiedBy"`
}

// newEnvelope maps a notify.BlockChangeEvent to the wire envelope. It is a
// pure function: the caller supplies identity and timestamp so tests can
// pin golden output.
func newEnvelope(evt notify.BlockChangeEvent, eventID string, occurredAt int64) *Envelope {
	return &Envelope{
		EventID:      eventID,
		OccurredAt:   occurredAt,
		Action:       string(evt.Action),
		TeamID:       evt.TeamID,
		Board:        evt.Board,
		Card:         evt.Card,
		BlockChanged: evt.BlockChanged,
		BlockOld:     evt.BlockOld,
		ModifiedBy:   evt.ModifiedBy,
	}
}
