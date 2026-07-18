# Phase 4: Tests and upstream hygiene

## Implementation Summary

Phase 4 gap-checking found every prescribed notifywebhook scenario already
covered by the Phase 1-3 implementation and review rounds. No test code was
added.

| Prescribed scenario | Existing coverage | Phase 4 action |
|---|---|---|
| Delivery and independent HMAC round-trip | `TestDelivery_SignatureRoundTrip`, `TestBackend_EndToEndDelivery`; test verifier uses `crypto/hmac` directly rather than `signPayload` | Verified existing |
| Event filter, including all nine block types | `TestParseEventFilter`, `TestEventFilterMatches`, `TestBackend_BlockChangedRespectsFilter` | Verified existing |
| Empty-config no-op | `TestBackend_NoopWhenUnconfigured` covers empty and nil config | Verified existing |
| Retry and backoff | `TestDelivery_RetryThenSuccess` | Verified existing |
| 4xx without retry | `TestDelivery_4xxNoRetry` | Verified existing |
| Request timeout | `TestDelivery_TimeoutRetries` | Verified existing |
| Full-queue drop | `TestDelivery_FullQueueDrop` | Verified existing |
| Shutdown drain | `TestDelivery_ShutdownDrainsInFlight` | Verified existing |
| Hung-endpoint shutdown deadline | `TestDelivery_ShutdownDrainHonorsDeadline` | Verified existing |
| Redirect rejection | `TestDelivery_RedirectNotFollowed` | Verified existing |
| Concurrent config mutation/publication race | `TestBackend_ConcurrentConfigPublication` under `-race` | Verified existing |
| Golden envelope | `TestEnvelopeJSON` plus `testdata/envelope_golden.json` | Verified existing |

Added `.planning/UPSTREAM_PR_NOTES.md` with the upstream framing, operator and
wire contracts, delivery semantics, single-node behavior, consumer dedup
requirement, producer quirks, manifest-generator limitation, and test
inventory.

CI verification:

- `FOCALBOARD_UNIT_TESTING=1 go test -race -count=1
  ./services/notify/notifywebhook/...` passed.
- The first plain `make server-ci` invocation stopped in Makefile setup because
  the manifest parser rejects the manually maintained setting-level `secret`
  field: `json: unknown field "secret"`.
- Supplying only Makefile metadata overrides allowed `make server-ci` to run
  its real `server-lint` and `server-test` recipes. `golangci-lint run ./...`
  passed. Database-backed `server/boards`, `server/integrationtests`, and
  `server/services/store/sqlstore` passed against the healthy `mm-postgres`
  container through its Docker bridge address.
- The full server test recipe had one failing package:
  `server/services/store/sqlstore/migrationstests`. Its pre-existing fixture
  lookup fails with `open migrations/sqlite3: file does not exist`; this is
  unrelated to notifywebhook and occurred after live Postgres setup succeeded.
- Re-running the exact race/coverage scope with only
  `server/services/store/sqlstore/migrationstests` excluded passed every other
  server package.
- The full Mattermost license vet found a pre-existing typo in
  `server/model/board_test.go:1` (`copywright`). The focused notifywebhook
  license check passed.
- The `make check-style` wrapper has a separate pre-existing empty-`GOBIN`
  issue and tries to execute `/mattermost-govet`; direct golangci-lint and
  mattermost-govet commands were used to distinguish wrapper failure from code
  findings.

