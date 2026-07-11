---
description: "Task list for Backend Auto-Eject & Recovery"
---

# Tasks: Backend Auto-Eject & Recovery

**Input**: Design documents from `/specs/002-backend-auto-eject-recovery/`

**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/

**Tests**: INCLUDED — quickstart.md explicitly requests a `go test ./internal/proxy/` suite (10 coverage items). Test tasks precede their implementation within each story.

**Organization**: Tasks are grouped by user story (US1–US5 from spec.md) in priority order.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel — different file, no dependency on an incomplete task.
- ⚠️ **Shared-file reality**: US1–US5 nearly all edit `internal/proxy/balancer.go`. Tasks in the same file are **sequential**, so `[P]` appears only across genuinely distinct files (`handler.go`, `cmd/server/main.go`, `config/config.go`, separate test files). Stories stay independently *testable* even though they share a file.

## Path Conventions

Single Go project. Primary file: `internal/proxy/balancer.go`. Tests: `internal/proxy/balancer_test.go` (existing, `package proxy_test`) and a new `internal/proxy/state_test.go`. Request path: `internal/proxy/handler.go`. Server wiring: `cmd/server/main.go`.

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Confirm a green baseline before mutating the balancer.

- [ ] T001 Confirm baseline builds and tests pass: run `go build ./...` and `go test ./internal/proxy/` from repo root; record current pass state so regressions are attributable.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Extend the state model, struct, classification, and clock injection that ALL stories depend on. All in `internal/proxy/balancer.go` → sequential.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

- [ ] T002 Add new state constants `stateIsolated=3`, `stateProbing=4`, `stateQuarantine=5` (keep `stateDisabled=2` for back-compat mapping) and extend `stateName()` to return `"isolated"|"probing"|"quarantine"` in `internal/proxy/balancer.go`.
- [ ] T003 Add new `Backend` fields as `atomic.Int64` unix-seconds — `enteredAt`, `ttlUntil`, `retryAfter`, `lastHTTPCode`, `lastSuccessAt`, `probeShieldUntil` — plus `currentWeight int` (SWRR cursor) and a `selMu sync.Mutex` on `LoadBalancer`, per data-model.md, in `internal/proxy/balancer.go`.
- [ ] T004 Add injectable clock and randomness — `nowFn func() time.Time` (default `time.Now`) on `LoadBalancer` and a jitter helper using an injectable rand source (R11) — so TTL/window/jitter logic is deterministically testable, in `internal/proxy/balancer.go`.
- [ ] T005 Split `ClassifyError`: introduce `ErrForbidden` for **403 only** (separate from `ErrAuth` = **401 only**); keep 400/404/422 (and other 4xx) as `ErrClient`; update `countsAgainstHealth()` to include `ErrForbidden`, in `internal/proxy/balancer.go`.
- [ ] T006 Add `recentCodes(window)` helper that returns codes from the last ~20 `statusEntries` or within the last ~5 minutes (whichever is tighter), reading under `statusMu`, in `internal/proxy/balancer.go`.
- [ ] T007 Add `Reconcile(now time.Time)` orchestrator skeleton on `LoadBalancer` that calls ordered step methods (`releaseExpired`, `judgeProbing`, `softUpDown`, `refreshWeights`, `failsafe`) as no-op stubs to be filled by later stories, in `internal/proxy/balancer.go`.

**Checkpoint**: State model, fields, classification, clock, and reconcile scaffold exist. Stories can begin.

---

## Phase 3: User Story 1 - Auto-eject on real provider response (Priority: P1) 🎯 MVP

**Goal**: A backend returning a node-level failure (429/403/401/5xx) leaves the routable pool within one request; request-level 4xx (400/404/422) pass through untouched.

**Independent Test**: Call `RecordResult` with each class and assert state (Isolated for 429/403/5xx, Quarantine for 401 and for consec≥5) and TTL; assert 400/404/422 leave state and `consecErr` unchanged.

### Tests for User Story 1

- [ ] T008 [US1] Write failing tests for exit rules — 429/403/5xx → Isolated with correct base TTL; 401 → Quarantine + alert; consecErr≥5 → Quarantine; 400/404/422 → no state/counter change (FR-005..FR-008) — in `internal/proxy/state_test.go`.
- [ ] T009 [US1] Write failing tests for TTL policy — 429 base 30s / cap 5min, 403 base 2min / cap 30min, 5xx 30s; `Retry-After: 120` → TTL≈120s; 403 quota-body → TTL to next 00:00 CST (FR-010..FR-013) — in `internal/proxy/state_test.go`.

### Implementation for User Story 1

- [ ] T010 [US1] Implement `computeTTL(code, retryAfterUnix, consec)` returning base×backoff capped per code, honoring Retry-After and 403-quota→daily-reset (reuse `time.FixedZone("CST", 8*3600)` from `quotaResetLoop`), with ±20% jitter on set, in `internal/proxy/balancer.go`.
- [ ] T011 [US1] Refine `RecordResult` to drive exits: `ErrRateLimit`/`ErrServer`/`ErrTransport`/`ErrForbidden` → Isolated + `computeTTL` + `consecErr++`; `ErrAuth`(401) → Quarantine + alert; `consecErr≥5` → Quarantine; `ErrClient`/`ErrCanceled` → no change; respect `probeShieldUntil`; set `enteredAt`, in `internal/proxy/balancer.go`.
- [ ] T012 [US1] Add `SetIsolated`/`SetQuarantine` state helpers and an `alert(name, reason)` function (logrus/`log.Printf` warning) invoked on Quarantine entry (FR-038), in `internal/proxy/balancer.go`.
- [ ] T013 [P] [US1] In the request path, parse the `Retry-After` response header on 429 (delta-seconds and HTTP-date forms) into an absolute unix time and pass it to the backend via a single call (e.g. `SetIsolated(code, retryAfter)`); ensure 400/404/422 responses return to the caller with no health impact, in `internal/proxy/handler.go` (around lines 410–493).
- [ ] T014 [US1] Update request-path call sites to record `lastHTTPCode` and, on 2xx, `lastSuccessAt` via `RecordRequest`, in `internal/proxy/handler.go`.

**Checkpoint**: Ejection works and is unit-tested. ⚠️ Nodes are ejected but not yet auto-recovered — do NOT ship US1 alone (see Implementation Strategy).

---

## Phase 4: User Story 2 - Progressive recovery of a cooled-down backend (Priority: P1)

**Goal**: After TTL expiry a backend enters half-open Probing (weight 1), a successful trial promotes it (Degraded→Healthy, reset counters), a failed trial re-isolates it with doubled TTL.

**Independent Test**: Isolate a backend, advance the injected clock past its TTL, call `Reconcile(now)`, assert Probing; then simulate probe success/failure and assert the resulting state and TTL.

### Tests for User Story 2

- [ ] T015 [US2] Write failing tests — expired Isolated →(Reconcile)→ Probing; probe success → Degraded → (5min clean) → Healthy with `consecErr=0`; probe fail → Isolated with TTL×2 (capped); jitter spreads simultaneous releases (FR-014..FR-019) — in `internal/proxy/state_test.go`.

### Implementation for User Story 2

- [ ] T016 [US2] Implement the `releaseExpired` reconcile step: Isolated/Quarantine with `now > ttlUntil` → Probing (weight 1), set `enteredAt`, in `internal/proxy/balancer.go`.
- [ ] T017 [US2] Implement the `judgeProbing` reconcile step: read `recentCodes`/latest result of Probing backends → success → Degraded (`consecErr=0`); failure → Isolated with `ttlUntil=min(ttl×2, cap)`, in `internal/proxy/balancer.go`.
- [ ] T018 [US2] Implement Degraded→Healthy promotion after clean 5min (`consecOK`/`lastSuccessAt`), in `internal/proxy/balancer.go`.
- [ ] T019 [US2] Update `SetHealthStatus` so a `healthy=true` probe moves Isolated/Quarantine → Probing (was disabled→degraded) and Probing/Degraded advance toward Healthy; `healthy=false` demotes per the 5-state machine (FR-016, FR-035), in `internal/proxy/balancer.go`.

**Checkpoint**: Eject + progressive recovery both work and are unit-tested — safe minimal increment.

---

## Phase 5: User Story 3 - Minimum-availability failsafe (Priority: P1)

**Goal**: When routable (Healthy+Degraded+Probing) < 3, force-promote the best candidates to Probing until routable ≥ min(7, total), without thrashing.

**Independent Test**: Force most backends to Isolated/Quarantine, call `Reconcile(now)`, assert routable rises to the target, promoted nodes are shielded 60s, the failsafe does not re-trigger within the hysteresis window, and selection rotates by fewest-failures.

### Tests for User Story 3

- [ ] T020 [US3] Write failing tests — routable<3 promotes to ≥min(7,total); candidate ranking order (shortest TTL → 429/5xx before 403 before Quarantine/401 → lower budget → lower latency → fewer consec); `probeShieldUntil`=now+60s; hysteresis blocks immediate re-trigger; fewest-failures rotation (FR-021..FR-026) — in `internal/proxy/state_test.go`.

### Implementation for User Story 3

- [ ] T021 [US3] Implement `routableCount()` and a candidate ranking comparator over Isolated/Quarantine backends per FR-023, in `internal/proxy/balancer.go`.
- [ ] T022 [US3] Implement the `failsafe` reconcile step: when `routableCount()<3`, promote ranked candidates to Probing (ignoring TTL) until ≥min(7,total), set `probeShieldUntil`, record `lastFailsafeAt` for hysteresis, and rotate by fewest-failures, in `internal/proxy/balancer.go`.

**Checkpoint**: All three P1 stories functional and independently testable.

---

## Phase 6: User Story 4 - Budget & latency as soft routing signals (Priority: P2)

**Goal**: Budget and latency bias weight only — never eject, never zero. Selection uses smooth weighted round-robin so small-weight nodes are not starved.

**Independent Test**: Drive usage past the daily limit with no provider error → backend stays routable at reduced (non-zero) weight; raise latency → its traffic share drops; over many `Pick()` calls a low-weight node still gets selected.

### Tests for User Story 4

- [ ] T023 [US4] Write failing tests — `budgetFactor`: <80%→1.0, 80–95%→linear 0.3, 95–99%→0.05, ≥99%→0.05 (never 0); `latencyFactor`: >8s→8000/latency floor 0.3; SWRR distributes without starving the smallest weight (FR-009, FR-028..FR-031) — in `internal/proxy/balancer_test.go`.

### Implementation for User Story 4

- [ ] T024 [US4] Implement `budgetFactor(usageCents, limitCents)` and `latencyFactor(latencyMs)` pure helpers per the curves above, in `internal/proxy/balancer.go`.
- [ ] T025 [US4] Rewrite `effectiveWeight()` to `base × healthFactor(state) × budgetFactor × latencyFactor`; Probing pins to weight 1; Isolated/Quarantine/validationFailed → 0; Degraded health factor 0.2–0.5 (default 0.3), in `internal/proxy/balancer.go`.
- [ ] T026 [US4] Replace weighted-random selection in `Pick` and `PickExcluding` with smooth weighted round-robin using `currentWeight` under `selMu` (keep O(n), no per-call allocation), in `internal/proxy/balancer.go`.
- [ ] T027 [US4] Change `SetQuotaStatus` so `limit`/`usage` feed only the soft budget factor and `exhausted=true` no longer forces weight 0 or a state change (P1, FR-009), in `internal/proxy/balancer.go`.

**Checkpoint**: Weighting reflects budget+latency; no node is ejected on budget alone.

---

## Phase 7: User Story 5 - Periodic state reconciliation (Priority: P2)

**Goal**: A 60s convergence loop runs the ordered pass and is wired into the server, aligned with the per-minute `check --health` recovery signal.

**Independent Test**: With no live traffic, isolate a backend; after its TTL the next `Reconcile` tick moves it to Probing; a Degraded node clean for 5min promotes to Healthy; an idle Healthy node (>10min) is marked due-for-probe; consecErr decays after 5min clean.

### Tests for User Story 5

- [ ] T028 [US5] Write failing tests — `Reconcile(now)` runs steps in the FR-033 order; consecErr decrements after 5min clean; Healthy idle >10min marked due-for-probe (FR-020, FR-032..FR-034) — in `internal/proxy/state_test.go`.

### Implementation for User Story 5

- [ ] T029 [US5] Implement the `softUpDown` reconcile step: Healthy→Degraded on budget/latency soft signal; decrement `consecErr` after 5min clean; mark Healthy idle >10min as due-for-probe, in `internal/proxy/balancer.go`.
- [ ] T030 [US5] Fill the `refreshWeights` step and finalize the ordered `Reconcile` body (release → judge → soft → refresh → failsafe), in `internal/proxy/balancer.go`.
- [ ] T031 [US5] Replace the no-op `recoveryLoop` with a 60s ticker calling `lb.Reconcile(lb.nowFn())`, started from `NewLoadBalancer` (mirror `quotaResetLoop` goroutine pattern), in `internal/proxy/balancer.go`.
- [ ] T032 [P] [US5] Verify/adjust server wiring so the reconcile loop starts and the existing `/admin/api/backends/health` + `/admin/api/backends/quota` sync paths remain intact (they call `SetHealthStatus`/`SetQuotaStatus`), in `cmd/server/main.go`.

**Checkpoint**: Full state machine converges on a timer and via the external health probe.

---

## Phase 8: Polish & Cross-Cutting Concerns

- [ ] T033 [P] Extend `BackendInfo` and `GetBackends()` with `ttl_until`, `consec_failures`, `last_http_code`, and optional `budget_factor`/`latency_factor`; keep `state` string and `disabled` bool back-compatible (R7, FR-036/FR-037), in `internal/proxy/balancer.go`.
- [ ] T034 [P] (Optional) Expose tunable parameters (TTL bases/caps, thresholds, factor breakpoints) via config with in-code defaults, in `config/config.go`.
- [ ] T035 Run `go vet ./...` and `go test ./...`; fix any regressions in `internal/proxy` and dependents.
- [ ] T036 Run `gitnexus_detect_changes()` before committing (per CLAUDE.md) to confirm only expected symbols/flows changed.
- [ ] T037 Execute the quickstart.md integration validation against a fake upstream: auto-eject on 429, 400 passthrough, recovery via `./check --config config.yaml --health`, and failsafe under mass failure.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: none — start immediately.
- **Foundational (Phase 2)**: depends on Setup — **BLOCKS all user stories** (defines states, fields, classification, clock, reconcile scaffold).
- **User Stories (Phase 3–7)**: all depend on Foundational.
- **Polish (Phase 8)**: depends on the desired stories being complete.

### User Story Dependencies

- **US1 (P1)**: after Foundational. Independent (eject tested via direct `RecordResult`).
- **US2 (P1)**: after Foundational. Uses the `Reconcile` scaffold (T007) and states from US1's exits; tests call `Reconcile` directly, so it is testable independently of the US5 ticker.
- **US3 (P1)**: after Foundational. Failsafe step is independent; tests call `Reconcile` directly.
- **US4 (P2)**: after Foundational. Weighting/SWRR is independent of the state-transition stories.
- **US5 (P2)**: after Foundational. Orchestration + ticker; benefits from US2/US3 steps being implemented but the ordered body (T030) simply calls whatever steps exist.

### Within Each Story

- Tests first (they must fail), then implementation.
- In `balancer.go`, tasks are **sequential** (same file); do not attempt to parallelize them.

### Parallel Opportunities

- `[P]` tasks touch distinct files and may run alongside the current story's `balancer.go` work:
  - T013 (`handler.go`) alongside US1 balancer tasks.
  - T032 (`cmd/server/main.go`) alongside US5 balancer tasks.
  - T033 (`balancer.go`) and T034 (`config.go`) — T034 is `[P]` vs T033; T033 is not `[P]` vs other balancer tasks.
- Different developers can own different stories, but must coordinate edits to `balancer.go` (rebase frequently or split by method region).

---

## Parallel Example: User Story 1

```bash
# T013 (handler.go) can proceed in parallel with the balancer.go tasks T010–T012:
Task T013: "Parse Retry-After + request-level 4xx passthrough in internal/proxy/handler.go"
# Meanwhile, sequentially in balancer.go:
Task T010 → T011 → T012
```

---

## Implementation Strategy

### MVP scope

- **Nominal MVP = US1** (auto-eject). ⚠️ **But US1 alone strands ejected nodes**: US1 moves failing nodes to the new Isolated/Quarantine states, and old recovery only handled `stateDisabled`. **Ship US1 + US2 together** as the true safe minimal increment (eject + progressive recovery). US2 also updates `SetHealthStatus`, restoring `check --health`-driven recovery.

### Incremental delivery

1. Setup + Foundational → scaffold ready.
2. US1 + US2 → eject + recovery (validate, deploy — safe MVP).
3. US3 → failsafe (protects against correlated outages).
4. US4 → budget/latency soft weighting + SWRR.
5. US5 → periodic reconcile ticker (deterministic idle-fleet recovery).
6. Polish → status surface, optional config, full test + quickstart validation.

### Notes

- All state stays in memory; no migrations.
- Preserve `BackendInfo.State` string + `disabled` bool for the web UI (R7).
- Inject `nowFn`/rand for deterministic TTL/jitter tests (R11).
- Commit after each task or logical group; run `gitnexus_impact` before editing shared symbols (`RecordResult`, `Pick`, `effectiveWeight`, `SetHealthStatus`) per CLAUDE.md.
