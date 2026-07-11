# Contract: error_reason column + reason-code vocabulary

## Storage

`usage_logs.error_reason TEXT NOT NULL DEFAULT ''` — added by migration `{36}`:

```sql
ALTER TABLE usage_logs ADD COLUMN error_reason TEXT NOT NULL DEFAULT '';
```

Also add `error_reason` to the `usage_logs` CREATE statement (for fresh DBs) and to the column list in `BatchInsertUsageLogs` (internal/db/stats.go). `internal/model.UsageLog` gains `ErrorReason string \`db:"error_reason" json:"error_reason"\``; `internal/stats.Record` gains `ErrorReason string`; `recordToLog` maps it.

## reason-code vocabulary (all ≤32 bytes)

| Code | When |
|---|---|
| `""` | 2xx success / no error (default; most rows) |
| `client_4xx` | 400/404/422 and other client 4xx (caller fault) |
| `auth_401` | 401 (backend key invalid) |
| `forbidden_403` | 403 without a quota-indicating body |
| `quota_403` | 403 whose body indicates quota/balance exhaustion |
| `rate_limit` | 429 |
| `server_5xx` | any 5xx |
| `transport` | connection-level failure (no HTTP status) |
| `canceled` | client aborted the request |
| `probe` | synthetic record from an active health probe |
| `unknown` | non-2xx that matched no rule above |

## Producer (handler)

```go
// reasonCode maps an ErrorClass + HTTP status to a compact code.
func reasonCode(class ErrorClass, httpCode int, quotaBody bool) string
```

Set `Record.ErrorReason = reasonCode(...)` on every emitted record (empty for 2xx). Raw upstream error text is intentionally NOT stored — a 32-byte truncation of a JSON error body is unusable for aggregation and risks leaking secret/PII fragments.

## Consumer (analysis)

```sql
SELECT error_reason, COUNT(*) AS n
FROM usage_logs
WHERE status_code >= 400 AND created_at >= date('now','-1 day')
GROUP BY error_reason
ORDER BY n DESC;

-- per-backend error breakdown
SELECT backend, error_reason, COUNT(*) n
FROM usage_logs
WHERE status_code >= 400
GROUP BY backend, error_reason
ORDER BY backend, n DESC;
```

## Compatibility

- Column defaults to `''`; existing rows and any un-upgraded writer path are unaffected.
- No index required for v1 (analysis queries are ad-hoc / low-frequency); add a composite index later if needed.
