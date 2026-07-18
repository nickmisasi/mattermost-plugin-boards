# Phase 1 Plan: Envelope + notifywebhook Backend Skeleton + Registration

> Fully prescriptive implementation plan for Phase 1 of `.planning/PLAN.md`
> ("Envelope + backend skeleton — envelope-first"). Executable by a coding agent
> with no further design decisions. The envelope JSON emitted here is the
> **frozen cross-repo contract** consumed by the Software Factory ingress —
> field names in this document are final.

## Metadata

- **Parent plan:** `.planning/PLAN.md` Phase 1 (tasks 1.1–1.3)
- **Requirements:** `planning/projects/software-factory/ideas/001-framework-vision/gaps-20260718.md`
  G3 (event identity: eventId, server-side occurredAt, blockChanged with ID+updateAt,
  blockOld) and G4 (delivery-semantics context; delivery itself is Phase 3)
- **Branch:** `cursor/m2-webhook-plan-ba42`
- **Supersedes:** PoC on `cursor/notify-webhook-poc-ba42` (env-var URL, raw
  `BlockChangeEvent` payload, synchronous POST). Orientation only; every design
  point below overrides it.
- **Status:** ready for implementation

## Goal / Success State

After Phase 1:

- `server/services/notify/notifywebhook/` exists, compiles, and is registered as a
  fourth notify backend beside `notifymentions`, `notifysubscriptions`, and
  `notifylogger`.
- The backend is **inert**: no configuration surface exists yet (Phase 2), so
  `BlockChanged` always returns immediately without side effects. Zero behavior
  change for every existing install.
- The wire envelope (`Envelope` struct + golden JSON fixture) is frozen and
  review-ready — the tagged commit at the end of this phase is what the Factory
  team codes its ingress fixtures against.
- New `utils.IDType` constant exists for event IDs.
- All new files pass `make check-style` (golangci-lint + mattermost-govet license
  check) and targeted `go test -race`.

---

## Task 1: `utils.IDType` constant for webhook event IDs

**File:** `server/utils/utils.go` — Modify.

Existing prefixes (const block, `utils.go:18-29`): `'7'` none, `'t'` team, `'b'`
board, `'c'` card, `'v'` view, `'s'` session, `'u'` user, `'k'` token, `'a'` block,
`'i'` attachment.

**Prescription:** add one line at the end of the const block, after
`IDTypeAttachment IDType = 'i'` (line 28):

```go
	IDTypeWebhookEvent IDType = 'e'
```

Rationale for `'e'`: unused by any existing prefix; mnemonic for **e**vent (the ID
identifies a notification event, not the webhook mechanism); follows the existing
entity-based naming pattern (`IDTypeBoard`, `IDTypeCard`, …). IDs produced by
`utils.NewID(utils.IDTypeWebhookEvent)` (`utils.go:35-37`) are 27-char strings:
`'e'` + 26-char zbase32-encoded UUIDv4, e.g. `e7g8i9j3k5m6n7p8q9r3s5t6u7w`.

This is a deliberate deviation from the gaps-report "ULID" wording, ratified in the
parent plan's Architecture Overview: `utils.NewID` adds zero dependencies and
`occurredAt` covers ordering. Uniqueness (Factory dedup key) is all `eventId` must
provide.

---

## Task 2: Envelope — the frozen contract

**Files:**
- `server/services/notify/notifywebhook/doc.go` — Create (package doc stub)
- `server/services/notify/notifywebhook/envelope.go` — Create
- `server/services/notify/notifywebhook/testdata/envelope_golden.json` — Create (Task 5)

> Note: the parent File Change Map lists `doc.go` under Phase 3. Amendment: create
> the stub in Phase 1 (the parent's task 1.1 requires the contract documented "in
> the package doc comment"); Phase 3 extends it with delivery semantics rather than
> creating it.

### 2.1 Full structs vs slimmed shapes — DECISION: full `*model.Block` / `*model.Board`

Embed the model structs as-is. Justification:

1. **Fidelity.** G3's requirements fall out for free: `blockChanged` carries
   `id` and `updateAt` (`model/block.go:40,76`) — the fields Factory needs for
   dedup-adjacent disambiguation and G12 stale-event dropping. `blockOld` is the
   same shape, giving the old-column source for `fromStage`.
2. **The board's `cardProperties` is a feature, not bloat.** Factory's projector
   must translate select-option IDs in `card.fields.properties` to stage names;
   `board.cardProperties` (`model/board.go:98`) is exactly that mapping, delivered
   with every event — no extra API round trip. Size is bounded (a few KB for
   property-heavy boards) and acceptable.
3. **No drift.** Slimmed shadow types would need hand-maintained sync with
   `model.Block`/`model.Board` on every upstream field addition. The model JSON
   tags are already public, swagger-documented API shapes (`swagger:model`) —
   they are stable by upstream's own compatibility rules.
4. **Simplicity for upstream review.** The envelope file stays ~60 lines of
   declaration, no mapping code to review or test.

`modifiedBy` is the full `*model.BoardMember` (`model/board.go:178-214`) for the
same reasons; `userId` is the field Factory keys bot-write filtering on.

### 2.2 Struct prescription (exact code)

`envelope.go` in full:

```go
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
```

Notes locking the decisions in:

- **Type name is `Envelope`, not `WebhookEnvelope`** — the package qualifier
  already says webhook (`notifywebhook.Envelope`); `notifywebhook.WebhookEnvelope`
  stutters. This supersedes the parent plan's informal `WebhookEnvelope` spelling;
  the JSON keys, not the Go identifier, are the contract.
- **`newEnvelope` takes `eventID`/`occurredAt` as parameters** (pure function) so
  the golden test needs no clock/ID injection seam. Production call site (Task 3)
  passes `utils.NewID(utils.IDTypeWebhookEvent)` and `utils.GetMillis()`
  (`utils.go:35-42`; use `utils.GetMillis`, not the duplicate `model.GetMillis`
  the PoC used — `notifywebhook` already imports `utils` for `NewID`).
- **`omitempty` only on `card` and `blockOld`** — the genuinely optional fields.
  Evidence from the single event producer `app/blocks.go:438-469`
  (`notifyBlockChanged`): `board` is always non-nil (early return on lookup error,
  `:445-449`); `modifiedBy` is always non-nil (temporary guest member synthesized
  at `:451-458`); `blockChanged` is always the mutated block (and
  `notify.Service.BlockChanged` dereferences `evt.BlockChanged.ID` in its error
  path, `service.go:103`); `card` is nil for non-card blocks (`:476-479`).
- **`blockOld` per action**, from every producer call site: nil on `add`
  (`blocks.go:229,327,422`, `boards_and_blocks.go:62`); old block on `update`
  (`blocks.go:116,183`, `boards_and_blocks.go:150`); same pointer as
  `blockChanged` on `delete` (`blocks.go:369`) and on the `DeleteBoardsAndBlocks`
  quirk path that emits `update` (`boards_and_blocks.go:187`). Document exactly
  this in the struct comment (done above); do NOT "fix" the quirk (guardrails,
  Task 8).
- **`action` is a plain `string` in the envelope** (not `notify.Action`) so the
  wire contract does not depend on a Go type; values are the `notify.Action`
  constants `"add" | "update" | "delete"` (`service.go:17-21`).

### 2.3 Package doc stub — `doc.go` in full

```go
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
```

(Phase 3 replaces the second paragraph with the full at-least-once /
unordered / dedup-by-eventId semantics text — parent task 3.3.)

### 2.4 Documented example JSON (the fixture the Factory team codes against)

The example below is byte-representative of production output (an `update` event:
a card dragged to a new Stage column). Field order follows Go struct order for
`Envelope` and model struct order within `board`/`card`/`blockChanged`/
`blockOld`/`modifiedBy`.

```json
{
  "eventId": "e7g8i9j3k5m6n7p8q9r3s5t6u7w",
  "occurredAt": 1784500000000,
  "action": "update",
  "teamId": "tjq3g5n8e7y5um6kf9r3s5t6u7w",
  "board": {
    "id": "b8f6h3k5m7p9r3s5t7v9w3x5y7z",
    "teamId": "tjq3g5n8e7y5um6kf9r3s5t6u7w",
    "channelId": "",
    "createdBy": "uo5d8m3k5n7p9r3s5t7v9w3x5y7",
    "modifiedBy": "uo5d8m3k5n7p9r3s5t7v9w3x5y7",
    "type": "O",
    "minimumRole": "editor",
    "title": "Factory Runs",
    "description": "",
    "icon": "🏭",
    "showDescription": false,
    "isTemplate": false,
    "templateVersion": 0,
    "properties": {},
    "cardProperties": [
      {
        "id": "prop-stage-id-000000000000000",
        "name": "Stage",
        "type": "select",
        "options": [
          { "id": "opt-queued-000000000000000000", "value": "Queued", "color": "propColorGray" },
          { "id": "opt-inreview-0000000000000000", "value": "In Review", "color": "propColorYellow" }
        ]
      }
    ],
    "createAt": 1784400000000,
    "updateAt": 1784490000000,
    "deleteAt": 0
  },
  "card": {
    "id": "c4d6f8h3k5m7p9r3s5t7v9w3x5y",
    "parentId": "b8f6h3k5m7p9r3s5t7v9w3x5y7z",
    "createdBy": "uo5d8m3k5n7p9r3s5t7v9w3x5y7",
    "modifiedBy": "uo5d8m3k5n7p9r3s5t7v9w3x5y7",
    "schema": 1,
    "type": "card",
    "title": "Fix login crash (run #42)",
    "fields": {
      "contentOrder": [],
      "icon": "🤖",
      "isTemplate": false,
      "properties": {
        "prop-stage-id-000000000000000": "opt-inreview-0000000000000000"
      }
    },
    "createAt": 1784410000000,
    "updateAt": 1784500000000,
    "deleteAt": 0,
    "boardId": "b8f6h3k5m7p9r3s5t7v9w3x5y7z"
  },
  "blockChanged": {
    "id": "c4d6f8h3k5m7p9r3s5t7v9w3x5y",
    "parentId": "b8f6h3k5m7p9r3s5t7v9w3x5y7z",
    "createdBy": "uo5d8m3k5n7p9r3s5t7v9w3x5y7",
    "modifiedBy": "uo5d8m3k5n7p9r3s5t7v9w3x5y7",
    "schema": 1,
    "type": "card",
    "title": "Fix login crash (run #42)",
    "fields": {
      "contentOrder": [],
      "icon": "🤖",
      "isTemplate": false,
      "properties": {
        "prop-stage-id-000000000000000": "opt-inreview-0000000000000000"
      }
    },
    "createAt": 1784410000000,
    "updateAt": 1784500000000,
    "deleteAt": 0,
    "boardId": "b8f6h3k5m7p9r3s5t7v9w3x5y7z"
  },
  "blockOld": {
    "id": "c4d6f8h3k5m7p9r3s5t7v9w3x5y",
    "parentId": "b8f6h3k5m7p9r3s5t7v9w3x5y7z",
    "createdBy": "uo5d8m3k5n7p9r3s5t7v9w3x5y7",
    "modifiedBy": "uo5d8m3k5n7p9r3s5t7v9w3x5y7",
    "schema": 1,
    "type": "card",
    "title": "Fix login crash (run #42)",
    "fields": {
      "contentOrder": [],
      "icon": "🤖",
      "isTemplate": false,
      "properties": {
        "prop-stage-id-000000000000000": "opt-queued-000000000000000000"
      }
    },
    "createAt": 1784410000000,
    "updateAt": 1784490000000,
    "deleteAt": 0,
    "boardId": "b8f6h3k5m7p9r3s5t7v9w3x5y7z"
  },
  "modifiedBy": {
    "boardId": "b8f6h3k5m7p9r3s5t7v9w3x5y7z",
    "userId": "uo5d8m3k5n7p9r3s5t7v9w3x5y7",
    "roles": "",
    "minimumRole": "",
    "schemeAdmin": true,
    "schemeEditor": false,
    "schemeCommenter": false,
    "schemeViewer": false,
    "synthetic": false
  }
}
```

Two envelope-level caveats to note in the contract review (they are properties of
the changed block, not of card moves specifically): when the changed block is a
card, `card` and `blockChanged` are the same object; when it is a child block
(comment, text), `blockChanged` is the child and `card` is its ancestor. On
`add` events `blockOld` is absent entirely (not `null`).

---

## Task 3: Backend skeleton

**File:** `server/services/notify/notifywebhook/webhook_backend.go` — Create.

Template: `notifylogger/logger_backend.go` (method set) + the `BackendParams`
struct pattern from `notifymentions/mentions_backend.go:31-56` and
`notifysubscriptions/subscriptions_backend.go:24-55`.

### What Phase 1 ships vs defers

| Concern | Phase 1 | Deferred to |
|---|---|---|
| `Backend` implementing `notify.Backend` (`service.go:34-39`) | ships | — |
| Holds shared `*config.Configuration` pointer | ships (field + nil-guarded read) | — |
| URL/secret/filter config fields | **do not exist yet** — `configuredURLs()` always returns nil | Phase 2 |
| Envelope construction | ships (`newEnvelope`, exercised by tests; unreachable in production because no URLs can be configured) | — |
| HTTP delivery, signing, queue, retries | nothing — not even an `http.Client` field | Phase 3 |
| `Start`/`ShutDown` | no-op stubs (logger flush on shutdown, notifylogger precedent `logger_backend.go:28-35`) | Phase 3 fills lifecycle |

### `webhook_backend.go` in full

```go
// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package notifywebhook

import (
	"github.com/mattermost/mattermost-plugin-boards/server/services/config"
	"github.com/mattermost/mattermost-plugin-boards/server/services/notify"
	"github.com/mattermost/mattermost-plugin-boards/server/utils"

	"github.com/mattermost/mattermost/server/public/shared/mlog"
)

const (
	backendName = "notifyWebhook"
)

// BackendParams are the required inputs for creating a webhook backend.
type BackendParams struct {
	Config *config.Configuration
	Logger mlog.LoggerIFace
}

// Backend delivers block change events to configured webhook endpoints.
type Backend struct {
	cfg    *config.Configuration
	logger mlog.LoggerIFace
}

// New creates a webhook notification backend.
func New(params BackendParams) *Backend {
	return &Backend{
		cfg:    params.Config,
		logger: params.Logger,
	}
}

func (b *Backend) Start() error {
	return nil
}

func (b *Backend) ShutDown() error {
	_ = b.logger.Flush()
	return nil
}

func (b *Backend) Name() string {
	return backendName
}

// BlockChanged builds the wire envelope for the event and hands it to
// delivery. Delivery is added in a later phase; with no URLs configurable
// yet this is a no-op, so the backend is inert on every install.
func (b *Backend) BlockChanged(evt notify.BlockChangeEvent) error {
	urls := b.configuredURLs()
	if len(urls) == 0 {
		return nil
	}

	env := newEnvelope(evt, utils.NewID(utils.IDTypeWebhookEvent), utils.GetMillis())

	b.logger.Debug("notifyWebhook event built; delivery not yet implemented",
		mlog.String("event_id", env.EventID),
		mlog.String("action", env.Action),
		mlog.String("block_id", env.BlockChanged.ID),
	)
	return nil
}

// configuredURLs returns the outbound endpoint URLs from the shared plugin
// configuration. The pointer is re-read on every event because the server
// mutates the shared Configuration in place on config change
// (boards/configuration.go OnConfigurationChange -> server.UpdateAppConfig);
// backends are never rebuilt. The URL setting is introduced in a later
// phase; until then this always returns nil.
func (b *Backend) configuredURLs() []string {
	if b.cfg == nil {
		return nil
	}
	return nil
}
```

Prescriptive notes:

- **`Name` constant is `"notifyWebhook"`** (lowerCamel with backend prefix,
  matching `"notifyLogger"`/`"notifyMentions"`/`"notifySubscriptions"`), declared
  as unexported `backendName` in a `const` block — exact repo convention
  (`logger_backend.go:12-14`).
- **Hold the `*config.Configuration` pointer, never copy the struct.** This is the
  same pointer `boardsapp.go:70` creates and `server.Config()` mutates in place on
  `OnConfigurationChange` (`configuration.go:90-114` + `UpdateAppConfig`,
  `server.go:355-357`), which is how Phase 2 gets live config without backend
  rebuilds. Re-read via `configuredURLs()` per event — never cache its result on
  the struct.
- **Check URLs before building the envelope** — `BlockChanged` runs on the shared
  notify CallbackQueue worker for every block mutation on the server
  (`service.go:94-108`); unconfigured installs must pay near-zero cost.
- **Lint risk, sanctioned mitigation:** `unparam` (enabled,
  `server/.golangci.yml:36`) may flag `configuredURLs` for always returning nil.
  If it fires, annotate the function line with
  `//nolint:unparam // config fields for URLs land in a follow-up; the accessor is the seam` —
  `nolintlint` (`:59`) requires exactly this machine-checkable
  `//nolint:<linter> // <reason>` shape. Do not restructure the code to dodge the
  linter; the accessor IS the Phase 2 seam.
- **Do not return an error from `BlockChanged` for "unconfigured"** — a returned
  error is logged at Error level per event by the service (`service.go:99-106`).

---

## Task 4: Registration

### 4.1 `server/boards/notifications.go` — Modify

**Imports** (block at `notifications.go:6-19`): add
`"github.com/mattermost/mattermost-plugin-boards/server/services/notify/notifywebhook"`
immediately after the `notifysubscriptions` import (line 13). Keep the group
gofmt/goimports-sorted.

**Builder:** insert after `createSubscriptionsNotifyBackend` (ends line 66) and
before `createDelivery` (starts line 68):

```go
func createWebhookNotifyBackend(params notifyBackendParams) *notifywebhook.Backend {
	backendParams := notifywebhook.BackendParams{
		Config: params.cfg,
		Logger: params.logger,
	}

	return notifywebhook.New(backendParams)
}
```

Deliberate deviation from the `(backend, error)` shape of the two existing
builders (`notifications.go:30,48`): those return errors only because
`createDelivery` → `EnsureBot` can fail (`:68-77`); this builder is infallible,
and an always-nil error return is exactly what the enabled `unparam` linter
flags. Consumes `params.cfg` and `params.logger` from the existing
`notifyBackendParams` struct (`:21-28`) — no new params fields.

### 4.2 `server/boards/boardsapp.go` — Modify

**Imports** (block at `boardsapp.go:11-24`): add
`"github.com/mattermost/mattermost-plugin-boards/server/services/notify/notifywebhook"`
after the `services/notify` import (line 14).

**Registration:** the backend-assembly block is `boardsapp.go:108-121`
(`var notifyBackends []notify.Backend` … `mentionsBackend.AddListener(subscriptionsBackend)`).
Insert after line 121, before `params := server.Params{` (line 123):

```go
	notifyBackends = append(notifyBackends, createWebhookNotifyBackend(backendParams))
```

That is the entire registration. `backendParams` (built at `:99-106`) already
carries `cfg` and `logger`. `server.Params.NotifyBackends` (`:130`) flows into
`initNotificationService` (`server/server/server.go:497-503`), which appends the
logger backend and constructs the `notify.Service` — **no changes to
`server/server/server.go`**. `notify.Service.AddBackend` calls `Start()` on
registration (`service.go:67-75`), so the no-op `Start` must return nil (it does).

Ordering note: append after the mentions/subscriptions pair; do not wire any
listener relationship (that mechanism is mentions→subscriptions-specific,
`boardsapp.go:121`).

---

## Task 5: Tests

Repo conventions honored: table-driven plain `testing` + `testify/assert`, no
mockery for notify backends (`notifymentions/mentions_test.go` builds model
structs directly and never boots a server); `mlog.CreateConsoleTestLogger(t)` for
loggers (`boardsapp_test.go:107`); `FOCALBOARD_UNIT_TESTING=1` in the environment
(read by `utils.IsRunningUnitTests`, `utils/debug.go:12-23`).

### 5.1 `server/services/notify/notifywebhook/envelope_test.go` — Create (golden contract test)

The load-bearing test of this phase: it pins the frozen JSON so any accidental
field rename/removal fails CI here before it breaks the Factory ingress.

Prescription:

- Build a fully-populated `notify.BlockChangeEvent` fixture with **fixed,
  deterministic values** mirroring the example JSON in Task 2.4: full
  `model.Board` (including one select `cardProperties` entry with two options),
  card block, `blockChanged` = card, `blockOld` = prior card state,
  `model.BoardMember`. No `mm_model.NewId()` calls — literal IDs only.
- Call `env := newEnvelope(evt, "e7g8i9j3k5m6n7p8q9r3s5t6u7w", int64(1784500000000))`.
- Marshal with `json.MarshalIndent(env, "", "  ")` and compare byte-for-byte
  against `testdata/envelope_golden.json` (create `testdata/`; content = exactly
  the Task 2.4 example). `encoding/json` sorts map keys, so `fields` and
  `properties` maps marshal deterministically.
- Support `-update` regeneration via the standard flag pattern:
  `var update = flag.Bool("update", false, "update golden files")`; when set,
  write the marshaled bytes to the golden file and skip comparison. Regenerating
  the golden file is a **contract change** — the test file must carry a comment
  saying exactly that ("this file freezes the cross-repo webhook contract;
  changes require coordination with consumers").
- Add two focused subtests for the presence rules:
  - `action=add`: `BlockOld: nil` → assert marshaled JSON does **not** contain a
    `"blockOld"` key.
  - nil `Card` → assert no `"card"` key.

### 5.2 `server/services/notify/notifywebhook/webhook_backend_test.go` — Create

- `TestBackend_NoopWhenUnconfigured`: `New(BackendParams{Config: &config.Configuration{}, Logger: mlog.CreateConsoleTestLogger(t)})`;
  call `BlockChanged` with a minimal populated event (non-nil `BlockChanged`
  block); assert nil error. Repeat with `Config: nil` (defensive path) — also nil
  error, no panic. This is the "zero behavior change with settings unset" proof
  for the phase.
- `TestBackend_Interface`: compile-time assertion
  `var _ notify.Backend = (*Backend)(nil)` (file-scope), plus runtime asserts
  `Name() == "notifyWebhook"`, `Start()` nil, `ShutDown()` nil.

### 5.3 `server/boards/notifications_test.go` — Create (registration smoke)

Feasible without server bootstrap — verified: unlike
`createMentionsNotifyBackend`/`createSubscriptionsNotifyBackend`, whose
`createDelivery` calls `servicesAPI.EnsureBot` (`notifications.go:68-77`),
`createWebhookNotifyBackend` touches only `params.cfg`/`params.logger`, so the
test needs neither `SetupTestHelper` nor mocks:

```go
func TestCreateWebhookNotifyBackend(t *testing.T) {
	params := notifyBackendParams{
		cfg:    &config.Configuration{},
		logger: mlog.CreateConsoleTestLogger(t),
	}

	backend := createWebhookNotifyBackend(params)

	assert.NotNil(t, backend)
	assert.Equal(t, "notifyWebhook", backend.Name())
}
```

Plus `TestWebhookBackendRegistersWithNotifyService` (may live in the same file or
in 5.2's file using `notify.New` — prefer here, closer to the wiring):
`service, err := notify.New(mlog.CreateConsoleTestLogger(t), createWebhookNotifyBackend(params))`;
assert nil error (proves `Start()` succeeds through the real
`AddBackend` path, `service.go:49-75`); then
`service.BlockChanged(evt)` with a minimal event and assert it does not panic —
the closest cheap analogue to "registered + inert" without booting `server.New`.

Do **not** attempt to smoke-test `NewBoardsApp` itself — it requires a live
`ServicesAPI` with `GetMasterDB` and `EnsureBot`; that is integration-test
territory and out of scope for this phase.

---

## Task 6: License headers, doc comments, lint constraints

- **License header** — first two lines of every new `.go` file (including tests),
  byte-exact:

  ```go
  // Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
  // See LICENSE.txt for license information.
  ```

  Enforced twice: `goheader` in `server/.golangci.yml:52` and
  `mattermost-govet -license -license.year=2020` in the `check-style` Makefile
  target (Makefile `:96-97`).
- **`godot`** (`.golangci.yml:50`): every declaration comment ends with a period
  — all prescribed comments above already comply; keep it that way for any
  additions.
- **`lll`**: 150-char lines (`.golangci.yml:18-19`).
- **`err113`** (`:51`): no dynamic `errors.New`/`fmt.Errorf` without `%w` at call
  sites. Phase 1 code has no error-producing paths, so nothing to do — but if any
  are added, wrap sentinel errors.
- **`exhaustive`** (`:41`): do not `switch` on `notify.Action` in Phase 1 code
  (none prescribed); if a later edit adds one, cover all three constants.
- **`gosec` / `bodyclose`** (`:43`, `:38`): not exercised in Phase 1 — there is
  deliberately no `http.Client` and no request code in this phase. They become
  binding in Phase 3 (explicit client timeout, `defer resp.Body.Close()` + drain).
  Do not pre-add HTTP scaffolding "for later".
- **`nolintlint`** (`:59`): the only sanctioned suppression is the `unparam` one
  specified in Task 3, in exactly the `//nolint:unparam // <reason>` form.
- **`goimports` grouping**: repo files group boards-local imports first, then the
  `mattermost/server/public` imports after a blank line (see
  `logger_backend.go:6-10`); the prescribed files follow it.
- **Package doc**: lives in `doc.go` only (Task 2.3); no duplicate `// Package`
  comment on other files.

---

## Task 7: Definition of Done + commands

All from the repo root unless noted. `golangci-lint` v1.64.x must be on PATH for
style checks (`Makefile:88-97`).

```bash
# 1. Build — the whole server tree, not just the new package
cd server && go build ./...

# 2. Targeted unit tests, race detector on (CI runs -race; match it)
cd server && FOCALBOARD_UNIT_TESTING=1 go test -race ./services/notify/notifywebhook/...
cd server && FOCALBOARD_UNIT_TESTING=1 go test -race ./boards/ -run 'TestCreateWebhookNotifyBackend|TestWebhookBackendRegistersWithNotifyService'

# 3. Style — full gate (webapp lint requires webapp/node_modules; if the webapp
#    toolchain is unavailable in the environment, the server-only equivalent is:
#      cd server && golangci-lint run ./...
#      go vet -vettool=$(go env GOPATH)/bin/mattermost-govet -license -license.year=2020 ./server/...
#    after `go install github.com/mattermost/mattermost-govet/v2@3f08281c344327ac09364f196b15f9a81c7eff08`)
make check-style
```

**DoD checklist:**

- [ ] `go build ./...` clean from `server/`
- [ ] `IDTypeWebhookEvent IDType = 'e'` added to `server/utils/utils.go`
- [ ] `notifywebhook` package exists with `doc.go`, `envelope.go`,
      `webhook_backend.go` exactly as prescribed
- [ ] Golden test passes and `testdata/envelope_golden.json` matches the Task 2.4
      example structure (all nine top-level keys, `blockOld` omitted on add)
- [ ] No-op-when-unconfigured test and registration smoke tests pass under `-race`
- [ ] `createWebhookNotifyBackend` in `notifications.go`; single `append` in
      `boardsapp.go` after the subscriptions backend
- [ ] `make check-style` (or the documented server-only equivalent) green — license
      headers, godot periods, goimports grouping all clean
- [ ] No diff outside the files listed in the File Change Map below
- [ ] Commit checkpoint created (local, not pushed): suggested message
      `Add notifywebhook backend skeleton with frozen envelope contract (Phase 1)`.
      This commit is the Factory-coordination review artifact — call out
      `envelope.go` + `testdata/envelope_golden.json` in the commit body.

## File Change Map (Phase 1 complete set)

| File | Action | Content |
|---|---|---|
| `server/utils/utils.go` | Modify | `IDTypeWebhookEvent IDType = 'e'` after line 28 |
| `server/services/notify/notifywebhook/doc.go` | Create | Package doc stub (Task 2.3) |
| `server/services/notify/notifywebhook/envelope.go` | Create | `Envelope` + `newEnvelope` (Task 2.2) |
| `server/services/notify/notifywebhook/webhook_backend.go` | Create | Backend skeleton (Task 3) |
| `server/services/notify/notifywebhook/envelope_test.go` | Create | Golden contract test + presence subtests |
| `server/services/notify/notifywebhook/testdata/envelope_golden.json` | Create | Frozen fixture (Task 2.4 shape) |
| `server/services/notify/notifywebhook/webhook_backend_test.go` | Create | No-op + interface tests |
| `server/boards/notifications.go` | Modify | Import + `createWebhookNotifyBackend` after line 66 |
| `server/boards/notifications_test.go` | Create | Builder + registration smoke tests |
| `server/boards/boardsapp.go` | Modify | Import + `append` after line 121 |

## Task 8: Out-of-scope guardrails (hard NOs for this phase)

- **No settings plumbing.** Do not touch `plugin.json`,
  `server/boards/configuration.go`, `server/boards/boardsapp_util.go`, or
  `server/services/config/config.go`. No new `Configuration` fields, no
  `make apply`. That is Phase 2 in its entirety.
- **No signing, no HTTP, no queue, no goroutines.** No `net/http` import in the
  package, no `crypto/*`, no channels, no `Start`/`ShutDown` lifecycle beyond the
  prescribed stubs. Phase 3.
- **Do NOT touch the legacy `services/webhook` client** (`server/services/webhook/`)
  or any of its 7 call sites (`app/blocks.go:112,181,227,325,421`,
  `app/boards_and_blocks.go:61,149`), nor the dormant `WebhookUpdate`/`Secret`
  config fields (`config.go:50-51`, zeroed at `boardsapp_util.go:100`). They stay
  byte-identical.
- **Do not "fix" pre-existing event quirks** (e.g. `DeleteBoardsAndBlocks`
  emitting `update` with `blockOld == blockChanged`,
  `boards_and_blocks.go:187`) — they are documented contract behavior, flagged
  for upstream in Phase 4's PR notes.
- **Do not modify `server/server/server.go`**, `server/services/notify/service.go`,
  or any existing notify backend.
- **No new dependencies** — no ULID library, no go.mod changes.
- **Do not delete or modify the PoC branch**; it is orientation history.
- **No plugin.json version bump** (version is injected from git tags).

## Implementation Summary

Implemented the frozen `Envelope` contract, golden JSON fixture with `-update`
support and presence tests, the inert `notifywebhook` backend with the
`configuredURLs()` nil seam, the `IDTypeWebhookEvent` ID prefix, and Boards
registration through `createWebhookNotifyBackend`.

Added eight prescribed test cases covering golden serialization, optional field
presence, empty and nil configuration, the backend interface/lifecycle, builder
creation, and registration through the real notify service. The required server
build and race-enabled notify/Boards suites pass. `golangci-lint` was not
installed, so the prescribed `go vet ./services/notify/notifywebhook/...`
fallback was run and passed.

Deviation: the plan asks `server/boards/boardsapp.go` to import
`notifywebhook`, but that file only calls the package-local builder and therefore
has no direct package reference. Adding the import would make the server fail to
compile with an unused import. The package is imported where it is used in
`server/boards/notifications.go`; the prescribed single backend append remains
in `boardsapp.go`.

Remediation after review: added the plan-sanctioned inline `unparam` suppression
to `configuredURLs()`, changed optional-field tests to inspect unmarshaled
`json.RawMessage` maps, and documented that the delete-event duplication rule
rests on current producer behavior pending a Phase 4 producer-level test. The
combined build, race-test, and vet command passes, and the repo-pinned
`golangci-lint` v1.64.8 command passes for `notifywebhook` and `boards`.
