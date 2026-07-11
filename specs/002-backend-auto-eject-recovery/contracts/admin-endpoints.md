# Contract: Admin sync endpoints (gateway ← cmd/check)

These HTTP endpoints already exist and are wired in `cmd/server/main.go`; `cmd/check` posts to them. This feature **reuses** them as the external recovery/quota signal. No new endpoints are required; only the server-side handler behavior changes (which balancer method it calls / how state transitions).

Auth: `Authorization: Bearer <config.Auth.SessionSecret>` on all admin sync calls.

## POST `/admin/api/backends/health`

Posted by `check --health` every minute. (FR-035)

Request body:
```json
{ "backends": [ { "name": "sc030", "healthy": true, "latency_ms": 1320, "error": "" } ] }
```

Server behavior: for each entry calls `lb.SetHealthStatus(name, healthy, latency_ms)`.
- **Behavior change**: `healthy=true` now promotes Isolated/Quarantine → **Probing** (previously disabled→degraded); `healthy=false` demotes per the 5-state machine. (FR-016, FR-035)
- Response: `{ "updated": <count> }` (unchanged shape).

## POST `/admin/api/backends/quota`

Posted by `check` (default billing mode) and `check --enable/--disable`.

Request body:
```json
{ "backends": [ { "name": "sc030", "exhausted": false, "limit": 400.0, "usage": 412.5 } ] }
```

Server behavior: for each entry calls `lb.SetQuotaStatus(name, exhausted, limit, usage)`.
- **Behavior change**: `limit`/`usage` now feed the **soft budget factor** (0.05 floor, never 0, never a state change). `exhausted=true` no longer forces the backend non-routable; it only biases weight down. Hard exit remains the provider's real 403/429 on the request path. (FR-009, P1, FR-028)
- Response: `{ "updated": <count> }` (unchanged shape).

## GET `/admin/api/backends` (status read)

Returns `[]BackendInfo`. Extended fields (additive, back-compatible): `state` string now includes `isolated|probing|quarantine`; new `ttl_until`, `consec_failures`, `last_http_code`, optional factor fields. Existing `disabled` bool stays truthful (`effective_weight==0`). (FR-036, FR-037, R7)

## Notes

- `cmd/check` itself needs no contract change to deliver recovery; the `--health` cron is the recovery driver. Optionally it may forward a parsed `Retry-After` hint, but Retry-After is authoritative only from the live request path (see [balancer-api.md](./balancer-api.md) R5), so this is not required for v1.
