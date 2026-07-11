# Phase 0 Research: Backend Health Hardening

This resolves the constants and design decisions deferred by the spec's Assumptions. Each entry: **Decision / Rationale / Alternatives**. FR references map back to `spec.md`.

## R1 — Event-driven failsafe trigger & recovery target (US1, FR-001..004)

**Decision**: Add two synchronous entry points that call the existing `failsafe(now)` logic, keeping the 60s tick as a backstop:
1. At the end of `setIsolated` / `setQuarantine` (i.e., every transition into a non-routable state), after releasing the state write, check `routableCount() < failsafeTrigger` and if so invoke `failsafe(now)`.
2. In `pick(...)`, if the SWRR scan finds `best == nil`, invoke `failsafe(now)` once and re-scan a single time before returning nil.
Keep `failsafeTrigger = 3` and `failsafeTargetCap = 7`; the failsafe already promotes in one pass up to `min(failsafeTargetCap, total)` (loop in `failsafe`), so the "recover to ≥7 in one invocation" behavior already exists — the review's open question is answered: it is one-shot, not one-per-cycle. `lastFailsafeAt` hysteresis (30s) prevents thrash across the new synchronous callers and the tick.

**Rationale**: Closes the ~60s availability window with no new goroutine or lock — reuses `failsafe`, which is already a pure pass over `lb.backends` under `lb.mu`. The `pick` path already holds `lb.mu.RLock`; the synchronous failsafe there needs the promotion writes to use atomics (they do: `setState`, `ttlUntil.Store`, `probeShieldUntil.Store` are all atomic), so it is safe under RLock without upgrading to a write lock.

**Alternatives**: (a) A dedicated fast-tick (5s) reconcile — rejected: still polling, just finer; wastes cycles when healthy. (b) A condition-variable woken on transition — rejected: more machinery than the direct call, and the hysteresis field already serializes runs.

## R2 — Probe result carries observed code + Retry-After (US2, FR-005..007)

**Decision**: Extend the health payload `{name, healthy, latency_ms, error}` with optional `observed_code int` and `retry_after string` (raw header value). `SetHealthStatus` becomes `SetHealthStatusDetailed(name, healthy, latencyMs, observedCode, retryAfterUnix)`; the unhealthy branch calls `lb.setIsolated(b, observedCode, retryAfterUnix, quotaBody=false, now)` instead of passing `b.lastHTTPCode.Load()`. When `observedCode == 0` (transport failure / older probe), fall back to the server-class band (`ttlBase5xx`), which is today's behavior. `cmd/check` parses `Retry-After` from the probe response (reusing `parseRetryAfter` moved to a shared spot) and sets `observed_code` from the probe's HTTP status.

**Rationale**: One `computeTTL` policy for both passive and active paths (the review's explicit ask). Optional fields keep the endpoint backward-compatible with an un-upgraded `check` binary.

**Alternatives**: Have the server re-derive the code by re-probing — rejected: double the upstream load, defeats the purpose.

## R3 — No billable probe for quota-isolated; minimal probe; cost attribution (US3, FR-008..010)

**Decision**: Three changes in `cmd/check` health mode:
1. Before probing, ask the gateway which backends are quota-isolated (reuse the `/admin/api/backends` list already exposed via `GetBackends`; skip any whose `state == "isolated"` **and** (`quota_exhausted` true or `last_http_code == 403`)). Those are reported `healthy=false` **without** any probe, so they recover only via daily-reset / `releaseExpired`.
2. The `/v1/messages` fallback probe uses `max_tokens: 1` (down from 16).
3. The probe's estimated cost (input+output tokens × the backend's configured rate, or a flat floor if unknown) is emitted to the gateway so it lands in `usage_logs` / the budget accounting. Simplest: `check` posts a synthetic usage record via a new field on the health payload (`probe_cost_usd`), and the health endpoint forwards it to `stats.Collector.Emit` with `Model="__probe__"`, `StatusCode` of the probe, and `ErrorReason="probe"`.

**Rationale**: Directly stops "越探越超". `max_tokens=1` is the floor Anthropic-style APIs accept for a well-formed message. Emitting probe cost via the existing collector keeps budget accounting closed without a new table.

**Alternatives**: (a) Drop the message fallback entirely — rejected: US4 depends on it for listing-less backends. (b) Track probe cost only in memory — rejected: not durable, can't calibrate later.

## R4 — Unify startup validation with runtime probe fallback (US4, FR-011..012)

**Decision**: Extract the runtime probe's two-step logic (`/v1/models`, then `/v1/messages` fallback with `healthCheckModel`) into a shared function used by both `cmd/check`'s `runHealthProbe` and `balancer.go`'s `validateBackend`. `validateBackend` currently only does `/v1/models`; it will call the shared "reachable?" routine so a backend that serves inference but not listing is **not** marked `validationFailed`. Keep `validationFailed` as a permanent gate only for backends that fail *both* checks at startup.

**Rationale**: Removes the silent-capacity-loss disagreement between the two code paths. The shared routine lives in one place (proposed: `cmd/check` logic moved to an importable helper, or duplicated minimally in `balancer.go` since `cmd/check` can't be imported by `internal/proxy`). Chosen: put the reachability probe in `internal/proxy` (e.g., `probe.go`) and have `cmd/check` call it — `internal/proxy` is importable, `cmd/check` is not.

**Alternatives**: Make startup validation non-permanent (retry via reconcile) — rejected as the *primary* fix (it masks the real issue) but adopted as a secondary safety net: `validationFailed` backends are re-validated once per hour by the reconcile loop, so a transient startup failure self-heals.

## R5 — Per-backend quota reset instead of hardcoded 00:00 CST (US5, FR-013..014)

**Decision**: Default policy = **treat quota isolation as a long TTL**, not a wall-clock lock. When a backend is quota-isolated (403-quota body or `MarkQuotaExhausted`), set `ttlUntil = now + quotaIsolationTTL` where `quotaIsolationTTL` defaults to 6h (jittered ±20%), and let `releaseExpired` → probing discover recovery. Optionally, `config.BackendAPI` gains `quota_reset` (one of `"none"` (default, long-TTL), `"cst-midnight"`, `"utc-midnight"`, or an explicit `"HH:MM±TZ"`); when set, the reset instant is computed per-backend instead of the long TTL. The existing global `quotaResetLoop` (00:00 CST clears `quotaExhausted`) is kept only as a safety clear for backends configured `cst-midnight` or left at the legacy default during migration.

**Rationale**: Cross-provider correctness without forcing every deployment to configure timezones. Long-TTL + probe is self-correcting and consistent with "the daily limit is only an estimate." Per-backend override covers providers with a known hard reset.

**Alternatives**: Keep a single global clock but make it configurable — rejected: still wrong for mixed fleets. Trust only probes with no TTL floor — rejected: would hammer a truly-exhausted backend every reconcile.

## R6 — Model-scoped error validity for Claude backends (US6, FR-015..017)

**Decision**: Before the handler calls `RecordResult*` on a non-2xx, gate on the request's model family. Reuse the existing `reqModel` (already parsed at `handler.go:165`) and a new `isValidHealthModel(model)` that returns true only when the lowercased name contains `sonnet`, `haiku`, or `opus`. If the backend is a Claude backend (its traffic is Anthropic-style — treat all backends in this gateway as Claude backends for v1) and the model is **not** a valid-health model, skip the health-impacting `RecordResult` (still record the raw request in the ring buffer / usage log, but pass `ErrClient`-equivalent "ignored" semantics so state/consec/TTL are untouched). When `reqModel` is empty/undeterminable, **default to treating the error as valid** (count it) — chosen over "ignore" because ignoring on unknown risks keeping a genuinely broken backend routable (avoids silent capacity *retention* of a bad node); this is the FR-017 documented default.

**Rationale**: Matches the user's explicit rule — only sonnet/haiku/opus errors say something about a Claude backend's health. `isClaudeModel` already exists and already keys on exactly these three family words plus "claude"; the new helper is the stricter subset (drops the bare "claude" match so a generic/unknown "claude-*" alias still counts, avoiding a gap).

**Alternatives**: Default-ignore on unknown model — rejected (see above). Maintain an explicit model allowlist in config — deferred; the substring rule covers current traffic.

## R7 — Windowed error-rate signal for flapping backends (US7, FR-018..019)

**Decision**: Add a windowed degrade/isolate trigger evaluated in `softUpDown` (reconcile) using the existing ring buffer via `recentCodes(now)`: compute the impacting-error rate over the last ≤20 entries within 5 min. If `sampleCount >= 8` and `errorRate >= 0.5`, force the backend to at least `degraded`; if `errorRate >= 0.8`, `setIsolated` with the server band. Recovery: when a probing/degraded backend's windowed rate falls below 0.3, allow the normal gated progression. This is independent of `consecErr`, so an alternating node is caught.

**Rationale**: A rate signal over a min-sample window is the standard fix for flappers and reuses data already recorded. Thresholds (0.5 degrade / 0.8 isolate / 0.3 recover, min 8 samples) are conservative defaults, tunable later.

**Alternatives**: EWMA of error indicator — mathematically nicer but needs a new per-backend float and decay tuning; rejected for v1 in favor of the ring-buffer window already present.

## R8 — Auth-quarantine exponential backoff (US8, FR-020..021)

**Decision**: Add `authQuarantineCount atomic.Int64` per backend. On each auth (401) quarantine, `ttl = min(ttlBase401 * 2^authQuarantineCount, authQuarantineMaxTTL)` with `authQuarantineMaxTTL = 24h`, and increment the count. A successful probe/request (2xx) resets the count to 0. `releaseExpired` still promotes to probing at TTL expiry, but the grown TTL means an auth-dead node is retried at 15m, 30m, 1h, 2h, … not every 15m. FR-021: a new admin signal `POST /admin/api/backends/health` with an explicit `key_rotated: true` (or reuse `/quota` enable) resets `authQuarantineCount` and TTL so a rotated key is retried immediately.

**Rationale**: Revoked keys don't self-heal; exponential backoff caps wasted probes while still allowing eventual recovery, and the rotation signal gives an instant path when ops fixes the key.

**Alternatives**: Never re-probe auth quarantine (require manual re-enable) — rejected: too operationally heavy for 40+ nodes.

## R9 — Bounded, budget-aware failover chain (US9, FR-022..024)

**Decision**: In `handler.go`'s failover loop, (a) capture `chainDeadline = start + failoverBudget` where `failoverBudget = 20s`; stop failing over once `time.Now() > chainDeadline`. (b) Track a package-level short-window quota-report counter; if `>= quotaCascadeThreshold (3)` backends reported quota within the last `cascadeWindow (10s)`, fast-fail the current request instead of walking further. (c) `PickExcluding` already exists; add `PickExcludingPreferBudget` that, among routable non-excluded backends, biases toward the lowest `budgetFactor`-adjusted utilization (it already does via SWRR effective weight, which folds in `budgetFactor` — so the existing `PickExcluding` mostly satisfies this; the change is to confirm the failover uses `PickExcluding` (it does) and to add the cascade fast-fail + deadline).

**Rationale**: Caps tail latency (`maxQuotaFailovers=2` × ~15s ≈ 45s today → bounded to 20s) and stops adding load during a cascade. Budget-aware selection largely falls out of the existing SWRR weight.

**Alternatives**: Per-attempt timeout only — rejected: doesn't bound the *chain*. Remove failover under cascade entirely — the fast-fail achieves this selectively.

## R10 — Retry-After clamp (US10, FR-025..026)

**Decision**: In `computeTTL` (and `parseRetryAfter` consumers), clamp the honored Retry-After to `[retryAfterMin=5s, retryAfterMax=30min]`. A value ≤0 or unparseable → the code's base TTL (existing behavior). A value above 30min → 30min.

**Rationale**: One header can no longer remove a node for 24h; a near-zero value can't cause a hot re-probe loop. 30min aligns with the 401/403 caps already in the TTL table.

**Alternatives**: No upper clamp (fully trust upstream) — rejected: the review's exact concern. Cap at the per-code `cap` — viable, but a flat 30min is simpler and documented.

## R11 — Degraded state reachability (US11, FR-027)

**Decision**: Document that, after 002, `degraded` is entered via (1) probe recovery (`probing → degraded`), (2) the new windowed-rate degrade (R7), and (3) `SetHealthStatus(false)` on a currently-healthy node. The passive `consecErrToDegrade=3` path is confirmed **unreachable from live traffic** (first impacting error isolates). Resolution: make it reachable by treating the *first* few impacting errors of a **non-quota, non-auth transient** class (429/5xx/transport) as `degraded` until `consecErrToDegrade`, then `isolated` — i.e., a soft first-strike for transient blips, reserving immediate isolation for 403/401. This gives `degraded` a real passive trigger and reduces over-eager isolation on a single 500.

**Rationale**: Aligns behavior with the documented design and reduces churn from one-off 5xx. Keeps 403/401 fast-isolating (those are not transient).

**Alternatives**: Document `degraded` as recovery-only and delete `consecErrToDegrade` — simpler but discards a useful soft state; rejected in favor of making it reachable.

## R12 — Observability: counters, metrics, error-reason column (US12, FR-028..030)

**Decision**:
- Split counters: keep `consecErr` (consecutive) and add `totalErr atomic.Int64` (cumulative, never reset except on process restart). `BackendInfo.ErrCount` → `totalErr`; `ConsecFailures` → `consecErr` (fixes the current double-report).
- Per-backend metrics (in-memory atomics, exposed via new `GET /admin/api/backends/metrics`): `isolationCountByCode map[int]int64` (guarded by a small mutex), `failsafePromotions int64`, `probeCostUSD float64`-as-cents `int64`, and per-state dwell accumulated on each `setState` (`dwellByState [6]int64` seconds).
- `first403`: on the first quota-indicating 403 for a backend, record `estimatedSpendAtFirst403` (current `quotaUsage`) and `configuredLimit` (`quotaLimit`/`daily_limit`) once, exposed in metrics for calibration.
- **Error-reason column** (the user's clarify request): add `error_reason TEXT NOT NULL DEFAULT ''` to `usage_logs`. On any non-2xx outcome, `handler` sets `Record.ErrorReason` to a **compact classified reason code** (≤32 bytes) from a new `reasonCode(class, httpCode)` mapping — e.g. `rate_limit`, `auth_401`, `forbidden_403`, `quota_403`, `server_5xx`, `transport`, `canceled`, `client_4xx`. This is groupable in SQL for later analysis; raw upstream text is intentionally **not** stored (a 32-byte truncation of a JSON error body is unusable and risks leaking fragments).

**Rationale**: The clarify question ("是否记录出错原因，建议数据库增加 32 字节错误信息") is satisfied by a classified code that fits 32 bytes and aggregates cleanly (`SELECT error_reason, COUNT(*) FROM usage_logs WHERE status_code >= 400 GROUP BY error_reason`). Metrics close the calibration loop for `budgetFactor`.

**Alternatives**: Store truncated raw upstream message — rejected: unusable at 32 bytes, PII/secret leakage risk. Store both code + long raw column — deferred; can be added later if a raw sample is needed (would be a separate wider column, not the 32-byte field). This was the recommended clarify option A; the user proceeded to plan without selecting, so option A is adopted and recorded here.

## Resolved constants (defaults; all tunable, default-in-code)

| Constant | Value | FR |
|---|---|---|
| failsafeTrigger / failsafeTargetCap | 3 / 7 (unchanged) | FR-001..003 |
| quotaIsolationTTL (default policy) | 6h ±20% | FR-013..014 |
| windowed: minSamples / degrade / isolate / recover | 8 / 0.5 / 0.8 / 0.3 | FR-018..019 |
| authQuarantineMaxTTL | 24h (exp from ttlBase401=15m) | FR-020 |
| failoverBudget (chain deadline) | 20s | FR-022 |
| quotaCascadeThreshold / cascadeWindow | 3 / 10s | FR-023 |
| retryAfterMin / retryAfterMax | 5s / 30min | FR-025..026 |
| error_reason column width | 32 bytes (classified code) | FR-030 |

**Output**: All spec Assumptions resolved; no NEEDS CLARIFICATION remain for design.
