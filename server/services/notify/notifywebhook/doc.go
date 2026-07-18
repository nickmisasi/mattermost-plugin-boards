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
package notifywebhook
