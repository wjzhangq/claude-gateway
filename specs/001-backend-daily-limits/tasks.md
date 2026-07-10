---
description: "Task list for Backend Daily Cost Limits"
---

# Tasks: Backend Daily Cost Limits

**Input**: Design documents from `/specs/001-backend-daily-limits/`

**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/backend-stats-api.md, quickstart.md

**Tests**: Not requested in the spec. No automated test tasks are generated; validation is via `go build`, frontend build, and `quickstart.md`.

**Organization**: Grouped by user story. US1 (config caps) is the foundational data layer; US2 (fleet summary) and US3 (per-row badge) are the two display slices that share the same API response and color helper.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- File paths are repository-relative.

## Path Conventions

Web application: Go backend under `config/`, `internal/`, `cmd/`; React frontend under `web/src/`.

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Confirm baseline builds before changes.

- [X] T001 Verify baseline builds: run `go build ./...` and `cd web && npm run build` (if used) to confirm a clean starting point.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Config type + lookup that every other slice depends on.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

- [X] T002 Add `BackendDailyLimit` struct (`Name string` yaml:"name", `DailyUSD float64` yaml:"daily_usd") and `BackendDailyLimits []BackendDailyLimit` field (yaml:"backend_daily_limits") to `config/config.go`, placed alongside `UserDailyLimit`/`UserDailyLimits`.
- [X] T003 Add `func (c *Config) LookupBackendDailyLimit(name string) float64` in `config/config.go` returning the configured cap, or `0` when not found or not positive (guards the `0 = unlimited` convention).

**Checkpoint**: Config parses the new section and exposes a lookup helper.

---

## Phase 3: User Story 1 - Configure a daily cost cap per backend (Priority: P1) 🎯 MVP

**Goal**: Administrator can set `backend_daily_limits` in config; the system recognizes each backend's cap (0/absent = unlimited).

**Independent Test**: Add caps for two backends in config, reload, and confirm via the read-only limits API that each cap is recognized and an unlisted backend is unlimited.

### Implementation for User Story 1

- [X] T004 [US1] Extend `limitsResponse` in `internal/handler/config.go` with a `BackendDailyLimits []backendLimitResponse` field (json:"backend_daily_limits") and add the `backendLimitResponse` struct (`Name`, `DailyUSD`).
- [X] T005 [US1] Populate the new field in `ConfigHandler.GetLimits` (`internal/handler/config.go`) from `h.cfg.BackendDailyLimits` (read-only surfacing; no changes to `UpdateLimits`).
- [X] T006 [US1] Add a documented `backend_daily_limits` example block to the config sample/README used by this project so admins know the YAML shape (per data-model.md).

**Checkpoint**: `GET /admin/api/config/limits` returns configured backend caps; config reload picks up edits with no code change.

---

## Phase 4: User Story 2 - Fleet-wide daily budget & consumption (Priority: P1) 🎯 MVP

**Goal**: `/admin/backends` shows total ceiling (sum of caps), total used today (sum of usage), and a color-coded progress bar; partial when unlimited backends exist.

**Independent Test**: With caps configured and usage recorded, open the page and confirm the aggregate ceiling, used amount, and threshold-colored bar match the sums, with a partial indicator when an unlimited backend exists.

### Implementation for User Story 2

- [X] T007 [US2] Extend `GetBackendStats` in `internal/handler/stats.go` to compute and return a top-level `summary` object (`daily_limit_total`, `used_total`, `used_pct`, `has_unlimited`) using `h.config.LookupBackendDailyLimit` and the existing per-backend `cost_usd`; keep the existing `stats` array shape (see contracts/backend-stats-api.md).
- [X] T008 [P] [US2] Add a shared `budgetColor(pct: number, hasLimit: boolean)` helper (returns tier: healthy <70, warn 70–<90, critical 90–100, over >100, or none when no limit) in a small util under `web/src/utils/` for reuse by summary and rows (FR-013).
- [X] T009 [US2] Update the `BackendStat` interface and add a `summary` type in `web/src/pages/AdminBackendsPage.tsx` (and `web/src/api.ts` types if applicable) to consume the new response fields.
- [X] T010 [US2] Render a fleet budget bar in `AdminBackendsPage.tsx` labeled `已用 $X / 上限 $Y (Z%)`, fill colored via `budgetColor`; when `has_unlimited` is true show the ceiling as partial (e.g. `$Y+`) and avoid a misleading "100% of 0" state (FR-004, FR-005, FR-006, FR-009, FR-010).

**Checkpoint**: Fleet summary bar renders with correct sums and threshold colors, partial ceiling when unlimited backends exist.

---

## Phase 5: User Story 3 - Per-backend cap & usage percentage (Priority: P2)

**Goal**: Each backend row shows a compact colored percentage badge plus a muted cap label; `无上限` chip for uncapped backends; overage clearly signaled.

**Independent Test**: With per-backend caps and usage, confirm each row shows cap + correct percentage badge, uncapped backends show `无上限`, and an over-cap backend shows the true % with the `over` treatment.

### Implementation for User Story 3

- [X] T011 [US3] Add a "每日上限 / 使用比例" column (or inline cell) to the backend table in `web/src/pages/AdminBackendsPage.tsx`: colored percentage badge via `budgetColor` + muted `$used / $cap` label (FR-007).
- [X] T012 [US3] Render a neutral `无上限` chip instead of a badge when the backend's `daily_limit <= 0` / `used_pct` is null, visually distinct from a 0%-used capped backend (FR-008, SC-004).
- [X] T013 [US3] Handle the overage case in the badge: clamp any visual fill at 100%, recolor to the `over` tier, and label the true percentage (e.g. `128%`).

**Checkpoint**: Per-row badges render with shared colors, unlimited chip, and overage treatment.

---

## Phase 6: Polish & Cross-Cutting Concerns

- [X] T014 [P] Verify color consistency: the same percentage produces the same color in the fleet bar and a row badge (FR-013, SC-003) by eyeballing a shared value.
- [X] T015 [P] Confirm the existing `public*` backend filtering still applies and that unmatched config names are ignored without error.
- [X] T016 Run `go build ./...` and the frontend build; fix any errors.
- [X] T017 Execute `specs/001-backend-daily-limits/quickstart.md` end-to-end and confirm all listed expectations (SC-001..SC-005 and edge cases).

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies.
- **Foundational (Phase 2)**: Depends on Setup. BLOCKS all user stories (config type + lookup are used everywhere).
- **User Stories (Phase 3–5)**: All depend on Phase 2.
- **Polish (Phase 6)**: Depends on the user stories being implemented.

### User Story Dependencies

- **US1 (P1)**: After Phase 2. Independent — surfaces config via the limits API.
- **US2 (P1)**: After Phase 2. Independent of US1 (reads caps via `LookupBackendDailyLimit`, not the limits API). Requires T002/T003.
- **US3 (P2)**: After Phase 2. Shares the extended `GetBackendStats` response (T007) and `budgetColor` helper (T008) with US2. If US2 is not built first, T007 and T008 must be done as part of US3.

### Within Each User Story

- Backend response/field tasks before the frontend rendering tasks that consume them.
- `budgetColor` helper (T008) before the components that use it (T010, T011).

### Parallel Opportunities

- T008 (frontend util) is [P] relative to T007 (backend handler) — different files.
- T014 and T015 (polish checks) are [P].
- US1 (T004–T006) is fully parallel to US2/US3 work once Phase 2 is done — it touches `config.go`'s handler path, not the stats handler or the page.

---

## Parallel Example: after Phase 2

```bash
# US1 backend surfacing and US2 backend+util can proceed together:
Task: "Extend limitsResponse + GetLimits in internal/handler/config.go"   # T004, T005
Task: "Extend GetBackendStats summary in internal/handler/stats.go"        # T007
Task: "Add budgetColor helper in web/src/utils/"                           # T008 [P]
```

---

## Implementation Strategy

### MVP First

The user's core ask is the fleet-wide view. MVP = Phase 1 → Phase 2 → US1 (config recognized) + US2 (fleet bar). This delivers "configure caps and see fleet budget/usage/progress" — the primary value.

1. Setup + Foundational (T001–T003).
2. US1 (T004–T006): caps configurable and visible via API.
3. US2 (T007–T010): fleet summary bar. **STOP and VALIDATE.**
4. Add US3 (T011–T013): per-row badges.
5. Polish (T014–T017).

### Notes

- No enforcement/routing changes (FR-014) — display only.
- Reuse existing per-backend `cost_usd` aggregation; do not add a new query (FR-011).
- Commit after each phase or logical group.
