# Phase 0 Research: Backend Auto-Eject & Recovery

This document resolves the open questions implied by the spec against the **actual** codebase (`internal/proxy/balancer.go`, `internal/proxy/handler.go`, `cmd/check/main.go`, `cmd/server/main.go`). No NEEDS CLARIFICATION markers remained in the spec; the research below records design decisions and how each maps onto existing code.

---

## R1. State model: extend the existing 3-state enum to 5 states

**Decision**: Extend the current `int32` state constants from 3 (`stateHealthy=0`, `stateDegraded=1`, `stateDisabled=2`) to 5 by adding `stateIsolated` and `stateQuarantine`, and rename `stateDisabled`'s role. Map to spec states:

| Spec state | Code constant | Routable | Notes |
|---|---|---|---|
| Healthy | `stateHealthy` | yes | unchanged |
| Degraded | `stateDegraded` | yes | now weight ×0.2–0.5 (was ÷4 = ×0.25, already in range) |
| Isolated | `stateIsolated` (new) | **no** | TTL-based, code-specific cooldown |
| Probing | `stateProbing` (new) | yes | half-open, weight = 1 |
| Quarantine | `stateQuarantine` (new) | **no** | long TTL + alert (replaces today's terminal `stateDisabled` semantics for 401) |

**Rationale**: The current `stateDisabled` conflates two spec concepts — transient isolation (429/5xx/consec-errors) and long quarantine (401 auth). Splitting them lets TTL policy differ by cause. `atomic.Int32` stays the state carrier; new fields (`ttlUntil`, `enteredAt`, `retryAfter`) are added as `atomic.Int64` unix-seconds to preserve the lock-free per-field access pattern already used (`consecErr`, `lastErr`, `lastLatencyMs`).

**Alternatives considered**: A separate state-machine struct with its own mutex — rejected: the codebase deliberately uses per-field atomics + a narrow `statusMu` only for the ring buffer, and adding a coarse lock would regress the hot-path `Pick`.

---

## R2. Exit driven by *recent* real HTTP codes, not a single field

**Decision**: Judge a backend on its recent code history, not one stored code. The `Backend` already keeps a ring buffer `statusEntries []StatusEntry{Code, LatencyMs, Timestamp}` sized `max(50, 10×count)`. Add a helper `recentCodes(window)` that returns codes from the last N entries (N≈20) **or** within the last ~5 minutes (whichever is the tighter, per the spec's "last ~20 requests or 5 minutes"). Request-path classification still acts on the immediate response (fast ejection, SC-001); the reconcile loop and failsafe ranking use the windowed view.

**Rationale**: Matches spec §Edge Cases ("Sparse HTTP history") and the user's note that "backend http code 是内存记录，考虑查看最近20次或者5分钟内". The ring buffer already exists and is concurrency-safe under `statusMu`; no new storage.

**Alternatives considered**: Add a dedicated small counter per code class — rejected, redundant with the ring buffer and its `statusCodeDist` map.

---

## R3. Code → action mapping (node-level vs request-level 4xx)

**Decision**: Refine `ClassifyError` and `RecordResult`. Current code maps 401 **and** 403 together to `ErrAuth → disable`, and 429 to `ErrRateLimit`. The spec requires finer treatment:

| HTTP code | Class | Action |
|---|---|---|
| 2xx | `ErrNone` | success; reset consec failures, drive recovery |
| 400 / 404 / 422 | `ErrClient` (request-level) | **pass back to caller, no state change, no failure count** |
| other 4xx (e.g. 402/405/409…) | `ErrClient` | same as request-level (Assumptions default) |
| 401 | `ErrAuth` | → **Quarantine** + alert (15–30min TTL) |
| 403 | `ErrForbidden` (new) | → Isolated, base 2min, cap 30min; quota-body → lock to daily reset |
| 429 | `ErrRateLimit` | → Isolated, base 30s, cap 5min; honor Retry-After |
| 5xx | `ErrServer` | → Isolated, base 30s, cap 5min |
| transport | `ErrTransport` | counts as server-class error toward Isolated |
| client-canceled | `ErrCanceled` | ignored (already handled) |

**Rationale**: Directly encodes spec FR-005..FR-008 and the confirmed decision "只退 401/403/429". Splitting 403 out of `ErrAuth` is required because 403 is now transient-isolated (not quarantined) while 401 quarantines. `countsAgainstHealth()` extends to include `ErrForbidden`.

**Note on current 403 behavior**: today 403 disables like 401. New behavior isolates 403 with a 2min base instead, which is less aggressive and matches "配额/禁止,可能真到额".

---

## R4. TTL policy, backoff, and jitter

**Decision**: Store `ttlUntil` (unix seconds) on the backend. On entering Isolated/Quarantine, compute TTL from the most recent code:

| Code | Base TTL | Cap | Special |
|---|---|---|---|
| 429 | 30s | 5min | if `Retry-After` present → use it directly |
| 403 | 2min | 30min | body clearly quota → TTL = seconds until next 00:00 CST |
| 5xx / transport | 30s | 5min | — |
| 401 | 15min (→Quarantine) | 30min | always Quarantine + alert |

Backoff: on a failed probe, `ttl = min(ttl×2, cap)`. Jitter: on **release/set**, apply `±20%`: `ttl = ttl × (0.8 + 0.4×rand)`. Daily-reset computation reuses the CST midnight math already in `quotaResetLoop` (`time.FixedZone("CST", 8*3600)`).

**Rationale**: Encodes FR-010..FR-015 and §11 parameters. Reusing the existing CST zone keeps quota-lock behavior consistent with `quotaResetLoop`.

**Alternatives considered**: Monotonic `time.Timer` per backend — rejected: 42 timers add lifecycle complexity; a polled `ttlUntil` checked by the 60s reconcile loop (and lazily at `Pick` time) is simpler and matches the existing tick-based `quotaResetLoop`/`aggregator` pattern.

---

## R5. Retry-After parsing at the request path

**Decision**: In `handler.go`, where 429 is currently detected (around line 441–449), read the `Retry-After` response header. Support both delta-seconds (`Retry-After: 120`) and HTTP-date forms; convert to an absolute unix-seconds `retryAfter` stored on the backend, consumed by the TTL policy (R4). Add a `Backend.SetIsolated(code, retryAfterUnix)` method so the handler passes the parsed value in one call.

**Rationale**: FR-012. The header is only available at the response, i.e. on the request path, so the parse must live in the handler and be handed to the balancer (the balancer must not re-read HTTP).

**Alternatives considered**: Parse inside `RecordRequest` — rejected: `RecordRequest` only receives `(code, latencyMs)` and has no header access; keeping HTTP parsing in the handler preserves the layering.

---

## R6. Weight factors and smooth weighted round-robin

**Decision**: Replace `effectiveWeight()`'s branch-only logic with `base × healthFactor × budgetFactor × latencyFactor`, and replace random selection in `Pick`/`PickExcluding` with **smooth weighted round-robin (SWRR)**.

- **healthFactor**: Healthy 1.0; Degraded 0.2–0.5 (use 0.3, ≈ current ÷4); Probing → fixed weight 1 (bypasses factors); Isolated/Quarantine → 0 (not selectable).
- **budgetFactor** (soft, from `quotaUsage/quotaLimit` or config `daily_limit`): `<80%`→1.0; `80–95%`→linear to 0.3; `95–99%`→linear to 0.05; `≥99%`→0.05 (never 0). Never sets weight to 0 and never changes state (P1, FR-009/FR-028).
- **latencyFactor**: `lastLatencyMs ≤ 8000`→1.0; else `8000/lastLatencyMs`, floor 0.3 (FR-029).
- **SWRR**: classic Nginx-style `currentWeight += effectiveWeight; pick max; selected.currentWeight -= total`. Requires a mutable `currentWeight int` per backend, mutated under `lb.mu` — `Pick` currently holds only `RLock`, so selection state mutation moves to a `Lock` (or a dedicated `selMu`). See R8.

**Rationale**: FR-027..FR-031. SWRR prevents starvation of small-weight nodes that pure `rand.Intn(total)` allows when weights are skewed (2 nodes at 5000 vs 40 at 400).

**Alternatives considered**: Keep weighted-random — rejected by FR-031. EDF/AVL schedulers — rejected as over-engineered for 42 backends.

---

## R7. Backward-compatible status surface for the web UI

**Decision**: Keep `BackendInfo.State string` and extend `stateName()` to return `"isolated"`, `"probing"`, `"quarantine"` alongside `"healthy"`/`"degraded"`/`"disabled"`. Keep `Disabled bool` = `effectiveWeight()==0` for old consumers. Add optional fields `TTLUntil int64`, `ConsecFailures` (already `ErrCount`), `LastHTTPCode int` to `BackendInfo` for richer display. The web dashboard (`web/src`) reads `state` + `disabled`; new states render as their string, and `disabled` stays truthful for isolated/quarantine.

**Rationale**: Avoids breaking `/admin/api/...` JSON consumers. FR-036/FR-037 want these fields surfaced anyway.

---

## R8. Concurrency model for selection state

**Decision**: SWRR needs to mutate `currentWeight` on every pick. Introduce a dedicated `selMu sync.Mutex` guarding only the SWRR cursor, taken inside `Pick`/`PickExcluding`, so the broader `lb.mu.RLock()` still protects the slice for readers. State/TTL fields stay on per-field atomics; the reconcile loop takes `lb.mu.RLock()` to iterate (like `quotaResetLoop`) and mutates backend state via atomics.

**Rationale**: Keeps the hot path contention-narrow. A single global `selMu` is fine at ~42 backends and one selection per request.

**Alternatives considered**: Per-backend atomic CAS loop for SWRR — rejected: SWRR's "pick the max then decrement" is inherently a critical section across all backends; a short mutex is simpler and correct.

---

## R9. Reconcile loop wiring and reuse of `cmd/check --health`

**Decision**: Add `lb.reconcileLoop()` started from `NewLoadBalancer` (replacing the no-op `recoveryLoop`), ticking every 60s, executing the ordered pass from FR-033: (1) release expired TTL → Probing; (2) judge Probing by recent result → Degraded (success, reset consec) or Isolated ×2 (fail); (3) soft demote/promote (Degraded→Healthy after 5min clean; Healthy→Degraded on soft signal; decay consec after 5min clean); (4) refresh weights (recompute cached factors); (5) failsafe check. The existing per-minute `cmd/check --health` cron continues to POST `/admin/api/backends/health`, which calls `lb.SetHealthStatus`; that path is extended to move Isolated/Quarantine→Probing on a healthy probe (currently it does disabled→degraded).

**Rationale**: FR-032..FR-035. The 60s tick aligns with the existing external `check --health` cadence, giving two independent recovery drivers (internal reconcile + external probe) as the spec intends ("配合 ./cmd/check 定期恢复").

**Alternatives considered**: Drive everything from `cmd/check` only — rejected: an idle/fully-isolated fleet with no live traffic still needs the internal tick to release TTLs and run the failsafe deterministically.

---

## R10. Failsafe (minimum availability) with anti-thrash

**Decision**: In the reconcile pass, if routable (`Healthy+Degraded+Probing`) `< 3`, force-promote best isolated/quarantined candidates to Probing until routable `≥ min(7, totalBackends)`. Ranking (FR-023): shortest remaining TTL → transient (429/5xx) before quota (403) before Quarantine/401 → lower budget usage → lower latency → fewer consec failures. Anti-thrash (FR-024..FR-026): a promoted candidate gets a `probeShieldUntil = now+60s` during which it cannot be re-isolated; a `lastFailsafeAt` timestamp adds hysteresis so the failsafe won't re-run within a short window; candidate selection rotates by fewest-failures.

**Rationale**: Encodes FR-021..FR-026 and the spec's "防抖三条". Prevents the promote→mass-fail→drop→re-promote spin.

---

## R11. Testability of time-dependent logic

**Decision**: Route all "now" reads in the balancer through a single `nowFn func() time.Time` field (defaults to `time.Now`), and make jitter use an injectable `rand` source. Tests set `nowFn` to advance time deterministically and seed a fixed rand to assert TTL/backoff/jitter bounds.

**Rationale**: TTL expiry, 5-minute windows, and jitter are otherwise untestable without sleeps. Matches the existing table-driven style in `balancer_test.go`.

---

## Resolved parameters (defaults, from spec §11)

| Param | Value |
|---|---|
| 429 base / cap TTL | 30s / 5min (Retry-After overrides) |
| 403 base / cap TTL | 2min / 30min (quota → daily reset) |
| 401 TTL | 15–30min (Quarantine + alert) |
| 5xx base / cap TTL | 30s / 5min |
| Backoff multiplier | ×2 per failed probe |
| Consec-failure → Quarantine | ≥ 5 |
| TTL jitter | ±20% |
| Probing weight | 1 |
| Degraded weight factor | 0.2–0.5 (default 0.3) |
| Budget soft-decay start | 80% → 0.3 @95% → 0.05 @99%, floor 0.05 |
| Latency factor | 8000/latency when >8s, floor 0.3 |
| Consec decay window | 5min clean |
| Failsafe trigger / target | routable <3 / ≥ min(7, total) |
| Failsafe probe shield | 60s |
| Reconcile period | 60s |

All parameters are code constants by default; exposing them via `config` is optional (Assumptions).
