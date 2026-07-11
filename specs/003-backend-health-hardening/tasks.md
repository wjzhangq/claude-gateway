---
description: "Task list for Backend Health Hardening"
---

# Tasks: Backend Health Hardening

**Input**: Design documents from `/specs/003-backend-health-hardening/`

**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/

**Tests**: Included. This feature is state-machine heavy with an injected clock (`lb.nowFn`) and rng (`lb.rng`); unit tests in `internal/proxy/*_test.go` are the primary validation vehicle (see quickstart.md).

**Organization**: Tasks are grouped by the 12 user stories from spec.md, in priority order (US1–US6 = P1, US7–US10 = P2, US11–US12 = P3).

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- Most core logic lives in the single file `internal/proxy/balancer.go`, so tasks touching it are **not** [P] relative to each other even across stories. `[P]` is used only for genuinely separate files.

## Path Conventions

Single Go project. Primary paths: `internal/proxy/balancer.go`, `internal/proxy/handler.go`, `internal/proxy/*_test.go`, `cmd/check/main.go`, `cmd/server/main.go`, `internal/stats/collector.go`, `internal/model/model.go`, `internal/db/`, `config/config.go`.

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Confirm baseline and land shared constants.

- [X] T001 Confirm baseline builds and 002 tests pass: run `go build ./...` and `go test ./internal/proxy/...` from repo root; record the green baseline before edits.
- [X] T002 Add the new tunable `const` block (values from research.md R-table: `quotaIsolationTTL`, `windowedMinSamples`/`windowedDegradeRate`/`windowedIsolateRate`/`windowedRecoverRate`, `authQuarantineMaxTTL`, `failoverBudget`, `quotaCascadeThreshold`/`cascadeWindow`, `retryAfterMin`/`retryAfterMax`) near the existing TTL/threshold consts in `internal/proxy/balancer.go`.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Storage + shared plumbing that multiple stories depend on.

**⚠️ CRITICAL**: Complete before US3 (probe cost), US4 (shared probe), and US12 (error_reason + metrics).

- [X] T003 Add DB migration `{36}` `ALTER TABLE usage_logs ADD COLUMN error_reason TEXT NOT NULL DEFAULT ''` to the `migrations` slice in `internal/db/db.go`, and add `error_reason TEXT NOT NULL DEFAULT ''` to the `usage_logs` CREATE statement (fresh-DB path) in the same file.
- [X] T004 Add `ErrorReason string \`db:"error_reason" json:"error_reason"\`` to `UsageLog` in `internal/model/model.go`; add `ErrorReason string` to `Record` in `internal/stats/collector.go` and map it in `recordToLog`.
- [X] T005 Update `BatchInsertUsageLogs` (and any single-insert path) in `internal/db/stats.go` to include `error_reason` in the column list and bound values.
- [X] T006 [P] Create `internal/proxy/errorreason.go` with `reasonCode(class ErrorClass, httpCode int, quotaBody bool) string` returning the ≤32-byte vocabulary from contracts/error-reason.md (`client_4xx`, `auth_401`, `forbidden_403`, `quota_403`, `rate_limit`, `server_5xx`, `transport`, `canceled`, `probe`, `unknown`, `""`).
- [X] T007 Create `internal/proxy/probe.go` with a shared reachability routine `probeReachable(client *http.Client, baseURL, apiKey string) (observedCode int, err error)` implementing the two-step `/v1/models` → `/v1/messages` (max_tokens=1) fallback, extracted so both `cmd/check` and `balancer.validateBackend` can call it.

**Checkpoint**: Storage column live, reason-code + shared probe available.

---

## Phase 3: User Story 1 - Instant failsafe on sudden fleet collapse (Priority: P1) 🎯 MVP

**Goal**: Failsafe becomes event-driven; no ~60s availability window.

**Independent Test**: Isolate enough backends in one step to drop `routableCount()` below `failsafeTrigger`; assert routable is restored to `min(failsafeTargetCap, total)` without advancing the clock past a tick, and `pick()` returns non-nil.

### Tests for User Story 1

- [X] T008 [P] [US1] Add tests in `internal/proxy/state_test.go`: (a) transition-driven failsafe restores routable floor immediately; (b) `pick()` with no routable candidate triggers a synchronous failsafe and returns non-nil; (c) hysteresis prevents double-promotion within `failsafeHysteresisSecs`.

### Implementation for User Story 1

- [X] T009 [US1] In `internal/proxy/balancer.go`, after the state write in `setIsolated` and `setQuarantine`, call `lb.failsafe(now)` when `lb.routableCount() < failsafeTrigger` (reuse existing hysteresis via `lastFailsafeAt`).
- [X] T010 [US1] In `pick(exclude)` in `internal/proxy/balancer.go`, when the SWRR scan yields `best == nil`, call `lb.failsafe(now)` once and re-scan a single time before returning nil. Verify all failsafe promotions use only atomic writes so they are safe under `lb.mu.RLock`.
- [X] T011 [US1] Document in the `failsafe` doc comment that promotion is one-shot to `min(failsafeTargetCap, total)=7` and now fires on transition + pick, not only the 60s tick.

**Checkpoint**: US1 independently testable.

---

## Phase 4: User Story 2 - Active probe carries the real failure signal (Priority: P1)

**Goal**: Probe-driven isolation uses the same `computeTTL` as the passive path, honoring the observed code + Retry-After.

**Independent Test**: `SetHealthStatusDetailed("b", false, 10, 429, now+120)` isolates with a Retry-After-honoring TTL, not the 30s server band; `observed_code=0` falls back to server band.

### Tests for User Story 2

- [X] T012 [P] [US2] Add tests in `internal/proxy/state_test.go` for `SetHealthStatusDetailed`: 429+Retry-After → clamped Retry-After TTL; 5xx → server band; 0 → server band; healthy=true progression unchanged.

### Implementation for User Story 2

- [X] T013 [US2] In `internal/proxy/balancer.go`, add `SetHealthStatusDetailed(name string, healthy bool, latencyMs int64, observedCode int, retryAfterUnix int64) bool`; move the unhealthy branch to `lb.setIsolated(b, observedCode, retryAfterUnix, false, now)`. Keep `SetHealthStatus(name, healthy, latencyMs)` as a wrapper calling `...Detailed(name, healthy, latencyMs, 0, 0)`.
- [X] T014 [US2] In `cmd/check/main.go`, capture the probe's HTTP status and `Retry-After` header and include `observed_code` + `retry_after` in each `healthBackend` payload entry.
- [X] T015 [US2] In `cmd/server/main.go` `/admin/api/backends/health` handler, parse the new optional fields and call `SetHealthStatusDetailed` (parse `retry_after` via the shared `parseRetryAfter`).

**Checkpoint**: US2 independently testable.

---

## Phase 5: User Story 3 - Probing does not waste money or worsen quota (Priority: P1)

**Goal**: No billable probe to quota-isolated backends; minimal probe; cost attributed.

**Independent Test**: Mark a backend quota-isolated, run probe cycle, confirm no `/v1/messages` call to it; fallback probes elsewhere use `max_tokens=1`; a `__probe__` cost row lands in `usage_logs`.

### Tests for User Story 3

- [X] T016 [P] [US3] Add a test in `cmd/check` (or a small helper unit test) asserting the probe-skip predicate (`state==isolated` AND (`quota_exhausted` OR `last_http_code==403`)) excludes those backends from probing.

### Implementation for User Story 3

- [X] T017 [US3] In `cmd/check/main.go` `runHealthProbe`, first GET `/admin/api/backends`, and skip probing any backend matching the quota-isolated predicate — report it `healthy=false` with no request (recovers via reset/TTL only).
- [X] T018 [US3] In `cmd/check/main.go`, change the `/v1/messages` fallback probe (and `probe.go` T007 routine) to `max_tokens: 1`; compute an estimated probe cost and set `probe_cost_usd` on the payload entry.
- [X] T019 [US3] In `cmd/server/main.go` `/admin/api/backends/health`, when `probe_cost_usd > 0`, `stats.Collector.Emit` a synthetic record (`Model="__probe__"`, `Backend=name`, `StatusCode=observed_code`, `ErrorReason="probe"`, `CostUSD=probe_cost_usd`); accumulate into `Backend.probeCostCents` for metrics.

**Checkpoint**: US3 independently testable.

---

## Phase 6: User Story 4 - No backend is silently lost at startup (Priority: P1)

**Goal**: Startup validation shares the probe fallback; inference-capable/listing-less backends aren't permanently disabled.

**Independent Test**: Backend that 404s `/v1/models` but serves `/v1/messages` is not `validationFailed` and becomes routable.

### Tests for User Story 4

- [X] T020 [P] [US4] Add a test (httptest stub) in `internal/proxy/state_test.go` (or new `probe_test.go`): stub 404 on `/v1/models`, 200 message on `/v1/messages` → `probeReachable` returns healthy; both fail → unavailable.

### Implementation for User Story 4

- [X] T021 [US4] Rewrite `validateBackend` in `internal/proxy/balancer.go` to call the shared `probeReachable` (T007) so a listing-less-but-inference-capable backend passes; only `validationFailed` when both checks fail.
- [X] T022 [US4] Add an hourly re-validation of `validationFailed` backends to `reconcileLoop`/`Reconcile` in `internal/proxy/balancer.go` (secondary safety net from research R4) so a transient startup failure self-heals.

**Checkpoint**: US4 independently testable.

---

## Phase 7: User Story 5 - Quota recovery not bound to hardcoded wall-clock (Priority: P1)

**Goal**: Default quota isolation = jittered long TTL + probe; optional per-backend `quota_reset`.

**Independent Test**: Default config → quota isolation `ttlUntil ≈ now + 6h`; `quota_reset="utc-midnight"` → TTL reflects UTC midnight.

### Tests for User Story 5

- [X] T023 [P] [US5] Add tests in `internal/proxy/state_test.go`: `MarkQuotaExhausted`/quota-403 with default policy sets ~6h jittered TTL and releases via `releaseExpired`; per-backend `utc-midnight` config computes the UTC instant.

### Implementation for User Story 5

- [X] T024 [P] [US5] Add optional `QuotaReset string` to `BackendAPI` in `config/config.go` (default `""`).
- [X] T025 [US5] In `internal/proxy/balancer.go`, replace `secondsUntilCSTMidnight` usage in `MarkQuotaExhausted` and the 403-quota path with a `quotaIsolationTTL` policy: default jittered 6h; if `QuotaReset` set, compute the per-backend reset instant. Keep `quotaResetLoop` only as a safety clear for `cst-midnight`/legacy backends.

**Checkpoint**: US5 independently testable.

---

## Phase 8: User Story 6 - Model-scoped error validity for Claude backends (Priority: P1)

**Goal**: Only sonnet/haiku/opus errors count against a Claude backend's health.

**Independent Test**: 500 on `model=gpt-4o` → no state/consecErr/ttl change; 500 on `claude-sonnet-4-6` → normal isolation; empty model → counted.

### Tests for User Story 6

- [X] T026 [P] [US6] Add handler-level tests in `internal/proxy/fixes_test.go` (or `handler_test.go`): non-health-model error leaves backend state untouched; valid-family error isolates; undeterminable model counts.

### Implementation for User Story 6

- [X] T027 [P] [US6] Add `isValidHealthModel(model string) bool` (true only when lowercased name contains `sonnet`/`haiku`/`opus`) to `internal/proxy/handler.go`.
- [X] T028 [US6] In `internal/proxy/handler.go` `forward` (and the failover branches), gate the health-impacting `RecordResult*` calls on `isValidHealthModel(reqModel)`; when false, still record the request/usage-log but skip health mutation. Empty/undeterminable `reqModel` → treat as valid (count).

**Checkpoint**: US6 independently testable. **End of MVP / P1 set.**

---

## Phase 9: User Story 7 - Windowed error-rate signal for flapping backends (Priority: P2)

**Goal**: Sustained high windowed error rate degrades/isolates even without consecutive errors.

**Independent Test**: Alternating 200/500 (≥8 samples, rate ≥0.5) settles to Degraded/Isolated instead of flapping through Healthy.

### Tests for User Story 7

- [X] T029 [P] [US7] Add tests in `internal/proxy/balancer_test.go`: windowed rate ≥0.5 → Degraded; ≥0.8 → Isolated; recovery allowed when <0.3.

### Implementation for User Story 7

- [X] T030 [US7] Add `windowedErrorRate(now) (rate float64, samples int)` to `Backend` in `internal/proxy/balancer.go` (reads the `statusMu`-guarded ring via `recentCodes`, counting 403/429/5xx/transport as errors).
- [X] T031 [US7] In `softUpDown` in `internal/proxy/balancer.go`, apply the windowed degrade/isolate thresholds (min 8 samples) and the <0.3 recovery gate.

**Checkpoint**: US7 independently testable.

---

## Phase 10: User Story 8 - Auth-quarantine exponential backoff (Priority: P2)

**Goal**: Repeated 401 quarantine grows TTL; key-rotation signal resets it.

**Independent Test**: 401 quarantine repeated → TTL 15m→30m→1h… capped 24h; `key_rotated`/2xx resets.

### Tests for User Story 8

- [X] T032 [P] [US8] Add tests in `internal/proxy/state_test.go`: exponential auth TTL growth capped at `authQuarantineMaxTTL`; reset on 2xx and on key-rotation signal.

### Implementation for User Story 8

- [X] T033 [US8] Add `authQuarantineCount atomic.Int64` to `Backend`; in `setQuarantine` for code 401 compute `ttl = min(ttlBase401 << authQuarantineCount, authQuarantineMaxTTL)` and increment; reset to 0 in the `ErrNone` branch of `recordResult`. All in `internal/proxy/balancer.go`.
- [X] T034 [US8] Add a `key_rotated` reset path: extend the `/admin/api/backends/health` handler in `cmd/server/main.go` (and a balancer method) to reset `authQuarantineCount` + `ttlUntil` when `key_rotated=true`.

**Checkpoint**: US8 independently testable.

---

## Phase 11: User Story 9 - Bounded, budget-aware failover chain (Priority: P2)

**Goal**: Failover chain has a total deadline, fast-fails on quota cascade, prefers low-budget targets.

**Independent Test**: ≥3 quota reports within 10s → next request fast-fails; cumulative failover time ≤ 20s.

### Tests for User Story 9

- [X] T035 [P] [US9] Add tests: `quotaCascadeActive` true after threshold reports in window; failover loop stops at `chainDeadline`. Place in `internal/proxy/fixes_test.go` (handler) + `balancer_test.go` (cascade counter).

### Implementation for User Story 9

- [X] T036 [US9] Add `noteQuotaReport(now)` + `quotaCascadeActive(now) bool` (short-window counter, guarded slice/mutex) to `LoadBalancer` in `internal/proxy/balancer.go`; call `noteQuotaReport` from `MarkQuotaExhausted`.
- [X] T037 [US9] In `internal/proxy/handler.go` failover loop, add `chainDeadline = start + failoverBudget` and stop failing over past it; fast-fail when `quotaCascadeActive(now)`. Confirm failover uses `PickExcluding` (budget-biased via existing SWRR effective weight).

**Checkpoint**: US9 independently testable.

---

## Phase 12: User Story 10 - Retry-After honored within sane bounds (Priority: P2)

**Goal**: Clamp honored Retry-After to [5s, 30min].

**Independent Test**: `Retry-After: 86400` → capped 30min; `0`/negative → code base TTL.

### Tests for User Story 10

- [X] T038 [P] [US10] Add tests in `internal/proxy/state_test.go` (or `handler_test.go` for `parseRetryAfter`): large value clamped to 30min; near-zero floored/rejected to base TTL.

### Implementation for User Story 10

- [X] T039 [US10] In `computeTTL` (and the Retry-After honoring branch) in `internal/proxy/balancer.go`, clamp the honored value to `[retryAfterMin, retryAfterMax]`.

**Checkpoint**: US10 independently testable.

---

## Phase 13: User Story 11 - Degraded state has a real, documented trigger (Priority: P3)

**Goal**: Make the passive transient first-strike reach Degraded; document all Degraded entries.

**Independent Test**: Single 500 while Healthy → Degraded (not immediate Isolated); reaching `consecErrToDegrade` transient errors → Isolated. Every documented Degraded transition is reachable.

### Tests for User Story 11

- [X] T040 [P] [US11] Add tests in `internal/proxy/state_test.go` enumerating each Degraded transition (probe recovery, windowed rate, transient first-strike) and asserting a single 500 degrades rather than isolates; 403/401 still immediate.

### Implementation for User Story 11

- [X] T041 [US11] In `recordResult` in `internal/proxy/balancer.go`, for transient classes (429/5xx/transport) while Healthy, set Degraded and increment `consecErr`, only isolating at `consecErr >= consecErrToDegrade`; keep 403→Isolated and 401→Quarantine immediate. Update the `RecordResult` doc comment to list all Degraded triggers.

**Checkpoint**: US11 independently testable.

---

## Phase 14: User Story 12 - Observability: split counters + metrics + error_reason (Priority: P3)

**Goal**: Distinct cumulative/consecutive counters; per-backend metrics endpoint; persisted error_reason.

**Independent Test**: `ErrCount` (cumulative) diverges from `ConsecFailures` after recovery; `Metrics()` reports isolation-by-code/failsafe/probe-cost/dwell/first-403; a 429 row carries `rate_limit`, a 2xx carries `''`.

### Tests for User Story 12

- [X] T042 [P] [US12] Add tests in `internal/proxy/balancer_test.go`: `totalErr` accumulates and diverges from `consecErr`; `Metrics()` fields populate after transitions; `reasonCode` mapping table.

### Implementation for User Story 12

- [X] T043 [US12] Add metric fields to `Backend` in `internal/proxy/balancer.go` per data-model.md (`totalErr`, `isolationCountByCode`+`metricsMu`, `failsafePromotions`, `probeCostCents`, `dwellByState`, `estSpendAtFirst403Cents`, `limitAtFirst403Cents`); increment at the right transition points (`setIsolated` code tally, `failsafe` promotions, `setState` dwell accumulation, first quota-403 snapshot, `recordResult` `totalErr++`).
- [X] T044 [US12] Fix `BackendInfo` mapping in `GetBackends`: `ErrCount` → `totalErr`, `ConsecFailures` → `consecErr` (currently both `consecErr`).
- [X] T045 [US12] Add `BackendMetrics` struct + `Metrics() []BackendMetrics` to `internal/proxy/balancer.go`, and wire `GET /admin/api/backends/metrics` in `cmd/server/main.go`.
- [X] T046 [US12] In `internal/proxy/handler.go`, set `Record.ErrorReason = reasonCode(class, httpCode, quotaBody)` on every emitted `stats.Record` (empty for 2xx) so it persists to `usage_logs`.

**Checkpoint**: US12 independently testable.

---

## Phase 15: Polish & Cross-Cutting Concerns

- [X] T047 Run `go build ./...` and full `go test ./...`; fix any regressions in existing 002 tests.
- [X] T048 Run `gitnexus_detect_changes()` to confirm changes only affect expected symbols/flows before committing.
- [X] T049 [P] Execute quickstart.md manual validations for US3 (probe skip + cost row) and US4 (listing-less backend) against a local stub or dev config.
- [ ] T050 [P] Update `AdminBackendsPage.tsx` (web) to surface the new `/metrics` fields and the split cumulative/consecutive error counts (optional UI follow-up). DEFERRED: backend `/admin/api/backends/metrics` endpoint + split counters are wired and tested; the existing UI's `ErrCount` now shows cumulative errors automatically. Frontend surfacing left as a separate follow-up per its optional marking.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: none.
- **Foundational (Phase 2)**: after Setup. Blocks US3 (T019 needs T004 Record.ErrorReason + probe cost), US4 (T021 needs T007 probe), US12 (T043–T046 need T003–T006).
- **User Stories (Phase 3+)**: after Foundational. US1, US2, US5, US6, US7, US8, US9, US10, US11 depend only on Setup+Foundational and are logically independent; US3 depends on T007; US4 depends on T007; US12 depends on Phase 2.
- **Polish (Phase 15)**: after all targeted stories.

### File-contention note (limits real parallelism)

`internal/proxy/balancer.go` is touched by almost every story (T009–T011, T013, T021–T022, T025, T030–T031, T033, T036, T039, T041, T043–T045). These are **sequential** despite living in different phases — do not run them in parallel. Genuinely parallel `[P]` work: the test files, `errorreason.go` (T006), `probe.go` (T007), `config.go` (T024), `handler.go` model helper (T027), and the web UI (T050).

### Within Each User Story

- Tests first (should fail), then implementation.
- For stories touching balancer.go, complete the balancer edit before the `cmd/*` wiring that calls it.

---

## Parallel Example: Foundational + test authoring

```bash
# After T003–T005 (DB/model plumbing, sequential in db.go/model.go), these are parallel:
Task: "T006 Create internal/proxy/errorreason.go (reasonCode)"
Task: "T007 Create internal/proxy/probe.go (probeReachable)"

# Test authoring across stories is parallel (separate concerns in test files):
Task: "T008 [US1] failsafe tests in state_test.go"
Task: "T012 [US2] SetHealthStatusDetailed tests"
Task: "T026 [US6] model-family gate tests in fixes_test.go"
```

---

## Implementation Strategy

### MVP scope (P1: US1–US6)

1. Phase 1 Setup → Phase 2 Foundational.
2. US1 (event failsafe) → US2 (probe code) → US3 (probe cost) → US4 (startup fallback) → US5 (quota clock) → US6 (model-scoped errors).
3. **STOP & VALIDATE**: the P1 set closes the real availability/cost/silent-loss risks the reviewer flagged as priority. Ship as the MVP.

### Incremental delivery

- P2 (US7–US10): robustness — flapper suppression, auth backoff, bounded failover, Retry-After clamp.
- P3 (US11–US12): degraded semantics + observability (metrics endpoint, error_reason, UI).

---

## Notes

- `[P]` = different files, no dependency. Balancer.go tasks are never mutually `[P]`.
- Time and randomness are injected (`lb.nowFn`, `lb.rng`) — tests set them directly; no sleeps.
- The `error_reason` column stores a classified code only (≤32 bytes), not raw upstream text (research R12).
- Per CLAUDE.md: run `gitnexus_impact` before editing shared symbols (`recordResult`, `setIsolated`, `Pick`, `GetBackends`) and `gitnexus_detect_changes` before committing (T048).
- Commit after each story checkpoint.
