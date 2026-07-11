# Contract: Admin Endpoints (delta over 002)

All under `/admin/api/backends`, bearer-auth with `Auth.SessionSecret` (as today).

## POST /admin/api/backends/health (extended)

Request body (new optional fields; older `check` binaries omit them):

```json
{
  "backends": [
    {
      "name": "aws-1",
      "healthy": false,
      "latency_ms": 1234,
      "error": "HTTP 429",
      "observed_code": 429,
      "retry_after": "120",
      "probe_cost_usd": 0.0001,
      "key_rotated": false
    }
  ]
}
```

Server behavior:
- Route each entry through `SetHealthStatusDetailed(name, healthy, latency_ms, observed_code, parseRetryAfter(retry_after))` so probe-driven isolation uses the shared `computeTTL` (honors Retry-After, clamped).
- `probe_cost_usd > 0` → forward to `stats.Collector.Emit` as a synthetic record (`Model="__probe__"`, `StatusCode=observed_code`, `ErrorReason="probe"`, `CostUSD=probe_cost_usd`, `Backend=name`) so cost lands in `usage_logs` and budget accounting.
- `key_rotated=true` → reset `authQuarantineCount` and `ttlUntil` for the backend (eligible for immediate re-probe).
- Response unchanged shape: `{"updated": <n>}`.

## POST /admin/api/backends/quota (unchanged)

Behavior unchanged. Note: `check` no longer sends billable probes for backends the gateway reports as quota-isolated; it derives that from the existing backend list (see below) before probing.

## GET /admin/api/backends (unchanged shape, used by check for probe-skip)

`check --health` reads this list first and skips any backend where `state == "isolated"` AND (`quota_exhausted == true` OR `last_http_code == 403`) — those are reported `healthy=false` with **no** probe (FR-008).

## GET /admin/api/backends/metrics (NEW)

Read-only. Response:

```json
{
  "backends": [
    {
      "name": "aws-1",
      "total_err": 42,
      "consec_err": 0,
      "isolation_count_by_code": {"429": 5, "500": 2, "403": 1},
      "failsafe_promotions": 3,
      "probe_cost_usd": 0.0123,
      "dwell_seconds_by_state": {"healthy": 8000, "degraded": 120, "isolated": 300, "probing": 60, "quarantine": 0},
      "est_spend_at_first_403_usd": 47.80,
      "limit_at_first_403_usd": 50.00
    }
  ]
}
```

Purpose: operator diagnosis of the state machine and calibration of the budget estimate (`est_spend_at_first_403` vs `limit`). In-memory only; resets on process restart.
