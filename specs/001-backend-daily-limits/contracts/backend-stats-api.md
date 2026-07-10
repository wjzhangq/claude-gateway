# Contract: Backend Stats API (extended)

## GET /admin/api/backends/stats

Admin-authenticated. Existing endpoint (`internal/handler/stats.go:GetBackendStats`), extended with daily-limit fields. No new endpoint or route is added.

### Request (unchanged)

| Query param | Type | Notes |
|-------------|------|-------|
| start_date | string `YYYY-MM-DD` | Selected day (UI passes the same value for start and end) |
| end_date | string `YYYY-MM-DD` | Same as start_date for a single day |

### Response (extended)

```json
{
  "stats": [
    {
      "backend": "alpha",
      "requests": 1200,
      "total_tokens": 3450000,
      "cost_usd": 42.0,
      "avg_latency_ms": 830,
      "error_count": 3,
      "daily_limit": 100.0,
      "used_pct": 42.0
    },
    {
      "backend": "gamma",
      "requests": 10,
      "total_tokens": 5000,
      "cost_usd": 1.2,
      "avg_latency_ms": 700,
      "error_count": 0,
      "daily_limit": 0,
      "used_pct": null
    }
  ],
  "summary": {
    "daily_limit_total": 150.0,
    "used_total": 43.2,
    "used_pct": 28.8,
    "has_unlimited": true
  }
}
```

### Field rules

- `daily_limit`: configured USD cap for the backend; `0` means unlimited.
- `used_pct`: `null` (or omitted) when `daily_limit <= 0`; otherwise `cost_usd / daily_limit * 100`, may exceed 100.
- `summary.daily_limit_total`: sum of positive caps only.
- `summary.used_total`: sum of `cost_usd` across all backends (capped + uncapped).
- `summary.has_unlimited`: `true` when ≥1 active backend has no positive cap; UI renders the ceiling as partial (e.g. `$150+`).

### Backward compatibility

Existing consumers that ignore the new fields are unaffected. `public*` backends continue to be filtered client-side as today.

## GET /admin/api/config/limits (extended, read-only)

Response gains a `backend_daily_limits` array (read surfacing only; not editable via `PUT /config/limits` in this feature):

```json
{
  "backend_daily_max": 0,
  "aws_daily_max": 0,
  "aws_monthly_max": 0,
  "user_daily_limits": [],
  "backend_daily_limits": [
    { "name": "alpha", "daily_usd": 100 },
    { "name": "beta", "daily_usd": 50 }
  ]
}
```
