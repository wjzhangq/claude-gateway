# Phase 1 Data Model: Backend Health Hardening

All state remains in memory on `Backend`/`LoadBalancer` (as in 002) except one additive persisted column. New fields are `atomic.*` unless noted.

## Backend (internal/proxy/balancer.go) — additions

| Field | Type | Purpose | FR |
|---|---|---|---|
| `totalErr` | `atomic.Int64` | Cumulative impacting-error count; never reset at runtime. Distinct from `consecErr`. | FR-028 |
| `authQuarantineCount` | `atomic.Int64` | Number of consecutive auth (401) quarantines; drives exponential TTL. Reset to 0 on any 2xx or key-rotation signal. | FR-020, FR-021 |
| `isolationCountByCode` | `map[int]int64` + `metricsMu sync.Mutex` | Per-code isolation tally for metrics. | FR-029 |
| `failsafePromotions` | `atomic.Int64` | Times this backend was force-promoted by the failsafe. | FR-029 |
| `probeCostCents` | `atomic.Int64` | Accumulated estimated inference-probe cost (cents). | FR-010, FR-029 |
| `dwellByState` | `[6]atomic.Int64` | Accumulated seconds spent in each state; updated on `setState` (add `now-enteredAt` to the outgoing state's bucket). | FR-029 |
| `estSpendAtFirst403Cents` | `atomic.Int64` | `quotaUsage` snapshot at first quota-403 (0 = not yet seen). | FR-030 |
| `limitAtFirst403Cents` | `atomic.Int64` | `quotaLimit`/`daily_limit` snapshot at first quota-403. | FR-030 |

### State transition additions (see spec state machine + research R7/R11)

- **Passive transient first-strike (R11, FR-027)**: on 429/5xx/transport while `Healthy`, go to `Degraded` and `consecErr++`; only isolate once `consecErr >= consecErrToDegrade`. 403/401 still isolate/quarantine immediately.
- **Windowed degrade/isolate (R7, FR-018)**: in `softUpDown`, if windowed impacting-rate over ≥8 samples ≥0.5 → `Degraded`; ≥0.8 → `Isolated`. Recover path allowed when rate <0.3.
- **Auth exponential TTL (R8, FR-020)**: `setQuarantine` for code 401 computes `ttl = min(ttlBase401 << authQuarantineCount, authQuarantineMaxTTL)`.
- **Quota isolation (R5, FR-013)**: quota-403 / `MarkQuotaExhausted` sets `ttlUntil = now + jitter(quotaIsolationTTL)` (default policy) instead of `secondsUntilCSTMidnight`.

## LoadBalancer (internal/proxy/balancer.go) — additions

| Field | Type | Purpose | FR |
|---|---|---|---|
| `quotaReportTimes` | ring/slice of unix-s + `sync.Mutex` | Timestamps of recent quota reports for cascade fast-fail. | FR-023 |

New/changed methods: `SetHealthStatusDetailed(name, healthy, latencyMs, observedCode int, retryAfterUnix int64) bool`; `Metrics() []BackendMetrics`; event-driven `failsafe` invocation from `setIsolated`/`setQuarantine`/`pick`; `noteQuotaReport(now)` + `quotaCascadeActive(now) bool`.

## ProbeResult / health payload (cmd/check ↔ /admin/api/backends/health)

| Field (JSON) | Type | Notes | FR |
|---|---|---|---|
| `name` | string | existing | |
| `healthy` | bool | existing | |
| `latency_ms` | int64 | existing | |
| `error` | string | existing, optional | |
| `observed_code` | int | NEW, optional — HTTP status the probe saw (0 = transport/unknown) | FR-005 |
| `retry_after` | string | NEW, optional — raw Retry-After header from the probe response | FR-005 |
| `probe_cost_usd` | float64 | NEW, optional — estimated cost of an inference probe, attributed to the backend | FR-010 |
| `key_rotated` | bool | NEW, optional — reset auth-quarantine backoff for this backend | FR-021 |

Backward-compat: all NEW fields optional; absent → today's behavior (fall back to server-band TTL, no cost, no reset).

## BackendMetrics (new, /admin/api/backends/metrics response element)

| Field | Type | Source |
|---|---|---|
| `name` | string | Backend.Name |
| `total_err` | int64 | totalErr |
| `consec_err` | int64 | consecErr |
| `isolation_count_by_code` | map[string]int64 | isolationCountByCode |
| `failsafe_promotions` | int64 | failsafePromotions |
| `probe_cost_usd` | float64 | probeCostCents/100 |
| `dwell_seconds_by_state` | map[string]int64 | dwellByState keyed by state name |
| `est_spend_at_first_403_usd` | float64 | estSpendAtFirst403Cents/100 |
| `limit_at_first_403_usd` | float64 | limitAtFirst403Cents/100 |

## UsageLog / stats.Record — additions (persisted)

| Layer | Field | Type / column | Notes | FR |
|---|---|---|---|---|
| `internal/model.UsageLog` | `ErrorReason` | `string` `db:"error_reason"` | classified reason code | FR-030 |
| `internal/stats.Record` | `ErrorReason` | `string` | set by handler on non-2xx | FR-030 |
| DB `usage_logs` | `error_reason` | `TEXT NOT NULL DEFAULT ''` | migration `{36}` + CREATE + INSERT column list | FR-030 |

### error_reason vocabulary (≤32 bytes, classified — see contracts/error-reason.md)

`""` (2xx / no error), `client_4xx`, `auth_401`, `forbidden_403`, `quota_403`, `rate_limit`, `server_5xx`, `transport`, `canceled`, `probe`, `unknown`.

## BackendAPI config (config/config.go) — optional additions

| Field | Type | Default | Purpose | FR |
|---|---|---|---|---|
| `QuotaReset` | string | `""` (→ long-TTL policy) | `"cst-midnight"` / `"utc-midnight"` / `"HH:MM±HHMM"` per-backend quota reset | FR-014 |

Tunable thresholds (research R-table) are `const` defaults in `balancer.go`; exposing them via config is optional and out of scope for v1 unless trivial.
