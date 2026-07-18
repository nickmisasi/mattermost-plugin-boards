# Implementation Plan: Framework Vision M2 — Boards Front-End (plugin side)

> Production-grade `notifywebhook` notify backend: signed, async, bounded-retry
> delivery of board-change webhooks to configured external endpoints. Fork-first;
> written to upstream quality (framing: completion/hardening of the dormant 2020
> `webhook_update` path). Supersedes the PoC on `cursor/notify-webhook-poc-ba42`.

## Metadata
- **Spec:** `planning/projects/software-factory/ideas/001-framework-vision/spec.md` (Track 2 + gaps G3/G4 designs in `gaps-20260718.md`)
- **Target repo:** `nickmisasi/mattermost-plugin-boards` (upstream: `mattermost/mattermost-plugin-boards`)
- **Generated:** 2026-07-18
- **Status:** draft

## Architecture Overview

A new `notify.Backend` (`server/services/notify/notifywebhook/`) registered beside
`notifymentions`/`notifysubscriptions` in `server/boards/notifications.go`. It is
always registered, holds the shared `*config.Configuration` pointer, re-reads
URLs/secret/filter per event (backends are never rebuilt on config change), and no-ops
when no URLs are configured — zero behavior change unset. `BlockChanged` only builds
the envelope and enqueues; a dedicated delivery worker (subscriptions `notifier.go`
lifecycle precedent) does signed HTTP with bounded retries, because
`notify.Service.BlockChanged` calls backends synchronously inside the shared
CallbackQueue worker (`service.go:94-107`). Event identity uses `utils.NewID` with a
new `IDType` prefix (deliberate deviation from the gaps "ULID" wording — zero new
deps; `occurredAt` covers ordering). Legacy `webhook.NotifyUpdate` call sites are left
untouched (no-op in plugin mode; removal is upstream's call).

## Phases

### Phase 1: Envelope + backend skeleton (envelope-first — freezes the cross-repo contract)

**Goal:** compilable, registered, inert-when-unconfigured backend with the wire envelope frozen for Factory's ingress.
**Depends on:** none.

#### Tasks

- [ ] **1.1 Envelope**
  - **Files:** `server/services/notify/notifywebhook/envelope.go` (new)
  - **Action:** Create
  - **Details:** `WebhookEnvelope{ eventId, occurredAt, action, teamId, board, card,
    blockChanged, blockOld, modifiedBy }` per gaps G3. `eventId` via `utils.NewID` with
    a new `IDType` constant (`server/utils/utils.go:31-37`); `occurredAt` via
    `GetMillis()` server-side. `blockChanged`/`blockOld` are full `*model.Block`
    (carry ID + updateAt; `blockOld` is the old-column source — it exists on patch
    events, `app/blocks.go:438-469`, and the PoC ignored it). JSON field names are THE
    frozen cross-repo contract — document in the package doc comment; this file is the
    review artifact for the Factory coordination gate.

- [ ] **1.2 Backend skeleton**
  - **Files:** `server/services/notify/notifywebhook/webhook_backend.go` (new)
  - **Action:** Create
  - **Details:** Template `notifylogger/logger_backend.go` (New/Start/ShutDown/
    BlockChanged/Name) + params-struct pattern from `notifymentions` (:31-36). Holds
    `*config.Configuration`; re-reads URL list/secret/filter on every event; empty URL
    list → immediate return (no behavior change when unset). Name: `"notifyWebhook"`.

- [ ] **1.3 Registration**
  - **Files:** `server/boards/notifications.go`, `server/boards/boardsapp.go`
  - **Action:** Extend
  - **Details:** `createWebhookNotifyBackend(params)` beside the mentions (:30) and
    subscriptions (:48) builders; append in `boardsapp.go:99-121`.
    `initNotificationService` (`server/server/server.go:497-503`) needs no changes.

#### Definition of Done
- [ ] Plugin builds; backend registered and inert with empty config
- [ ] Envelope JSON shape reviewed and frozen (tagged commit for Factory consumption)

---

### Phase 2: Settings + configuration plumbing

**Goal:** operator-configurable via System Console; live changes without restart.
**Depends on:** Phase 1.

#### Tasks

- [ ] **2.1 plugin.json settings**
  - **Files:** `plugin.json`
  - **Action:** Extend
  - **Details:** Three settings (help_text carries the operator docs — signature header
    names, at-least-once semantics, replay window): `NotifyWebhookURLs` (longtext,
    newline-separated — no string-array setting type exists), `NotifyWebhookSecret`
    (text), `NotifyWebhookEventTypes` (longtext; empty = all). Run `make apply` to
    propagate manifests. Keys arrive lowercased (precedent `boardsapp.go:29`).

- [ ] **2.2 Config plumbing**
  - **Files:** `server/boards/configuration.go`, `server/boards/boardsapp_util.go`, `server/services/config/config.go`
  - **Action:** Extend
  - **Details:** New fields on the boards `configuration` struct (:21-23) and
    `config.Configuration` — do NOT reuse the zeroed legacy `WebhookUpdate`
    (`boardsapp_util.go:100`) or the dormant `Secret`; new clearly-named fields avoid
    entangling with the 2020 PoC path. Map in `createBoardsConfig` (initial) and
    `OnConfigurationChange` (:74-118, in-place mutation + `UpdateAppConfig` — existing
    flow). Backend parses/splits raw strings at read time.

#### Definition of Done
- [ ] Settings visible in System Console; toggling changes behavior on the next event without restart
- [ ] Unset config = verified no-op

---

### Phase 3: Signing + async bounded-retry delivery

**Goal:** production delivery semantics; `BlockChanged` never blocks the notify queue worker.
**Depends on:** Phase 1 (Phase 2 parallel-safe).

#### Tasks

- [ ] **3.1 HMAC signing**
  - **Files:** `server/services/notify/notifywebhook/signing.go` (new)
  - **Action:** Create
  - **Details:** HMAC-SHA256 over the raw JSON body; headers
    `X-Boards-Webhook-Signature: sha256=<hex>` and
    `X-Boards-Webhook-Timestamp: <millis>`, with the timestamp included in the signed
    material to bound replay. This tuple is the second half of the frozen contract —
    mirror into Factory's ingress test fixtures.

- [ ] **3.2 Async delivery worker**
  - **Files:** `server/services/notify/notifywebhook/delivery.go` (new), `webhook_backend.go`
  - **Action:** Create/Extend
  - **Details:** `BlockChanged`: build envelope → apply event-type filter → non-blocking
    enqueue to a bounded channel → return (drop + `mlog.Warn` on full). Delivery
    goroutine(s): shared `http.Client` with explicit timeout, `defer resp.Body.Close()`
    + drain (gosec + bodyclose linters enabled, `.golangci.yml:39,43`), bounded retries
    with backoff on 5xx/network errors, no retry on 4xx. Lifecycle: `Start` spawns,
    `ShutDown` closes + drains with deadline (precedent `subscriptions_backend.go:57-70`
    + `notifier.go:59-67`; server gives notify a 10s drain, `initialize.go:21-28`).

- [ ] **3.3 Semantics documentation**
  - **Files:** `server/services/notify/notifywebhook/doc.go` (new)
  - **Action:** Create
  - **Details:** Package doc: at-least-once/best-effort (in-memory queue; retries +
    crash = possible dup/loss; unordered), single-node emission (notify fires only on
    the mutating node — no cluster duplication, verified against
    `ws/plugin_adapter_cluster.go` being WS-only), consumers must dedup by `eventId`.
    This text seeds the upstream PR body.

#### Definition of Done
- [ ] `go test -race`: non-blocking enqueue, retry-then-success, 4xx-no-retry, shutdown drain delivers in-flight items within deadline

---

### Phase 4: Tests + upstream hygiene

**Goal:** upstream-quality PR.
**Depends on:** Phases 2, 3.

#### Tasks

- [ ] **4.1 Test suite**
  - **Files:** `server/services/notify/notifywebhook/webhook_backend_test.go`, `signing_test.go`, `delivery_test.go` (new)
  - **Action:** Create
  - **Details:** httptest receivers (precedent `services/webhook/webhook_test.go:18-42`):
    delivery with signature verification round-trip; event-type filter (table-driven,
    notifymentions style — no mockery); empty-config no-op; retry/backoff; shutdown
    drain; **golden envelope JSON test** guarding the frozen contract;
    `mlog.CreateConsoleTestLogger` for loop tests. `FOCALBOARD_UNIT_TESTING=1`.

- [ ] **4.2 Hygiene pass**
  - **Files:** all new files
  - **Action:** Modify
  - **Details:** License headers (`2020-present`, goheader-enforced); `make apply`
    artifacts committed; NO plugin.json version bump (injected from git tags); mlog
    levels (Debug per delivery, Error on final failure); leave the 7 legacy
    `NotifyUpdate` call sites untouched (`app/blocks.go:112,181,227,325,421`,
    `app/boards_and_blocks.go:61,149`).

- [ ] **4.3 Upstream PR description draft**
  - **Files:** `.planning/UPSTREAM_PR_NOTES.md` (new)
  - **Action:** Create
  - **Details:** Framing: completion/hardening of the dormant `webhook_update` path
    (adds signing, retries, config surface, envelope). Explicitly flag pre-existing
    quirks NOT masked: `DeleteBoardsAndBlocks` emits `Update` not `Delete`
    (`boards_and_blocks.go:187`); board-level CRUD emits no `BlockChangeEvent`
    (WS-only); duplicate-block path skips notify. Document at-least-once semantics and
    the consumer dedup contract.

- [ ] **4.4 CI gate**
  - **Files:** —
  - **Action:** Run
  - **Details:** `make server-ci` (golangci-lint v1.64.8 + `go test -race -cover`),
    `make check-style`, mattermost-govet license check.

#### Definition of Done
- [ ] `make server-ci` green; golden envelope test pins the contract
- [ ] PR description draft complete

## File Change Map

| File | Phase(s) | Action | Summary |
|------|----------|--------|---------|
| `server/services/notify/notifywebhook/envelope.go` | 1 | Create | Frozen wire envelope + eventId |
| `server/services/notify/notifywebhook/webhook_backend.go` | 1, 3 | Create/Modify | Backend impl |
| `server/services/notify/notifywebhook/signing.go` | 3 | Create | HMAC-SHA256 + timestamp |
| `server/services/notify/notifywebhook/delivery.go` | 3 | Create | Async worker, retries |
| `server/services/notify/notifywebhook/doc.go` | 3 | Create | Semantics documentation |
| `server/services/notify/notifywebhook/*_test.go` | 4 | Create | Full suite + golden envelope |
| `server/boards/notifications.go` | 1 | Modify | `createWebhookNotifyBackend` |
| `server/boards/boardsapp.go` | 1 | Modify | Backend registration |
| `plugin.json` (+ `make apply` artifacts) | 2 | Modify | Three settings |
| `server/boards/configuration.go` | 2 | Modify | Config struct + change handler |
| `server/boards/boardsapp_util.go` | 2 | Modify | `createBoardsConfig` mapping |
| `server/services/config/config.go` | 2 | Modify | New Configuration fields |
| `.planning/UPSTREAM_PR_NOTES.md` | 4 | Create | Upstream framing + quirk flags |

## Testing Strategy

Pure unit tests with httptest receivers (repo convention: table-driven, no mockery for
notify backends); `go test -race` mandatory (CI runs it); golden JSON test pins the
envelope so Factory fixture drift is caught here; `make server-ci` + `make check-style`
before every commit checkpoint. Live validation (deploy to the dev VM's running
Mattermost + real Factory ingress) happens at the orchestration plan's Step 5.

## Definition of Done (Overall)

- [ ] All 4 phases complete with per-phase DoD checked
- [ ] Zero behavior change with settings unset (explicit test)
- [ ] Envelope + signing tuple byte-identical to Factory's ingress fixtures
- [ ] `make server-ci`, `make check-style`, license govet all green
- [ ] Upstream PR notes drafted (quirks flagged, semantics documented)
