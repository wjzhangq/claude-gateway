# Phase 0 Research: Backend Daily Cost Limits

All spec ambiguities were resolved in `/speckit-clarify` (Session 2026-07-10). No open NEEDS CLARIFICATION remain. This document records the design decisions grounded in the existing codebase.

## Decision 1: Config shape for per-backend caps

- **Decision**: Add `BackendDailyLimits []BackendDailyLimit` to `config.Config`, where `BackendDailyLimit{ Name string; DailyUSD float64 }`, YAML key `backend_daily_limits`. Add `(*Config) LookupBackendDailyLimit(name string) float64` returning `0` when absent.
- **Rationale**: Mirrors the existing `UserDailyLimit` slice pattern (`config/config.go:15`) and the established `0 = unlimited` convention (FR-002). A slice keyed by `name` matches how backends are already identified (`BackendAPI.Name`, `config.go:92`) and how stats rows key by `backend` (`db/stats.go:GetBackendStats`).
- **Alternatives considered**: (a) `map[string]float64` — rejected for consistency with the surrounding slice-of-struct style and to keep YAML diff-friendly with the existing `replaceYAMLValue` tooling. (b) Embedding the cap on `BackendAPI` itself — rejected because caps are a monitoring concern the user framed as a separate config section, and co-locating would complicate the existing backend validation loop.

## Decision 2: Where to compute usage vs. cap

- **Decision**: Extend `GetBackendStats` (`internal/handler/stats.go:69`) to attach, per backend, its configured `daily_limit`, and to add a top-level fleet summary (`daily_limit_total`, `used_total`, `has_unlimited`). Reuse the existing `h.db.GetBackendStats(start, end)` aggregation for usage.
- **Rationale**: `StatsHandler` already holds `*config.Config` (`stats.go:21`) and the handler already returns the per-backend cost the UI reads (`cost_usd`). Joining caps here means no new query and no new data source (FR-011), and the selected-day date range is already passed through (`start_date`/`end_date`), satisfying the historical-date edge case.
- **Alternatives considered**: Computing sums client-side only — rejected for the aggregate ceiling because "partial/approximate when unlimited backends exist" (FR-009) is a server-known fact (`has_unlimited`) best asserted once; the client still renders, but the server supplies the authoritative totals and the unlimited flag.

## Decision 3: Threshold color model (shared summary + row)

- **Decision**: Single source of truth for thresholds on the frontend: a `budgetColor(pct)` helper returning a tier (`healthy` <70, `warn` 70–<90, `critical` ≥90, `over` >100). Both the fleet bar fill and the per-row percentage badge consume it (FR-013).
- **Rationale**: Guarantees "same percentage → same color everywhere" (SC-003). Thresholds fixed by clarification: green <70%, amber 70–90%, red ≥90%, distinct overflow >100%.
- **Alternatives considered**: Independent color logic per component — rejected; it risks drift and violates FR-013.

## Decision 4: Unlimited & zero-cap presentation

- **Decision**: When a backend's cap is `0`/absent, render a neutral `无上限` chip instead of a badge; exclude it from the ceiling sum but include its usage in `used_total`. Percentage helper returns `null`/no-limit sentinel when cap ≤ 0 (FR-008, FR-010). When any backend is unlimited, the summary shows the ceiling as partial (e.g. `上限 $Y+`) driven by `has_unlimited` (FR-009).
- **Rationale**: Directly encodes the edge cases in the spec (division-by-zero guard, unlimited-in-aggregate, usage-without-cap counted).
- **Alternatives considered**: Treating unlimited as `100%` or `0%` — rejected; both mislead (SC-004 requires unlimited be visually distinct from 0%-used).

## Decision 5: Overage presentation

- **Decision**: When usage > cap, clamp the visual bar/badge fill at 100% but recolor to the `over` tier (deep red) and label the true percentage (e.g. `128%`).
- **Rationale**: Matches the advisory display guidance and keeps layout stable while making overage unmistakable.

## Decision 6: Config read surfacing

- **Decision**: Extend `ConfigHandler.GetLimits` (`internal/handler/config.go:39`) response with `backend_daily_limits` (read-only) so an admin can see configured caps via the limits API. Editing caps via `UpdateLimits` is out of scope for this feature (display-only); caps are edited directly in the YAML file and picked up on reload (FR-012).
- **Rationale**: Keeps scope tight to display; avoids extending the fragile line-based `replaceUserLimit` YAML writer. Reload path already exists.
- **Alternatives considered**: Full CRUD via the limits page — deferred; not requested and adds risk to the config-write path.
