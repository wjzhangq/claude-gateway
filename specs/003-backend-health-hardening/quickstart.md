# Quickstart / Validation Guide: Backend Health Hardening

Prerequisites: Go 1.24, repo at `/Users/wj/Downloads/work/claude-gateway`, a runnable `config.yaml`. All logic is unit-testable with the injected clock (`lb.nowFn`) and rng (`lb.rng`) — most validation is `go test`, not a live fleet.

## Build & test

```bash
go build ./...
go test ./internal/proxy/... ./internal/stats/... ./internal/db/...
```

Expected: all pass, including new cases in `balancer_test.go`, `state_test.go`, and handler tests.

## Per-user-story validation

### US1 — event-driven failsafe (FR-001..004)
Unit: isolate enough backends in one step to drop `routableCount()` below `failsafeTrigger`; assert (without advancing the clock past a tick) that `routableCount()` is immediately restored to `min(failsafeTargetCap, total)`, and that `pick()` returns non-nil when it would otherwise be nil.

### US2 — probe carries observed code (FR-005..007)
Unit: `SetHealthStatusDetailed("b", false, 10, 429, now+120)` → backend isolated with TTL honoring Retry-After (≈120s, clamped), NOT the 30s server band. `observed_code=0` → server band.

### US3 — no billable probe for quota-isolated (FR-008..010)
Manual: mark a backend quota-isolated, run `./check --health`, confirm logs show it skipped with no `/v1/messages` call; confirm the fallback probe elsewhere uses `max_tokens=1`; confirm a `__probe__` row appears in `usage_logs` with the probe cost.

### US4 — startup validation uses fallback (FR-011..012)
Manual/unit: point a backend at a stub that 404s `/v1/models` but serves `/v1/messages`; start the gateway; confirm it is NOT `validationFailed` and becomes routable.

### US5 — quota reset not hardcoded (FR-013..014)
Unit: quota-isolate a backend with default config → `ttlUntil ≈ now + 6h` (jittered), released via `releaseExpired`. With `QuotaReset="utc-midnight"` → TTL reflects UTC midnight, not CST.

### US6 — model-scoped error validity (FR-015..017)
Unit (handler): a 500 on `model=gpt-4o` (non sonnet/haiku/opus) → backend state/consecErr/ttl UNCHANGED. A 500 on `claude-sonnet-4-6` → normal isolation. Empty model → counted.

### US7 — windowed flapper (FR-018..019)
Unit: feed an alternating 200/500 pattern (≥8 samples, rate ≥0.5) → backend settles to Degraded/Isolated instead of oscillating through Healthy.

### US8 — auth backoff (FR-020..021)
Unit: quarantine on 401 repeatedly → TTL grows 15m → 30m → 1h … capped at 24h. A `key_rotated` signal (or 2xx) resets the count.

### US9 — bounded failover (FR-022..024)
Unit/manual: simulate ≥3 quota reports within 10s → next request fast-fails (no further failover). Confirm cumulative failover time never exceeds the 20s chain deadline.

### US10 — Retry-After clamp (FR-025..026)
Unit: `parseRetryAfter/computeTTL` with `Retry-After: 86400` → isolation capped at 30min; `0`/negative → code base TTL (floored at 5s when honored).

### US11 — degraded reachable (FR-027)
Unit: a single 500 while Healthy → Degraded (not immediate Isolated); reaching `consecErrToDegrade` transient errors → Isolated. Enumerate and assert every documented transition into Degraded.

### US12 — observability + error_reason (FR-028..030)
- Unit: `BackendInfo.ErrCount` (cumulative) diverges from `ConsecFailures` after a recovery. `Metrics()` reports isolation-by-code, failsafe promotions, probe cost, dwell, first-403 snapshot.
- DB: run migrations, confirm `usage_logs.error_reason` exists; drive a 429 and a 500 through the handler and confirm rows carry `rate_limit` / `server_5xx`; a 2xx carries `''`.

```bash
sqlite3 <db> "SELECT error_reason, COUNT(*) FROM usage_logs WHERE status_code>=400 GROUP BY error_reason;"
```

## Regression

```bash
go test ./...   # full suite; no behavior change to proxied request/response bytes
```

Confirm the existing 002 tests still pass (state machine, SWRR, TTL, reconcile) — this feature extends, not replaces.
