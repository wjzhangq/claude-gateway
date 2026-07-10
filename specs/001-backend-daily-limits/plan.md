# Implementation Plan: Backend Daily Cost Limits

**Branch**: `001-backend-daily-limits` | **Date**: 2026-07-10 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/001-backend-daily-limits/spec.md`

## Summary

Add a config-driven per-backend daily cost cap (`backend_daily_limits`, keyed by backend name, USD, 0 = unlimited) and surface it on the `/admin/backends` page as (a) a fleet-wide budget bar summing all caps vs. summed usage for the selected day, and (b) a compact colored percentage badge + muted cap label per backend row. Colors follow one shared threshold definition (green <70%, amber 70–90%, red ≥90%, distinct overflow >100%) used identically by the summary and the per-row badges. This feature is display/monitoring only — routing behavior is unchanged (FR-014).

The existing `GetBackendStats` handler already aggregates per-backend cost per day and already holds a `*config.Config`, so the limit values are joined into the same response with no new data source (FR-011).

## Technical Context

**Language/Version**: Go 1.x (backend), TypeScript + React (web/src)

**Primary Dependencies**: Gin (HTTP), gopkg.in/yaml.v3 (config), Vite + React + Tailwind CSS (frontend)

**Storage**: SQLite (`usage_logs` table) — read-only for this feature via existing `GetBackendStats` aggregation; caps stored in the YAML config file

**Testing**: `go test ./...` (backend); frontend has no unit-test harness — validate via `go build` + manual quickstart

**Target Platform**: Linux server + browser admin UI

**Project Type**: Web application (Go backend + React frontend)

**Performance Goals**: Reuse existing per-request stats query; no new query path. Page renders limits inline with the current stats fetch (SC-001: <5s at-a-glance).

**Constraints**: Must follow existing "0 = unlimited" convention (FR-002); percentage math must not divide by zero (FR-010); config change reflected after reload with no code change (FR-012).

**Scale/Scope**: Handful of backends (existing backend list); single admin page; one config section; one API response extension.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

The project constitution (`.specify/memory/constitution.md`) is an unpopulated template — no ratified principles or gates are defined. No constitutional constraints apply. Proceeding under the project's existing conventions (config-driven limits, `0 = unlimited`, reuse of existing aggregation) which this plan honors. **Gate: PASS (no defined principles to violate).**

## Project Structure

### Documentation (this feature)

```text
specs/001-backend-daily-limits/
├── plan.md              # This file
├── research.md          # Phase 0 output
├── data-model.md        # Phase 1 output
├── quickstart.md        # Phase 1 output
├── contracts/           # Phase 1 output
│   └── backend-stats-api.md
└── tasks.md             # Phase 2 output (/speckit-tasks — NOT created here)
```

### Source Code (repository root)

```text
config/
└── config.go                        # Add BackendDailyLimits []BackendDailyLimit + lookup helper

internal/handler/
├── stats.go                         # GetBackendStats: join per-backend cap + fleet summary into response
└── config.go                        # GetLimits: expose backend_daily_limits (read-only surfacing)

web/src/
├── api.ts                           # (types only — endpoint unchanged)
└── pages/
    └── AdminBackendsPage.tsx        # Fleet budget bar + per-row percentage badge; shared color util
```

**Structure Decision**: Existing web-application layout. Backend changes concentrate in `config/config.go` (new struct + helper) and `internal/handler/stats.go` (response extension on the already-`config`-aware `StatsHandler`). Frontend changes are confined to `AdminBackendsPage.tsx` plus a small shared threshold-color helper. No new files strictly required on the backend; a new config type is added alongside the existing `UserDailyLimit`.

## Complexity Tracking

No constitutional violations. No complexity deviations to justify.
