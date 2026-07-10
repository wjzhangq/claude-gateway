# Phase 1 Data Model: Backend Daily Cost Limits

## Config entity: BackendDailyLimit (new)

Location: `config/config.go`, added to `Config.BackendDailyLimits []BackendDailyLimit`.

| Field | Type | YAML key | Rules |
|-------|------|----------|-------|
| Name | string | `name` | Should match a `backends[].name`; unmatched names are ignored for aggregation (harmless) |
| DailyUSD | float64 | `daily_usd` | `0` or absent → unlimited (FR-002); negative treated as `0` |

YAML example:

```yaml
backend_daily_limits:
  - name: alpha
    daily_usd: 100
  - name: beta
    daily_usd: 50
  # backends not listed here → unlimited
```

Helper: `func (c *Config) LookupBackendDailyLimit(name string) float64` → returns configured cap, or `0` if not found / not positive.

## Derived: per-backend response fields (existing BackendStat, extended)

The `GetBackendStats` response gains, per backend row:

| Field | Type | Meaning |
|-------|------|---------|
| daily_limit | float64 | Configured cap for this backend; `0` = unlimited |
| used_pct | float64 \| null | `cost_usd / daily_limit * 100`; `null`/omitted when `daily_limit <= 0` (FR-008, FR-010) |

Existing fields reused unchanged: `backend`, `requests`, `total_tokens`, `cost_usd`, `avg_latency_ms`, `error_count`.

## Derived: fleet summary (new top-level object in response)

| Field | Type | Meaning |
|-------|------|---------|
| daily_limit_total | float64 | Sum of `daily_usd` over all configured (positive) caps (FR-004) |
| used_total | float64 | Sum of `cost_usd` over all backends for the selected day, including uncapped ones (FR-005) |
| used_pct | float64 | `used_total / daily_limit_total * 100` when total > 0, else `0` |
| has_unlimited | bool | True if ≥1 active backend has no positive cap → summary shown as partial (FR-009) |

## Threshold color tiers (frontend, shared helper)

| Tier | Condition | Intent |
|------|-----------|--------|
| healthy | pct < 70 | green |
| warn | 70 ≤ pct < 90 | amber |
| critical | 90 ≤ pct ≤ 100 | red |
| over | pct > 100 | distinct deep-red overflow; visual fill clamped at 100%, true % labeled |
| none | cap ≤ 0 | neutral `无上限` chip, no bar/badge |

Applies identically to the fleet bar fill and per-row badge (FR-006, FR-013).

## Validation rules

- `daily_usd <= 0` → unlimited; never produces a percentage (guards div-by-zero, FR-010).
- Config-listed name with no matching active backend → excluded from `daily_limit_total`, not rendered (edge case).
- Usage for an uncapped backend → still added to `used_total` (edge case).
- Selected date ≠ today → all figures computed against that day's usage vs. current caps (edge case).

## State transitions

None. All values are read-derived per request; no persisted feature state.
