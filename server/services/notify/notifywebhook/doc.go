// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Package notifywebhook implements a notification backend that delivers
// board change events to configured external HTTP endpoints as JSON
// envelopes (see Envelope). The envelope's JSON field names are a stable
// contract for external consumers.
//
// Endpoint URLs, signing secret, and event filter are read live from the
// plugin configuration (System Console); no restart is required.
//
// Delivery semantics are at-least-once and best-effort: events are
// queued in memory and delivered asynchronously by a small worker pool
// with bounded retries (network errors and 5xx retry with backoff; other
// non-2xx responses drop). A full queue, a failing endpoint that
// exhausts retries, or a server restart can lose events; retries can
// duplicate them; concurrent workers deliver out of order. Consumers
// must deduplicate by the envelope eventId and treat occurredAt as the
// ordering hint. Events fire only on the node that performed the
// mutation, so multi-node clusters do not duplicate deliveries.
//
// Requests are signed when a secret is configured: the
// X-Boards-Webhook-Signature header carries "sha256=" + lowercase hex
// HMAC-SHA256 over the decimal X-Boards-Webhook-Timestamp value, a
// literal period, and the raw request body. Each attempt is re-signed
// with a fresh timestamp; receivers should enforce a short replay
// window (five minutes recommended).
//
// The delete-event rule that blockOld duplicates blockChanged reflects
// current producer behavior in app/blocks.go:369. A producer-level contract
// test is tracked in the Phase 4 upstream notes.
package notifywebhook
