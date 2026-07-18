# Complete and harden board-change webhooks

## Summary

This change completes and hardens the dormant 2020 `webhook_update` path for
external board-change consumers. It adds a stable event envelope with event
identity, HMAC-SHA256 request signing, asynchronous bounded-retry delivery, and
System Console settings for endpoints, signing, and event filtering.

The new `notifywebhook` backend supersedes rather than revives the raw
`server/services/webhook` client. The legacy client and its existing call sites
are intentionally left untouched for upstream to retire separately.

## Operator settings

- `NotifyWebhookURLs`: one endpoint per line. HTTPS is required except for HTTP
  loopback URLs used in local development. An empty value disables delivery.
- `NotifyWebhookSecret`: shared HMAC secret. An empty value sends unsigned
  requests and is supported but not recommended.
- `NotifyWebhookEventTypes`: optional comma-separated action and block-type
  filter. Actions are `add`, `update`, and `delete`; block types are `board`,
  `card`, `view`, `text`, `checkbox`, `comment`, `image`, `attachment`, and
  `divider`. Empty means all events. When both dimensions are present, both
  must match.

Settings are published as immutable snapshots and take effect without a plugin
restart.

## Wire and signing contract

Each delivery is an HTTP POST with `Content-Type: application/json`. The JSON
envelope contains:

- `eventId`: unique, 27-character, `e`-prefixed event identity.
- `occurredAt`: server observation time in Unix milliseconds.
- `action`, `teamId`, `board`, `blockChanged`, and `modifiedBy`.
- Optional `card` and `blockOld`; `blockOld` is absent for adds and carries the
  prior block for updates.

The committed golden JSON fixture freezes the envelope field names and
presence rules.

Every request includes:

- `X-Boards-Webhook-Timestamp: <timestampMillis>`
- `X-Boards-Webhook-Signature: sha256=<lowercase hex>` when a secret is set

The signed material is byte-exact:

`ASCII-decimal(timestampMillis) + "." + rawRequestBody`

The signature is `HMAC-SHA256(secret, signedMaterial)`. Consumers must verify
against the raw received bytes with a constant-time comparison and should
reject timestamps outside a short replay window; five minutes is recommended.
Each retry receives a fresh timestamp and signature.

## Delivery semantics

Delivery is asynchronous, at-least-once, unordered, and backed only by an
in-memory bounded queue. Network failures and 5xx responses retry with bounded
backoff; 4xx and other non-2xx responses do not retry. A full queue, exhausted
retries, shutdown deadline, or process failure can lose an event, while retries
can duplicate one. Consumers must deduplicate by `eventId` and may use
`occurredAt` only as an ordering hint.

Events are emitted only by the node that performs the mutation. The cluster
adapter broadcasts WebSocket messages only, so this path does not duplicate a
single mutation across cluster nodes.

## Pre-existing producer behavior intentionally not masked

- `DeleteBoardsAndBlocks` emits `notify.Update`, not `notify.Delete`, at
  `server/app/boards_and_blocks.go:187`.
- Board-level create, patch, and delete operations emit WebSocket board events
  but no `BlockChangeEvent`; only block changes reach this backend.
- `DuplicateBlock` broadcasts WebSocket block changes but skips notify
  emission.
- The envelope documentation says delete events duplicate `blockChanged` into
  `blockOld`, but that clause rests on current producer behavior, as called out
  in `server/services/notify/notifywebhook/doc.go`; it is not independently
  enforced by the backend.

These quirks are called out rather than silently changed so the webhook work
does not alter existing Boards mutation behavior.

## Manifest generator limitation

The repository's `make apply` manifest generator does not support the
setting-level `secret` field and currently rejects `plugin.json` with
`json: unknown field "secret"`. `plugin.json`, `server/manifest.go`, and
`webapp/src/manifest.ts` therefore keep `NotifyWebhookSecret` marked secret by
hand. Upstream should extend the manifest tooling to support `secret`; until
then, these three files must be reviewed and maintained together.

## Test inventory

- Golden envelope JSON and optional-field presence rules.
- OpenSSL-derived signing vectors and signed-material shape.
- End-to-end delivery with an independent HMAC verifier.
- Unsigned delivery when no secret is configured.
- Table-driven URL parsing and event filtering, including all nine block types.
- Empty and nil configuration no-op behavior.
- Live settings updates and concurrent settings publication under `-race`.
- Retry then success with backoff and per-attempt signatures.
- No retry on 4xx responses.
- Transport timeout retry.
- Redirect rejection for signed requests.
- Non-blocking full-queue drop.
- Shutdown drain of queued and in-flight jobs.
- Shutdown deadline against a hung endpoint.
- Idempotent start, stop, and restart.
- Independent delivery to multiple configured URLs.

