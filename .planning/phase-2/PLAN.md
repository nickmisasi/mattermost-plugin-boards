# Phase 2 Plan: Plugin Settings + Configuration Plumbing

> Fully prescriptive implementation plan for Phase 2 of `.planning/PLAN.md`
> ("Settings + configuration plumbing"). Executable by a coding agent with no
> further design decisions. Builds directly on the implemented Phase 1 code
> (commits `b7cc3dc4` + `ea708f91`): the `notifywebhook` backend skeleton with
> the `configuredURLs()` nil seam is replaced here by real, operator-configurable
> settings that take effect live, without a restart.

## Metadata

- **Parent plan:** `.planning/PLAN.md` Phase 2 (tasks 2.1–2.2)
- **Requirements:** `planning/projects/software-factory/ideas/001-framework-vision/gaps-20260718.md`
  G4 (at-least-once / in-memory / unordered semantics — the operator-facing
  `help_text` written in this phase is where those semantics are documented for
  admins) and G3 (signing headers — named in `help_text` here, implemented in
  Phase 3)
- **Branch:** `cursor/m2-webhook-plan-ba42`
- **Depends on:** Phase 1 (implemented + reviewed). Phase 3 is parallel-safe with
  this phase except for the `webhook_backend.go` edits in Task 5 — if executed
  concurrently, land this phase first.
- **Status:** ready for implementation

## Goal / Success State

After Phase 2:

- Three settings appear in System Console → Plugins → Mattermost Boards:
  webhook URLs, signing secret, event filter. Their `help_text` is the operator
  documentation (header names, at-least-once semantics, replay-window guidance).
- Setting values flow: `plugin.json` → Mattermost config →
  `createBoardsConfig` (initial load) and `OnConfigurationChange` (live change,
  in-place mutation of the shared `*config.Configuration`) → the backend, which
  re-reads them per event.
- `configuredURLs()`'s Phase 1 nil seam (and its `//nolint:unparam`) is gone,
  replaced by real newline-split, trimmed, validated URL parsing with
  memoized parse + warn-once-per-change logging.
- Event-type filter semantics are frozen (grammar below) and enforced in
  `BlockChanged`.
- Unset config remains a verified no-op. Delivery still does not exist —
  `BlockChanged` ends at the Phase 1 debug log. That is Phase 3.
- `make apply` artifacts (`server/manifest.go`, `webapp/src/manifest.ts`)
  regenerated and committed.

## Frozen decisions (made here, binding on implementation and on Phase 3)

1. **URL scheme policy: https-only, with a loopback exception for dev.**
   A URL is valid iff it parses (`net/url.Parse`), has a non-empty host, and
   its scheme is `https`, or `http` where the hostname is `localhost` or a
   loopback IP (`net.ParseIP(...).IsLoopback()` — covers `127.0.0.0/8` and
   `::1`). Everything else is skipped with a Warn. Rationale: the payload
   carries full board/card content and Phase 3 signs with a shared secret;
   plaintext transport off-box is not acceptable, but `http://localhost:...`
   is required for local Factory-ingress development.
2. **Event-filter grammar (frozen):** the setting is a comma-separated,
   case-insensitive list of tokens; whitespace around tokens is trimmed.
   A token is either an **action** — `add`, `update`, `delete`
   (the `notify.Action` values, `service.go:17-21`) — or a **block type** —
   `board`, `card`, `view`, `text`, `checkbox`, `comment`, `image`,
   `attachment`, `divider` (the `model.BlockType` values,
   `model/blocktype.go:16-27`, recognized via `model.BlockTypeFromString`).
   An event is delivered iff **(action set empty OR event action ∈ action
   set) AND (type set empty OR `blockChanged.type` ∈ type set)**. Empty or
   whitespace-only setting = deliver everything. Unknown tokens are ignored
   (Warn once per config change). Example: `add,delete,card,comment` delivers
   only add/delete events whose changed block is a card or a comment.
3. **Raw strings live in `config.Configuration`; the backend parses.** The
   three new fields carry the raw setting strings verbatim
   (newline-separated URLs, comma-separated filter). Parsing at read time in
   the backend is what makes live config change work with in-place mutation —
   the shared pointer is mutated by `OnConfigurationChange`; backends are
   never rebuilt (parent plan Architecture Overview).
4. **The boards-local `configuration` struct is NOT extended.** The struct at
   `server/boards/configuration.go:21-23` + its clone/lock machinery exists to
   serve plugin-hook reads via `getConfiguration()`; nothing reads webhook
   settings from it. Propagation follows the DataRetention/TeammateNameDisplay
   precedent instead — direct in-place writes to `b.server.Config()`
   (`configuration.go:92-114`), not the `setConfiguration` clone path.
5. **Do NOT reuse the legacy fields.** `WebhookUpdate` (`config.go:50`, zeroed
   at `boardsapp_util.go:100`) and `Secret` (`config.go:51`) belong to the
   dormant 2020 `services/webhook` path and stay byte-identical. New fields
   are `NotifyWebhookURLs`, `NotifyWebhookSecret`, `NotifyWebhookEventTypes`.

---

## Task 1: `plugin.json` settings

**File:** `plugin.json` — Modify.

The `settings_schema.settings` array currently holds one entry,
`EnablePublicSharedBoards` (`plugin.json:24-30`). Append three entries after
its closing `}` (line 30), inside the array. Setting types verified against
the pinned `mattermost/server/public` manifest model: `"text"` and
`"longtext"` are both supported (`model/manifest.go:61-63,435-439` in the
module); no string-array setting type exists, hence newline-separation for
URLs.

The `help_text` strings below are **verbatim contract** — they are the
operator docs for the feature (header names, at-least-once semantics, replay
window). Do not paraphrase. They intentionally describe the signing and
async-delivery behavior Phase 3 implements; both phases ship in the same
upstream PR (Phase 4), so the console never describes unimplemented behavior
to a real operator.

```json
{
    "key": "NotifyWebhookURLs",
    "type": "longtext",
    "display_name": "Board Change Webhook URLs:",
    "default": "",
    "help_text": "One webhook endpoint URL per line. Every board change event (add, update, or delete of a block) is POSTed to each URL as a JSON envelope. URLs must use https; http is allowed only for localhost and loopback addresses, for development. Delivery is asynchronous and at-least-once: consumers must deduplicate using the envelope eventId field, and events may arrive out of order or, in rare cases (server restart, sustained endpoint failure), not at all. Leave empty to disable."
},
{
    "key": "NotifyWebhookSecret",
    "type": "text",
    "display_name": "Board Change Webhook Secret:",
    "default": "",
    "help_text": "Shared secret used to sign webhook requests with HMAC-SHA256. Each request carries two headers: X-Boards-Webhook-Timestamp (milliseconds since epoch) and X-Boards-Webhook-Signature (sha256= followed by the lowercase hex HMAC of the timestamp, a period, and the raw request body). Receivers should verify the signature and reject requests whose timestamp falls outside a short replay window; 5 minutes is recommended, and retried deliveries are re-signed with a fresh timestamp. If empty, requests are sent unsigned (not recommended)."
},
{
    "key": "NotifyWebhookEventTypes",
    "type": "longtext",
    "display_name": "Board Change Webhook Event Filter:",
    "default": "",
    "help_text": "Optional comma-separated filter. Tokens may be actions (add, update, delete) and block types (board, card, view, text, checkbox, comment, image, attachment, divider). An event is delivered when its action matches any listed action (or no actions are listed) and its changed block type matches any listed type (or no types are listed). Unknown tokens are ignored. Example: add,delete,card,comment. Leave empty to deliver all events."
}
```

Notes:

- Keep the existing four-space JSON indentation of `plugin.json` (the snippet
  above is content; match file style when inserting).
- Do NOT add a `"version"` field to `plugin.json` — version is injected from
  git tags by the manifest generator (`build/manifest/main.go:130-147`).
- The signed-material description in `NotifyWebhookSecret`'s help_text
  (`timestamp` + `.` + `body`) matches the Phase 3 frozen formula exactly.
  If Phase 3's plan and this text ever disagree, that is a planning bug —
  stop and reconcile before shipping.

## Task 2: `make apply` artifact regeneration

**Files:** `server/manifest.go`, `webapp/src/manifest.ts` — Regenerated, not
hand-edited (both are marked "automatically generated").

Run from the repo root after Task 1:

```bash
make apply
```

This builds `build/bin/manifest` (via `build/setup.mk:13`) and runs
`./build/bin/manifest apply` (`Makefile:73-75`), which rewrites
`server/manifest.go` and `webapp/src/manifest.ts` from `plugin.json`
(`build/manifest/main.go:169-208`).

**Hazard — version drift:** the generator injects `version` and
`release_notes_url` from git tags at generation time
(`build/manifest/main.go:130-155`); the committed artifacts currently say
`9.2.2`. After running, inspect `git diff server/manifest.go
webapp/src/manifest.ts` and confirm the only changes are inside the
`settings_schema` block (three new setting objects, each gaining the
generator's default `"placeholder": ""` / `"hosting": ""` fields, same as the
existing entry at `server/manifest.go:42-50`). If `version`/
`release_notes_url` lines drift because local tags differ, manually restore
those specific lines to their committed values so the diff stays scoped.

Commit the two regenerated files together with `plugin.json`.

## Task 3: `config.Configuration` fields

**File:** `server/services/config/config.go` — Modify.

Insert after the `NotifyFreqBoardSeconds` field (line 74), as a new block
before the struct's closing brace:

```go
	NotifyWebhookURLs       string `json:"notify_webhook_urls" mapstructure:"notify_webhook_urls"`
	NotifyWebhookSecret     string `json:"notify_webhook_secret" mapstructure:"notify_webhook_secret"`
	NotifyWebhookEventTypes string `json:"notify_webhook_event_types" mapstructure:"notify_webhook_event_types"`
```

Notes:

- Tag style follows the adjacent `notify_freq_*` fields (`config.go:73-74`).
- Do NOT add `viper.SetDefault` entries in `ReadConfigFile` (`config.go:78-133`)
  — the zero value `""` is the correct default, and the viper path serves
  standalone (non-plugin) mode only, which this feature does not target.
- Do NOT touch `removeSecurityData` (`config.go:135-138`). It is a pre-existing
  no-op that does not clean the legacy `Secret` either; changing it is upstream
  hygiene to flag in Phase 4's PR notes, not silently fix here.

## Task 4: Boards-side plumbing

### 4.1 `server/boards/boardsapp.go` — setting-key constants

The Mattermost server lowercases plugin-setting keys in
`PluginSettings.Plugins[PluginName]` — precedent: `plugin.json` key
`EnablePublicSharedBoards` is read as `SharedBoardsName =
"enablepublicsharedboards"` (`boardsapp.go:29`). Add to the const block, after
`notifyFreqBoardSecondsKey` (line 32):

```go
	notifyWebhookURLsKey       = "notifywebhookurls"
	notifyWebhookSecretKey     = "notifywebhooksecret"
	notifyWebhookEventTypesKey = "notifywebhookeventtypes"
```

No other change in this file (backend registration shipped in Phase 1,
`boardsapp.go:123`).

### 4.2 `server/boards/boardsapp_util.go` — `createBoardsConfig` mapping + string getter

**Mapping:** in the `config.Configuration` literal (`boardsapp_util.go:85-116`),
insert after the `NotifyFreqBoardSeconds` line (line 110):

```go
		NotifyWebhookURLs:        getPluginSettingString(mmconfig, notifyWebhookURLsKey, ""),
		NotifyWebhookSecret:      getPluginSettingString(mmconfig, notifyWebhookSecretKey, ""),
		NotifyWebhookEventTypes:  getPluginSettingString(mmconfig, notifyWebhookEventTypesKey, ""),
```

(gofmt will align the struct literal; let it.)

**String getter:** only `getPluginSettingInt` exists
(`boardsapp_util.go:147-157`). Add its string sibling immediately after it:

```go
func getPluginSettingString(mmConfig mm_model.Config, key string, def string) string {
	val, ok := getPluginSetting(mmConfig, key)
	if !ok {
		return def
	}
	valString, ok := val.(string)
	if !ok {
		return def
	}
	return valString
}
```

**Lint requirement (verified against golangci-lint v1.64.8):** all three
call sites pass `""` as `def`, so `unparam` (`server/.golangci.yml:36`)
**does** flag the parameter as always-constant. Keep the parameter (it
mirrors `getPluginSettingInt`'s shape and documents the fallback at each
call site) and annotate the function line with exactly:

```go
func getPluginSettingString(mmConfig mm_model.Config, key string, def string) string { //nolint:unparam // def mirrors getPluginSettingInt; call sites document their fallback
```

— `nolintlint` (`:59`) requires this `//nolint:<linter> // <reason>` shape,
and this exact line passes the repo lint. Do not drop the parameter to
dodge the linter.

### 4.3 `server/boards/configuration.go` — `OnConfigurationChange` propagation

`OnConfigurationChange` (`configuration.go:74-119`) mutates the shared
`*config.Configuration` in place via `b.server.Config()` and then calls
`b.server.UpdateAppConfig()` (line 116; `server.go:355-357`). The backend holds
that same pointer (Phase 1, `webhook_backend.go:26,70-75`), so these three
writes are the entirety of live-change support. Insert after the
`EnablePublicSharedBoards` write (line 90), before the Data Retention block:

```go
	b.server.Config().NotifyWebhookURLs = getPluginSettingString(*mmconfig, notifyWebhookURLsKey, "")
	b.server.Config().NotifyWebhookSecret = getPluginSettingString(*mmconfig, notifyWebhookSecretKey, "")
	b.server.Config().NotifyWebhookEventTypes = getPluginSettingString(*mmconfig, notifyWebhookEventTypesKey, "")
```

Per frozen decision 4, do NOT add fields to the boards-local `configuration`
struct (`configuration.go:21-23`) and do NOT route through
`setConfiguration`. Unsynchronized in-place mutation read by notify-queue
workers is the codebase's existing pattern for every field in this function;
we deliberately match it rather than introduce a one-off locking scheme
(flag in Phase 4 PR notes alongside the other quirks).

## Task 5: Backend — real parsing, filter, seam removal

### 5.1 `server/services/notify/notifywebhook/settings.go` — Create

Pure parsing/validation helpers (no I/O, no logger — callers log). Full file:

```go
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
```

Prescriptive notes:

- `model.BlockTypeFromString` (`model/blocktype.go:34-56`) already lowercases
  and enumerates every block type — reuse it; do not hand-roll a type list
  that can drift.
- The action `switch` covers all three `notify.Action` constants explicitly;
  there is no `default` arm because fall-through to the block-type check is
  the intended behavior for non-action tokens. The `exhaustive` linter
  (`server/.golangci.yml:41`) checks switches over named types — the three
  cases above are the complete constant set, so it stays green; if a fourth
  action constant is ever added upstream, extend the case list.
- `godot` (`server/.golangci.yml:50`): declaration comments end with periods
  (the code above complies).

### 5.2 `server/services/notify/notifywebhook/webhook_backend.go` — Modify

Current state after Phase 1 review (cited lines are the committed file):
struct at `:25-28`, `New` at `:31-36`, `BlockChanged` at `:54-68`,
`configuredURLs` nil seam with `//nolint:unparam` at `:70-81`.

**(a) Struct + imports.** Add `"sync"` to the stdlib imports (new first import
group). Replace the struct (`:25-28`) with:

```go
// Backend delivers block change events to configured webhook endpoints.
type Backend struct {
	cfg    *config.Configuration
	logger mlog.LoggerIFace

	// settingsMux guards the memoized parse of the raw setting strings.
	// The raw strings themselves live on the shared cfg pointer, which the
	// server mutates in place on config change; re-parsing only when a raw
	// value changes keeps the per-event cost at two string compares and
	// logs validation warnings once per change instead of once per event.
	settingsMux sync.Mutex
	settings    parsedSettings
}

// parsedSettings caches the parsed form of the two parse-worthy settings,
// keyed by the raw strings they were parsed from. The zero value is correct
// for empty config: no URLs, filter that matches everything.
type parsedSettings struct {
	rawURLs   string
	rawFilter string
	urls      []string
	filter    eventFilter
}
```

`New` (`:31-36`) is unchanged.

**(b) Replace the seam.** Delete `configuredURLs` (`:70-81`) including its
`//nolint:unparam` suppression — the seam it marked is being filled, and
`nolintlint` (`server/.golangci.yml:59`) would flag the leftover directive.
In its place:

```go
// currentSettings returns the endpoint URLs and event filter parsed from
// the shared plugin configuration. The cfg pointer is re-read on every
// event because the server mutates the shared Configuration in place on
// config change (boards/configuration.go OnConfigurationChange ->
// server.UpdateAppConfig); backends are never rebuilt.
func (b *Backend) currentSettings() ([]string, eventFilter) {
	rawURLs := b.cfg.NotifyWebhookURLs
	rawFilter := b.cfg.NotifyWebhookEventTypes

	b.settingsMux.Lock()
	defer b.settingsMux.Unlock()

	if rawURLs != b.settings.rawURLs {
		urls, invalid := parseWebhookURLs(rawURLs)
		for _, entry := range invalid {
			b.logger.Warn("notifyWebhook ignoring invalid webhook URL",
				mlog.String("url", entry),
			)
		}
		b.settings.rawURLs = rawURLs
		b.settings.urls = urls
	}

	if rawFilter != b.settings.rawFilter {
		filter, unknown := parseEventFilter(rawFilter)
		for _, token := range unknown {
			b.logger.Warn("notifyWebhook ignoring unknown event filter token",
				mlog.String("token", token),
			)
		}
		b.settings.rawFilter = rawFilter
		b.settings.filter = filter
	}

	return b.settings.urls, b.settings.filter
}
```

**(c) Rewire `BlockChanged`** (`:54-68`) — the nil-cfg guard moves to the top
(it lived inside `configuredURLs`), the filter gate is added, and the Phase 1
debug-log ending stays (Phase 3 replaces it with enqueue):

```go
// BlockChanged builds the wire envelope for the event and hands it to
// delivery. Delivery is added in a later phase; until then the event is
// only logged at debug level after passing the URL and filter gates.
func (b *Backend) BlockChanged(evt notify.BlockChangeEvent) error {
	if b.cfg == nil {
		return nil
	}

	urls, filter := b.currentSettings()
	if len(urls) == 0 {
		return nil
	}
	if !filter.matches(evt.Action, evt.BlockChanged.Type) {
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
```

Notes:

- URL gate before filter gate: unconfigured installs (the overwhelmingly
  common case) exit after two string compares. `BlockChanged` runs on the
  shared notify CallbackQueue workers for every block mutation
  (`service.go:94-108`, queue built in `app/app.go:26-29`).
- `evt.BlockChanged` is dereferenced without a nil guard — same as
  `service.go:103` and the Phase 1 envelope evidence: every producer sets it.
- `Start`/`ShutDown`/`Name` (`:38-49`) are untouched (Phase 3 territory).

### 5.3 `doc.go` — one-line touch

Update the second paragraph (`doc.go:9-11`) to reflect that configuration now
exists:

```go
// Endpoint URLs, signing secret, and event filter are read live from the
// plugin configuration (System Console). Delivery (signed, asynchronous,
// bounded-retry HTTP) is added in a later phase; until then the backend
// performs no I/O.
```

The full semantics rewrite of `doc.go` is Phase 3 (its plan prescribes the
final text); do not write Phase 3's version here.

## Task 6: Tests

Conventions (Phase 1 precedent): plain `testing` + testify, table-driven, no
mockery for the notify package; `mlog.CreateConsoleTestLogger(t)`;
`FOCALBOARD_UNIT_TESTING=1` in the environment.

### 6.1 `server/services/notify/notifywebhook/settings_test.go` — Create

Table-driven, three test functions:

- `TestParseWebhookURLs` — cases (input → expected valid, expected invalid):
  - empty string → nil, nil
  - single https URL → kept
  - `https://a.example/hook\nhttps://b.example/hook` → both kept, order preserved
  - CRLF line endings (`"https://a.example/hook\r\n"`) → kept (TrimSpace eats `\r`)
  - leading/trailing spaces and blank/whitespace-only lines → trimmed/skipped
  - `http://localhost:8065/plugins/factory/hook` → kept (loopback exception)
  - `http://127.0.0.1:9000/hook` → kept
  - `http://[::1]:9000/hook` → kept
  - `http://example.com/hook` → invalid (http off-loopback)
  - `ftp://example.com/hook` → invalid (scheme)
  - `not a url` and `https://` (empty host) → invalid
  - duplicate URLs on two lines → both kept (each configured line gets a
    delivery; dedup is the operator's job — document via the test name)
- `TestParseEventFilter` — cases:
  - empty / whitespace-only → zero filter, no unknowns
  - `add` → actions={add}, types empty
  - `card,comment` → types={card,comment}, actions empty
  - `add,delete,card` → both dimensions populated
  - `Add, CARD` (mixed case + spaces) → parsed
  - `add,,card` (empty token) → skipped, no unknown
  - `add,bogus,card` → unknown=[`bogus`], filter still parsed
- `TestEventFilterMatches` — cases over `(filter, action, blockType) → bool`:
  - zero filter matches everything (all three actions × a couple of types)
  - actions-only filter: matching action passes regardless of type;
    non-matching action fails even for any type
  - types-only filter: symmetric
  - both dimensions: AND semantics — `add,card` filter rejects
    (update, card) and (add, comment), accepts (add, card)

### 6.2 `server/services/notify/notifywebhook/webhook_backend_test.go` — Extend

Keep the two Phase 1 tests (`:20-53`). Add:

- `TestBackend_LiveConfigChange` — the live-toggle proof at unit level.
  Create one `cfg := &config.Configuration{}` and a backend around it. Then,
  sequentially (single goroutine — the production write pattern is the
  codebase's pre-existing unsynchronized in-place mutation; do not write a
  concurrent mutate-while-reading test, it would trip `-race` on that
  pre-existing pattern, not on this phase's code):
  1. `urls, _ := backend.currentSettings()` → empty.
  2. Mutate `cfg.NotifyWebhookURLs = "https://a.example/hook"` in place →
     `currentSettings` now returns that URL (no backend rebuild).
  3. Append an invalid line (`"https://a.example/hook\nhttp://evil.example"`)
     → still one URL.
  4. Set `cfg.NotifyWebhookEventTypes = "add,card"` → returned filter
     accepts `(add, card)`, rejects `(update, text)`.
  5. Clear both fields → back to empty/zero.
- `TestBackend_BlockChangedRespectsFilter` — backend with
  `cfg.NotifyWebhookURLs` set (any valid https URL; nothing is contacted in
  this phase) and `cfg.NotifyWebhookEventTypes = "comment"`; call
  `BlockChanged` with a card-typed event and a comment-typed event; assert
  both return nil error (behavioral difference becomes observable in Phase 3;
  here this pins that the filter path is exercised without panic and that
  filtered `BlockChanged` never errors).
- Update `TestBackend_NoopWhenUnconfigured` if needed: it must still pass
  unchanged — empty configuration and nil configuration both return nil.
  (The nil-cfg guard moved into `BlockChanged`; the test already covers it.)

### 6.3 `server/boards/boardsapp_test.go` — Extend

Inside `TestSetConfiguration` (`:18-99`), add a subtest after
"test enable shared boards" (`:91-98`), following its map-setup pattern:

```go
	t.Run("test webhook notification settings", func(t *testing.T) {
		mmConfig := baseConfig
		mmConfig.PluginSettings.Plugins = make(map[string]map[string]interface{})
		mmConfig.PluginSettings.Plugins[PluginName] = make(map[string]interface{})
		mmConfig.PluginSettings.Plugins[PluginName][notifyWebhookURLsKey] = "https://factory.example/hook"
		mmConfig.PluginSettings.Plugins[PluginName][notifyWebhookSecretKey] = "hunter2"
		mmConfig.PluginSettings.Plugins[PluginName][notifyWebhookEventTypesKey] = "add,card"

		config := createBoardsConfig(*mmConfig, "", "")
		assert.Equal(t, "https://factory.example/hook", config.NotifyWebhookURLs)
		assert.Equal(t, "hunter2", config.NotifyWebhookSecret)
		assert.Equal(t, "add,card", config.NotifyWebhookEventTypes)
	})
```

Also assert the default path: in the same subtest (or a sibling), a config
whose plugin map lacks the keys yields `""` for all three fields — this pins
`getPluginSettingString`'s default behavior.

### 6.4 `server/boards/configuration_test.go` — Extend

In `TestOnConfigurationChange` (`:61-117`): add the three lowercased keys to
`basePlugins[PluginName]` (after `:66`) with distinct values, and after the
existing assertions (`:113-115`) assert
`b.server.Config().NotifyWebhookURLs` / `NotifyWebhookSecret` /
`NotifyWebhookEventTypes` equal those values. This proves the in-place
propagation end to end through the real `OnConfigurationChange`.

**Caveat:** this test boots `SetupTestHelper` → a real store
(`configuration_test.go:30-55`), so it needs a test database
(`TEST_DATABASE_DRIVERNAME`, default postgres). It runs in CI via
`make server-test`; if no local database is available, rely on CI for this
one and say so in the checkpoint commit message. Do not skip writing it.

## Task 7: Definition of Done + commands

All from the repo root unless noted.

```bash
# 1. Manifest regeneration (after editing plugin.json)
make apply
git diff plugin.json server/manifest.go webapp/src/manifest.ts   # settings_schema only

# 2. Build
cd server && go build ./...

# 3. Targeted unit tests, race detector on
cd server && FOCALBOARD_UNIT_TESTING=1 go test -race ./services/notify/notifywebhook/...
cd server && FOCALBOARD_UNIT_TESTING=1 go test -race ./boards/ -run 'TestSetConfiguration'
# DB-backed (optional locally, mandatory in CI):
cd server && FOCALBOARD_UNIT_TESTING=1 go test -race ./boards/ -run 'TestOnConfigurationChange'

# 4. Style — golangci-lint v1.64.8 (repo-pinned) + license govet
make check-style
# server-only fallback if the webapp toolchain is unavailable:
#   cd server && golangci-lint run ./...
```

**DoD checklist:**

- [ ] Three settings in `plugin.json` with the verbatim help_text; `make apply`
      artifacts regenerated with no version drift in the diff
- [ ] `config.Configuration` has the three new raw-string fields; legacy
      `WebhookUpdate`/`Secret` untouched
- [ ] `createBoardsConfig` maps all three via `getPluginSettingString`;
      `OnConfigurationChange` writes all three to `b.server.Config()` in place
- [ ] `configuredURLs()` seam and its `//nolint:unparam` are gone; replaced by
      `currentSettings()` + `settings.go` parsing with warn-once-per-change
- [ ] Filter grammar implemented exactly as frozen (comma list; action AND
      block-type dimensions; empty = all; unknown ignored)
- [ ] URL policy implemented exactly as frozen (https, or http on loopback only)
- [ ] All Task 6 tests written and passing under `-race` (DB-backed test may
      defer to CI); Phase 1 tests (golden envelope, no-op, interface) still green
- [ ] Unset config verified as no-op (existing `TestBackend_NoopWhenUnconfigured`)
- [ ] License header (`// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.`
      / `// See LICENSE.txt for license information.`) on every new `.go` file,
      including tests — enforced by `goheader` (`server/.golangci.yml:52`) and
      `mattermost-govet -license`
- [ ] `make check-style` (or documented server-only fallback) green
- [ ] No diff outside the File Change Map below
- [ ] Commit checkpoint created (local, not pushed): suggested message
      `Phase 2: webhook settings (URLs/secret/filter) + live config plumbing`

## File Change Map (Phase 2 complete set)

| File | Action | Content |
|---|---|---|
| `plugin.json` | Modify | Three settings after line 30 (Task 1) |
| `server/manifest.go` | Regenerate | `make apply` output only |
| `webapp/src/manifest.ts` | Regenerate | `make apply` output only |
| `server/services/config/config.go` | Modify | Three fields after line 74 (Task 3) |
| `server/boards/boardsapp.go` | Modify | Three key consts after line 32 (Task 4.1) |
| `server/boards/boardsapp_util.go` | Modify | Mapping after line 110 + `getPluginSettingString` after line 157 (Task 4.2) |
| `server/boards/configuration.go` | Modify | Three writes after line 90 (Task 4.3) |
| `server/services/notify/notifywebhook/settings.go` | Create | Parsing + filter (Task 5.1) |
| `server/services/notify/notifywebhook/webhook_backend.go` | Modify | Memoized `currentSettings`, filter gate, seam removal (Task 5.2) |
| `server/services/notify/notifywebhook/doc.go` | Modify | Second paragraph only (Task 5.3) |
| `server/services/notify/notifywebhook/settings_test.go` | Create | Task 6.1 |
| `server/services/notify/notifywebhook/webhook_backend_test.go` | Modify | Task 6.2 |
| `server/boards/boardsapp_test.go` | Modify | Task 6.3 |
| `server/boards/configuration_test.go` | Modify | Task 6.4 |

## Out-of-scope guardrails (hard NOs for this phase)

- **No signing, no delivery, no HTTP, no queue, no goroutines.** No `net/http`
  or `crypto/*` imports in the package, no channels, no `Start`/`ShutDown`
  changes. All Phase 3.
- **No envelope changes.** `envelope.go`, `envelope_test.go`, and
  `testdata/envelope_golden.json` stay byte-identical — the contract is frozen.
- **Do NOT touch the legacy `services/webhook` client**, its 7 call sites, or
  the dormant `WebhookUpdate`/`Secret` config fields (`config.go:50-51`,
  `boardsapp_util.go:100`). New fields only.
- **Do NOT extend the boards-local `configuration` struct**
  (`configuration.go:21-23`) or route through `setConfiguration` — frozen
  decision 4.
- **Do NOT touch `removeSecurityData`** (`config.go:135-138`) or add secret
  redaction — pre-existing quirk, Phase 4 PR-notes material.
- **No webapp source changes** beyond the regenerated `manifest.ts`; no custom
  System Console component — plain schema settings only.
- **No changes to** `server/services/notify/service.go`,
  `server/server/server.go`, `server/utils/callbackqueue.go`, or any other
  notify backend.
- **No new dependencies; no go.mod changes; no plugin.json version field.**
- **Do not weaken or skip linters**; the only sanctioned suppression removal
  is deleting the Phase 1 `//nolint:unparam` along with its seam.

## Implementation Summary

Implemented the three System Console settings and regenerated both manifest
artifacts with `make apply`. Added the raw configuration fields, initial and
live setting propagation, HTTPS/loopback URL validation, memoized parsing, and
the action/block-type filter. Added parsing, live-config, backend-filter, and
Boards configuration tests. Phase 2's focused build and race-enabled tests
passed before Phase 3 implementation began.
