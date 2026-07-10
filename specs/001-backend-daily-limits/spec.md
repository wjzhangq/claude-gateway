# Feature Specification: Backend Daily Cost Limits

**Feature Branch**: `001-backend-daily-limits`

**Created**: 2026-07-10

**Status**: Draft

**Input**: User description: "config 增加 backend_daily_limits，设置每类backend 每日上限额度。/admin/backends 页面增加显示每日backend 上限(把backend每日上限相加)，显示当日使用量(backend使用量相加)，显示使用进度，进度用不同颜色区分。每个backend table 页面显示，每日费用上限，使用百分比。对价格限制推荐下更好的显示方式"

## Clarifications

### Session 2026-07-10

- Q: Should `backend_daily_limits` enforce routing cutoffs or is this display-only? → A: Display/monitoring only — show caps, usage, and progress; routing behavior is unchanged when a cap is exceeded (enforcement is out of scope for this feature).
- Q: What color thresholds should the consumption progress use? → A: Green < 70%, Amber 70–90%, Red ≥ 90%, with a distinct overflow treatment above 100%. These thresholds apply identically to the fleet-wide summary and per-backend indicators.
- Q: How should each backend row show its usage ratio compactly? → A: A colored percentage badge (e.g. `42%`) with a muted secondary cap label (e.g. `$4.20 / $10`); the badge color follows the same thresholds as the fleet summary so summary and per-backend colors stay consistent.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Configure a daily cost cap per backend (Priority: P1)

An administrator sets a maximum daily spending amount (in USD) for each backend channel through configuration, so that runaway cost on any single upstream is bounded and predictable.

**Why this priority**: Without the configured limits there is nothing to display or track. This is the foundational capability the rest of the feature depends on.

**Independent Test**: Add daily limits for two backends in configuration, reload, and confirm the system recognizes each backend's configured cap without affecting backends that have no limit set.

**Acceptance Scenarios**:

1. **Given** a configuration entry that assigns a daily cost cap to backend "alpha", **When** the configuration is loaded, **Then** backend "alpha" is associated with that daily cap value.
2. **Given** a backend with no configured daily limit, **When** the configuration is loaded, **Then** that backend is treated as having no cap (unlimited).
3. **Given** a daily limit set to zero, **When** the configuration is loaded, **Then** that backend is treated as unlimited (consistent with existing limit conventions in the system).

---

### User Story 2 - See the fleet-wide daily budget and consumption on the backends page (Priority: P1)

An administrator opens the /admin/backends page and immediately sees the combined daily cost ceiling (sum of all backends' configured caps), the combined amount spent so far today (sum of per-backend usage), and a progress indicator that changes color as consumption approaches the ceiling.

**Why this priority**: This is the primary at-a-glance monitoring value the administrator asked for — knowing how close the whole fleet is to its daily budget.

**Independent Test**: With limits configured for several backends and some usage recorded for today, open the page and confirm the aggregate ceiling, the aggregate used amount, and a colored progress indicator reflecting the correct percentage all appear.

**Acceptance Scenarios**:

1. **Given** backends with configured daily caps totaling a known amount and known usage today, **When** the administrator views the page, **Then** the total daily ceiling and total used-today amounts are displayed and match the sums.
2. **Given** total usage is well below the ceiling, **When** the administrator views the progress indicator, **Then** it shows a "healthy" color.
3. **Given** total usage is approaching the ceiling, **When** the administrator views the progress indicator, **Then** it shows a "warning" color.
4. **Given** total usage has reached or exceeded the ceiling, **When** the administrator views the progress indicator, **Then** it shows an "over/critical" color.
5. **Given** one or more backends have no configured cap, **When** the aggregate ceiling is computed, **Then** the display makes clear the ceiling is partial/approximate rather than presenting a misleading complete total.

---

### User Story 3 - See per-backend daily cap and usage percentage in the table (Priority: P2)

For each backend row in the backends table, the administrator sees that backend's own daily cost cap and the percentage of that cap consumed today, so they can identify which specific backend is driving spend.

**Why this priority**: Adds per-backend granularity on top of the fleet-wide view. Useful but secondary to the aggregate summary.

**Independent Test**: With per-backend limits and usage, confirm each table row shows its configured cap and a correctly computed usage percentage, and that a backend without a cap shows an unambiguous "no limit" indicator.

**Acceptance Scenarios**:

1. **Given** a backend with a configured cap and recorded usage today, **When** the row is displayed, **Then** it shows the cap amount and the used/cap percentage.
2. **Given** a backend with no configured cap, **When** the row is displayed, **Then** it shows a clear "no limit" indication instead of a percentage.
3. **Given** a backend whose usage exceeds its cap, **When** the row is displayed, **Then** the percentage and its visual treatment clearly signal the overage.

---

### Edge Cases

- A backend name in the limits configuration does not match any active backend (typo or removed backend): the configured value is ignored for aggregation and surfaced as harmless, not counted toward the ceiling.
- All backends are unlimited: the aggregate ceiling display avoids implying a false "0 of 0" or "100%" state.
- Usage exists for a backend that has no configured cap: usage is still counted in the fleet-wide used-today total even though it has no cap.
- The viewed date is not today (historical date picker): usage percentages and progress reflect that date's usage against the current caps, and it is clear the figures are historical.
- Division by zero when a cap is zero/unset: percentage computation must not error and must degrade to a "no limit" presentation.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Configuration MUST support a new section that maps individual backend channels to a daily cost cap expressed in USD.
- **FR-002**: A backend with no entry, or an entry of zero, MUST be treated as unlimited, consistent with the system's existing "0 = unlimited" convention.
- **FR-003**: The system MUST expose, for the /admin/backends view, each backend's configured daily cap alongside its usage for the selected day.
- **FR-004**: The backends page MUST display a fleet-wide daily ceiling equal to the sum of all configured per-backend caps.
- **FR-005**: The backends page MUST display the fleet-wide amount spent for the selected day equal to the sum of per-backend usage.
- **FR-006**: The backends page MUST display a fleet-wide consumption progress indicator whose color changes across these thresholds: green below 70%, amber from 70% to under 90%, red at or above 90%, and a distinct overflow treatment above 100%.
- **FR-007**: Each backend row in the table MUST display that backend's daily cap and the percentage of the cap consumed for the selected day, shown compactly as a colored percentage badge (e.g. `42%`) with a muted secondary cap label (e.g. `$4.20 / $10`).
- **FR-013**: The per-backend percentage badge color and the fleet-wide progress color MUST use the same threshold definitions (FR-006), so aggregate and per-backend indicators are visually consistent.
- **FR-014**: The configured `backend_daily_limits` are display/monitoring only for this feature; the system MUST NOT change backend routing or selection behavior when a backend reaches or exceeds its configured daily cap.
- **FR-008**: When a backend has no cap, its row MUST show an unambiguous "no limit" indicator rather than a numeric percentage.
- **FR-009**: When one or more backends are unlimited, the aggregate ceiling display MUST indicate the total is partial rather than presenting it as a complete budget.
- **FR-010**: Percentage and progress computations MUST handle zero/unset caps without error.
- **FR-011**: The feature MUST reuse the existing backend usage/cost data already aggregated per backend per day rather than introducing a separate accounting source.
- **FR-012**: Changing the configured caps MUST take effect on the display without requiring code changes (configuration-driven).

### Design Recommendation for Cost-Limit Display *(advisory, from user request "对价格限制推荐下更好的显示方式")*

This section records recommended presentation approaches for the planning phase to choose from; it does not mandate a specific visual implementation.

- **Fleet summary**: Present the ceiling and used-today as a single horizontal budget bar with the used amount filling toward the ceiling, labeled "已用 $X / 上限 $Y (Z%)". Color the fill by threshold: green below 70%, amber 70–90%, red at/over 90%, with a distinct "overflow" treatment beyond 100%.
- **Per-backend row**: Show usage compactly as a colored percentage badge (e.g. `42%`) with the cap as a muted secondary label (e.g. `$4.20 / $10`), so an administrator can scan the column for hotspots without extra row height. The badge color uses the same thresholds as the fleet bar.
- **Color consistency**: The badge color and the fleet bar fill color are driven by one shared threshold definition, so the same percentage reads as the same color everywhere on the page.
- **Unlimited state**: Use a neutral chip such as "无上限" instead of a badge, so unlimited backends are visually distinct from 0%-used capped backends.
- **Overage state**: When usage exceeds the cap, cap the visual bar at 100% but recolor it (deep red) and annotate the true percentage (e.g. "128%") so overage is unmistakable.
- **Thresholds**: Green below 70%, amber 70% to under 90%, red at or above 90%, overflow treatment above 100% (per FR-006).

### Key Entities *(include if feature involves data)*

- **Backend daily limit**: An association between a backend channel identity and a daily USD cost cap. Zero or absent means unlimited.
- **Backend daily usage**: The aggregated cost spent by a single backend for a given day, already tracked by the system.
- **Fleet daily budget summary**: A derived view combining the sum of configured caps, the sum of usage, and the resulting consumption ratio for the selected day.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: An administrator can determine the fleet-wide daily budget, amount spent today, and how close spending is to the ceiling within 5 seconds of opening the backends page, without any calculation.
- **SC-002**: For any backend with a configured cap, the administrator can read its cap and consumption percentage directly from its table row without navigating elsewhere.
- **SC-003**: The progress indicator visibly changes color across the green (<70%), amber (70–90%), and red (≥90%) states as consumption crosses the thresholds in 100% of cases, and the same percentage produces the same color in both the fleet summary and the per-backend badge.
- **SC-004**: Backends without a configured cap are visually distinguishable from capped backends at 0% usage in 100% of cases.
- **SC-005**: Adding, changing, or removing a backend's daily cap is reflected on the page after a configuration reload with no code change required.

## Assumptions

- Per-backend daily cost data is already available from the existing backend statistics aggregation and can be reused; this feature does not introduce new usage logging.
- "Backend" here refers to the upstream Claude API channels enumerated in the backend list, and excludes public-provider entries already filtered out of the backends view.
- The configured caps are display/monitoring limits only (confirmed in clarification); they do not enforce hard cutoffs on routing. Enforcement is explicitly out of scope for this feature and may be specced separately later.
- The daily window aligns with the existing per-day usage boundary used elsewhere in the system.
- Amounts are expressed and displayed in USD, consistent with existing cost displays on the page.
- The existing date picker on the backends page determines which day's usage is compared against the configured caps.
