# Quickstart: Backend Daily Cost Limits

Validates the feature end-to-end: configure caps → verify API → verify the `/admin/backends` page.

## Prerequisites

- Repo builds: `go build ./...`
- A running gateway with the admin UI (`web/`) and at least two backends configured with recent usage today.

## Setup: configure caps

Edit the gateway config YAML, adding a section (see [data-model.md](./data-model.md)):

```yaml
backend_daily_limits:
  - name: <backend-a-name>
    daily_usd: 100
  - name: <backend-b-name>
    daily_usd: 50
  # leave at least one backend unlisted to exercise the unlimited path
```

Reload config (restart or trigger the existing reload path). No code change required (FR-012).

## Validate: API

```bash
# adjust host/date; date should be today
curl -s 'http://localhost:8080/admin/api/backends/stats?start_date=2026-07-10&end_date=2026-07-10' \
  -H 'Cookie: <admin-session>' | jq '.summary, .stats[] | {backend, cost_usd, daily_limit, used_pct}'
```

Expected:
- Each capped backend row has `daily_limit > 0` and `used_pct == cost_usd/daily_limit*100`.
- The unlisted backend has `daily_limit == 0` and `used_pct == null`.
- `summary.daily_limit_total` equals the sum of the positive caps (e.g. `150`).
- `summary.used_total` equals the sum of all `cost_usd` (including the uncapped backend).
- `summary.has_unlimited == true` because a backend was left unlisted.

## Validate: /admin/backends page

Open `/admin/backends` and confirm (maps to spec Success Criteria):

1. A fleet budget bar shows `已用 $X / 上限 $Y (Z%)`; because an unlimited backend exists, the ceiling is shown as partial (e.g. `$150+`). — SC-001, FR-009
2. The bar fill color matches the tier: green <70%, amber 70–90%, red ≥90%. — SC-003, FR-006
3. Each capped backend row shows a colored percentage badge (e.g. `42%`) plus a muted `$42 / $100` label. — SC-002, FR-007
4. The unlisted backend shows a neutral `无上限` chip, visibly distinct from a 0%-used capped backend. — SC-004, FR-008
5. Pick a capped backend and push its usage over its cap (or use a day where it exceeded): the badge shows the true % (e.g. `128%`) with the `over` deep-red treatment, bar clamped at 100%. — overage edge case
6. The same percentage produces the same color in the summary bar and the row badge. — FR-013

## Validate: edge cases

- Set a `daily_usd: 0` for a backend → treated as unlimited (`无上限`), no divide-by-zero. — FR-002, FR-010
- Add a `name:` that matches no backend → ignored, not rendered, not summed. — edge case
- Switch the date picker to a past day → figures reflect that day's usage vs. current caps. — edge case

## Build check

```bash
go build ./...
cd web && npm run build   # if the frontend build is used in this environment
```

Both should succeed with no new errors.
