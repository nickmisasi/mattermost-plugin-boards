// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Package notifywebhook implements a notification backend that delivers
// board change events to configured external HTTP endpoints as JSON
// envelopes (see Envelope). The envelope's JSON field names are a stable
// contract for external consumers.
//
// Configuration (endpoint URLs, signing secret, event filter) and
// delivery (signed, asynchronous, bounded-retry HTTP) are added in later
// phases; until then the backend registers but performs no I/O.
//
// The delete-event rule that blockOld duplicates blockChanged reflects
// current producer behavior in app/blocks.go:369. A producer-level contract
// test is tracked in the Phase 4 upstream notes.
package notifywebhook
