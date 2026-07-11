# Feature Specification: Backend Health Hardening

**Feature Branch**: `003-backend-health-hardening`

**Created**: 2026-07-11

**Status**: Draft

**Input**: User description: Review-driven hardening of the backend auto-eject/recovery system (spec 002). Twelve improvement points across three tiers — real availability/cost risks, robustness gaps, and observability/tuning — plus a classification rule that for Claude backends only errors on the sonnet/haiku/opus model families count against backend health.

## Context

This feature builds directly on `002-backend-auto-eject-recovery`, which delivered a five-state backend health machine (healthy / degraded / isolated / probing / quarantine), TTL-based ejection with jittered backoff, a 60-second reconcile loop, an active `check --health` probe, and a failsafe that guarantees a minimum routable fleet. That system works: defense-in-depth, fast-eject/slow-recover, and a minimum-availability floor are all in place.

This spec addresses twelve issues found in a design review of the shipped system. The issues fall into three tiers by severity. The highest-priority items (US1–US5) concern real availability windows, wasted spend, and silently lost backends. The remaining items harden edge behavior and close observability gaps.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Instant failsafe on sudden fleet collapse (Priority: P1)

When a burst of upstream errors drives the routable backend count below the safety threshold, the system must restore a minimum routable fleet immediately rather than waiting for the next periodic reconcile pass. Today the failsafe only runs inside the 60-second reconcile loop, so a mid-tick collapse can leave the gateway under-provisioned for up to nearly a full minute — exactly the window when demand is being retried and pressure is highest.

**Why this priority**: This is a direct, user-visible availability gap. During an incident the gateway can return "no backend available" errors for up to ~60s even though recoverable backends exist. It is the single most impactful correctness fix in the set.

**Independent Test**: Drive several backends to isolated within a single reconcile interval so routable count drops below the trigger threshold, then immediately request a pick — a routable (force-promoted) backend must be returned without waiting for the periodic pass.

**Acceptance Scenarios**:

1. **Given** a fleet where a burst of failures has just pushed routable count below the trigger threshold, **When** a backend transitions into a non-routable state, **Then** the failsafe evaluates immediately and force-promotes enough candidates to restore the routable floor without waiting for the next reconcile tick.
2. **Given** routable count is below the floor and the periodic reconcile has not yet fired, **When** the gateway attempts to select a backend and finds no routable candidate, **Then** the failsafe is invoked synchronously so a candidate can be promoted and returned.
3. **Given** sustained pressure keeps knocking backends out, **When** the failsafe runs, **Then** it promotes enough candidates in a single invocation to reach the recovery target (not one-per-cycle), and the recovery target is an explicit, documented number.

---

### User Story 2 - Active probe carries the real failure signal (Priority: P1)

When the active health probe declares a backend unhealthy, the resulting isolation TTL must be based on the status code the probe actually observed, so that a probe-observed 429 is honored with its Retry-After / rate-limit backoff rather than being mis-bucketed as a short generic-server cooldown. Today the probe result is a bare healthy/latency pair, so a failing probe falls back to the last recorded live request code, which is often stale or a success — mis-scheduling the cooldown and re-probing the upstream too soon.

**Why this priority**: Mis-bucketed TTL causes the gateway to hammer a rate-limited upstream far sooner than the upstream asked, which can deepen the rate-limit and slow recovery. It also affects cost and upstream relations.

**Independent Test**: Run an active probe that receives a 429 with a Retry-After header; confirm the backend is isolated for the rate-limit-appropriate duration honoring Retry-After, not the generic short cooldown.

**Acceptance Scenarios**:

1. **Given** the active probe receives a 429 with a Retry-After header, **When** the probe result is applied, **Then** the backend is isolated using the same TTL policy the passive request path would apply for that code, honoring Retry-After.
2. **Given** the active probe receives a 5xx, **When** the probe result is applied, **Then** the isolation TTL matches the server-class policy for that observed code.
3. **Given** the active probe reports success, **When** the result is applied, **Then** recovery proceeds through the existing gated state progression unchanged.

---

### User Story 3 - Probing does not waste money or worsen quota (Priority: P1)

The active probe must not spend real inference budget probing backends that were isolated for quota/budget reasons, and any inference-based probe it does perform must be minimal-cost and accounted for against the probed backend's budget. Today the probe falls back to a real billable inference call, so probing a fleet of many backends every cycle burns money, and re-probing a quota-exhausted backend actively pushes it further over its limit.

**Why this priority**: This directly wastes money on every probe cycle across the whole fleet and is actively counterproductive for quota-isolated backends. Cost and correctness both suffer.

**Independent Test**: Isolate a backend for a quota reason, run a probe cycle, and confirm no billable inference request is sent to it; then verify that any inference-based probe elsewhere uses the minimum token budget and its cost is attributed to that backend.

**Acceptance Scenarios**:

1. **Given** a backend is isolated for a quota/budget reason (quota-exhausted or a quota-indicating 403), **When** a probe cycle runs, **Then** that backend receives no billable inference probe and recovers only via daily-reset / TTL-expiry paths.
2. **Given** a backend requires an inference-based probe to verify health, **When** the probe is sent, **Then** it uses the minimum possible token budget.
3. **Given** an inference-based probe is sent, **When** it completes, **Then** its estimated cost is recorded against that backend's spend so budget accounting stays closed.

---

### User Story 4 - No backend is silently lost at startup (Priority: P1)

Startup validation and the runtime active probe must use the same health-determination logic (including the inference fallback), so a backend that can serve inference but does not support the model-listing endpoint is not permanently disabled at startup with no path to recovery. Today startup validation uses listing-only validation and permanently marks such backends failed, while the runtime probe has a fallback that would have recognized them as healthy — the two disagree, and the stricter one wins permanently.

**Why this priority**: This silently and permanently removes usable capacity from the fleet with no automatic recovery, and the loss is invisible unless someone inspects logs.

**Independent Test**: Configure a backend that rejects the listing endpoint but serves inference; start the gateway and confirm the backend is not permanently disabled and becomes routable.

**Acceptance Scenarios**:

1. **Given** a backend that fails the listing endpoint but serves inference, **When** the gateway starts and validates backends, **Then** the backend is not permanently disabled and can carry traffic.
2. **Given** a backend that fails both listing and inference checks at startup, **When** validation runs, **Then** it is treated as unavailable consistent with the runtime probe's judgment.
3. **Given** the shared health logic changes, **When** either startup or runtime evaluates a backend, **Then** both reach the same availability verdict for the same backend condition.

---

### User Story 5 - Quota recovery is not bound to a single hardcoded wall-clock (Priority: P1)

Quota-based isolation recovery must not be hardcoded to a single fixed reset instant (00:00 CST) that assumes every backend shares one reset clock. Backends on different providers/timezones either stay isolated hours too long or are released early only to immediately hit the quota again. Because the daily limit is itself only an estimate, binding recovery to a precise wall-clock moment is internally inconsistent.

**Why this priority**: Cross-provider fleets are mis-scheduled by up to many hours in either direction, causing either lost capacity or thrash. It also compounds with the estimation error in the budget model.

**Independent Test**: Configure two backends with different reset expectations; confirm each recovers on a schedule appropriate to it rather than a single shared hardcoded instant.

**Acceptance Scenarios**:

1. **Given** backends with differing quota reset expectations, **When** a backend is quota-isolated, **Then** its recovery schedule reflects a per-backend configuration or a treat-as-long-TTL policy rather than a single global wall-clock instant.
2. **Given** a quota-isolated backend under the treat-as-long-TTL policy, **When** its TTL elapses, **Then** normal TTL-expiry/probe paths discover recovery.
3. **Given** no per-backend reset configuration is provided, **When** a backend is quota-isolated, **Then** a documented default policy applies without assuming a shared reset clock.

---

### User Story 6 - Model-scoped error validity for Claude backends (Priority: P1)

For Claude backends, only errors on requests targeting the sonnet, haiku, and opus model families count against backend health. Errors on requests to other/unknown models must not eject or degrade a Claude backend, because such errors do not indicate the backend is unhealthy for the traffic the gateway actually routes.

**Why this priority**: Without this, a backend can be ejected by errors that say nothing about its real health, removing healthy capacity. The user explicitly called this out as a required classification rule.

**Independent Test**: Send an error-producing request for a non-sonnet/haiku/opus model to a Claude backend; confirm no health-state change. Repeat for a sonnet/haiku/opus model and confirm the normal ejection applies.

**Acceptance Scenarios**:

1. **Given** a Claude backend, **When** a request for a model outside the sonnet/haiku/opus families returns an otherwise-impacting error, **Then** the backend's health state, consecutive-error counter, and TTL are unchanged.
2. **Given** a Claude backend, **When** a request for a sonnet/haiku/opus model returns an impacting error, **Then** the normal ejection/TTL logic applies.
3. **Given** the target model cannot be determined, **When** an impacting error occurs, **Then** the system applies a documented default (treat as valid or treat as ignored) chosen to avoid silently losing healthy capacity.

---

### User Story 7 - Flapping backends are caught by a windowed error signal (Priority: P2)

A backend that alternates success/failure must eventually be degraded or isolated based on a windowed error-rate signal, instead of oscillating forever. Today any success resets the consecutive-error counter, so an alternating backend never accumulates enough consecutive errors to degrade or escalate, and instead loops isolated → probing → one success → healthy → isolated, wasting a request every cycle.

**Why this priority**: Wastes requests and destabilizes routing under partial-failure conditions, but the fleet still functions, so it ranks below the P1 availability/cost items.

**Independent Test**: Feed a backend an alternating success/failure pattern with a sustained high error rate; confirm it settles into degraded/isolated rather than flapping through healthy.

**Acceptance Scenarios**:

1. **Given** a backend with a sustained high error rate over a recent window but no long consecutive run, **When** health is evaluated, **Then** the backend is degraded or isolated based on the windowed rate.
2. **Given** a backend whose windowed error rate falls back below threshold, **When** health is evaluated, **Then** it is allowed to recover through the normal gated progression.

---

### User Story 8 - Auth-class quarantine stops pointless re-probing (Priority: P2)

An auth-class (401) quarantine must not be re-probed on the same fixed short interval indefinitely, because a revoked key does not recover on its own. Repeatedly promoting such a backend to probing every 15–30 minutes just fires a request that is guaranteed to fail.

**Why this priority**: Wastes requests and log noise and delays genuine recovery signals, but does not by itself take down the fleet.

**Independent Test**: Quarantine a backend for a 401 repeatedly; confirm the re-probe interval grows (or requires an external key-rotation signal) rather than staying fixed.

**Acceptance Scenarios**:

1. **Given** a backend quarantined for an auth failure, **When** it is quarantined again on the next probe, **Then** its quarantine TTL grows (e.g., doubles) rather than staying fixed.
2. **Given** an external key-rotation/credential-change signal, **When** it is received, **Then** the backend becomes eligible for re-probe regardless of the grown TTL.

---

### User Story 9 - Failover chain is bounded and budget-aware (Priority: P2)

Transparent request-level failover must not amplify load and tail latency during a quota cascade. When many backends are near their limit, retrying each user request across multiple upstreams adds pressure at the worst moment and stacks per-attempt latency. The failover chain must have a total time budget, must fast-fail when several backends report quota in a short window, and must prefer lower-budget-utilization targets.

**Why this priority**: Improves behavior under cascade conditions and caps tail latency, but the base failover already functions.

**Independent Test**: Simulate several backends reporting quota within a short window; confirm the request fast-fails within the chain deadline instead of trying every backend, and that chosen failover targets are the lower-utilization ones.

**Acceptance Scenarios**:

1. **Given** a user request that triggers failover, **When** the cumulative failover time reaches the chain deadline budget, **Then** the request stops retrying and returns rather than continuing to add upstream load.
2. **Given** several backends report quota within a short window, **When** a new request would fail over, **Then** it fast-fails instead of walking the remaining backends.
3. **Given** multiple eligible failover targets, **When** the next target is chosen, **Then** a lower budget-utilization backend is preferred over an arbitrary one.

---

### User Story 10 - Retry-After honored within sane bounds (Priority: P2)

A Retry-After value from an upstream must be honored only within a bounded range, so a single header cannot remove a backend for an excessive duration. Very large values must be capped, and non-positive/near-zero values floored to a minimum cooldown.

**Why this priority**: Protects against a single upstream response pulling a node for many hours, but is a bounded-edge hardening rather than a live outage.

**Independent Test**: Return an extremely large Retry-After and confirm the isolation is capped at the maximum; return a zero/negative value and confirm a minimum floor applies.

**Acceptance Scenarios**:

1. **Given** an upstream returns a Retry-After far larger than the policy maximum, **When** isolation TTL is computed, **Then** it is capped at the documented maximum.
2. **Given** an upstream returns a non-positive or near-zero Retry-After, **When** isolation TTL is computed, **Then** a documented minimum floor applies.

---

### User Story 11 - Degraded state has a real, documented trigger (Priority: P3)

The conditions that move a backend into the degraded state must be clearly defined and reachable. Today the passive path isolates on the first impacting error, so the "3 consecutive errors → degraded" path is unreachable from live traffic; degraded is entered only via probe recovery. The intended triggers for degraded must be clarified and, if the consecutive-error path is meant to exist, made reachable — otherwise the state's semantics must be documented as recovery-only.

**Why this priority**: Correctness-of-documentation issue; the state machine works, but its behavior does not match the stated design, which risks future confusion. No live availability impact.

**Independent Test**: Enumerate every transition into degraded from the shipped logic and confirm each is reachable and matches documentation.

**Acceptance Scenarios**:

1. **Given** the shipped state machine, **When** all transitions into degraded are enumerated, **Then** each is reachable under some real condition and documented.
2. **Given** the "consecutive errors → degraded" path is intended to exist, **When** a backend accumulates that many impacting errors while routable, **Then** it enters degraded as documented.

---

### User Story 12 - Observability: separate counters and state-transition metrics (Priority: P3)

Operators must be able to distinguish cumulative error count from consecutive error count, and must have per-backend metrics for state transitions and probe cost so the budget estimate can be calibrated against reality. Today the admin view reports the same consecutive counter under two labels, and there is no export of isolation counts by code, failsafe triggers, probe spend, per-state dwell time, or the estimated-spend-at-first-403 vs configured-limit comparison needed to calibrate the budget model.

**Why this priority**: Tuning and diagnosis quality improvement; valuable for operating a 40+ node fleet but not a functional defect.

**Independent Test**: Drive a backend through several transitions and confirm the exported metrics distinguish cumulative vs consecutive errors and report transition/dwell/probe-cost data.

**Acceptance Scenarios**:

1. **Given** a backend that has recovered from errors, **When** the admin view is read, **Then** cumulative error count and consecutive error count are reported as distinct values.
2. **Given** a fleet under operation, **When** metrics are exported, **Then** per-backend isolation counts by code, failsafe trigger counts, probe cost, and per-state dwell time are available.
3. **Given** a backend first hits a quota-indicating 403, **When** that event is recorded, **Then** the estimated spend at that moment and the configured limit are captured so estimation error can be calibrated.

---

### Edge Cases

- A burst pushes routable count to zero between reconcile ticks (US1 must still recover synchronously via the pick path).
- An active probe both fails and observes no status code (transport failure) — TTL must fall back to a documented server-class default (US2).
- A backend is isolated for a quota 403 but the daily-reset policy and a per-backend reset config disagree — precedence must be documented (US3/US5).
- A backend serves inference but returns non-200 on both listing and message probes at startup (US4 — treated unavailable).
- A Claude request omits or uses an aliased model name so the family cannot be determined (US6 — documented default).
- A backend flaps exactly at the windowed-rate threshold boundary (US7 — no oscillation across the boundary).
- Retry-After present alongside a quota-indicating body — precedence between the capped Retry-After and the quota policy must be documented (US10 vs US3).
- Failover chain deadline elapses mid-retry on the last remaining backend (US9 — return current best result rather than abandoning).

## Requirements *(mandatory)*

### Functional Requirements

**Failsafe / availability (US1)**

- **FR-001**: The system MUST evaluate the minimum-routable failsafe immediately when a backend transitions into a non-routable state, not only on the periodic reconcile pass.
- **FR-002**: The system MUST invoke the failsafe synchronously when a backend selection finds no routable candidate, so a candidate can be promoted and returned in the same operation where feasible.
- **FR-003**: The system MUST promote enough candidates in a single failsafe invocation to reach an explicit, documented recovery target, rather than restoring one backend per cycle.
- **FR-004**: The failsafe MUST retain hysteresis/shield protections so event-driven invocation does not cause promotion thrash.

**Active probe TTL fidelity (US2)**

- **FR-005**: The active health probe MUST report the status code it observed (and any Retry-After) alongside its healthy/latency result.
- **FR-006**: Probe-driven isolation MUST compute its TTL using the same policy as the passive request path for the observed code, including honoring Retry-After.
- **FR-007**: When a probe fails with no observable status code, the system MUST apply a documented server-class default TTL.

**Probe cost control (US3)**

- **FR-008**: The system MUST NOT send a billable inference probe to a backend that is isolated for a quota/budget reason; such backends MUST recover only via reset/TTL-expiry paths.
- **FR-009**: Any inference-based probe MUST use the minimum viable token budget.
- **FR-010**: The estimated cost of any inference-based probe MUST be attributed to the probed backend's spend accounting.

**Startup/runtime consistency (US4)**

- **FR-011**: Startup validation and the runtime active probe MUST use the same health-determination logic, including the inference fallback for backends that do not support the listing endpoint.
- **FR-012**: A backend that can serve inference MUST NOT be permanently disabled at startup solely for failing the listing endpoint.

**Quota recovery clock (US5)**

- **FR-013**: Quota-based isolation recovery MUST NOT be bound to a single hardcoded global wall-clock reset instant.
- **FR-014**: The system MUST support either a per-backend reset configuration or a treat-as-long-TTL quota-isolation policy, with a documented default when no per-backend config is provided.

**Model-scoped error validity (US6)**

- **FR-015**: For Claude backends, only impacting errors on requests targeting the sonnet, haiku, or opus model families MUST count against backend health.
- **FR-016**: Impacting errors on requests to other/unknown models MUST NOT change a Claude backend's health state, consecutive-error counter, or TTL.
- **FR-017**: When the target model family cannot be determined, the system MUST apply a documented default that avoids silently losing healthy capacity.

**Windowed error signal (US7)**

- **FR-018**: The system MUST degrade or isolate a backend whose recent windowed error rate stays above a threshold, even without a long consecutive-error run.
- **FR-019**: A backend whose windowed error rate returns below threshold MUST be allowed to recover through the normal gated progression.

**Auth quarantine backoff (US8)**

- **FR-020**: Repeated auth-class quarantine of the same backend MUST grow its quarantine TTL (e.g., exponential) rather than re-probing on a fixed short interval.
- **FR-021**: An external credential-change/key-rotation signal MUST make an auth-quarantined backend eligible for re-probe regardless of the grown TTL.

**Bounded failover (US9)**

- **FR-022**: The request-level failover chain MUST enforce a total time-budget deadline across all attempts.
- **FR-023**: The system MUST fast-fail (skip further failover) when several backends report quota within a short window.
- **FR-024**: Failover target selection MUST prefer lower budget-utilization backends over arbitrary ones.

**Retry-After bounds (US10)**

- **FR-025**: A Retry-After-derived isolation TTL MUST be capped at a documented maximum.
- **FR-026**: A non-positive or near-zero Retry-After MUST be floored to a documented minimum cooldown.

**Degraded semantics (US11)**

- **FR-027**: Every transition into the degraded state MUST be reachable under a real condition and documented; if the consecutive-error path is intended, it MUST be made reachable, otherwise degraded MUST be documented as recovery-only.

**Observability (US12)**

- **FR-028**: The system MUST expose cumulative error count and consecutive error count as distinct values.
- **FR-029**: The system MUST export per-backend metrics for isolation counts by status code, failsafe trigger counts, inference-probe cost, and per-state dwell time.
- **FR-030**: The system MUST record the estimated spend at the moment a backend first hits a quota-indicating 403 alongside the configured limit, to enable calibration of the budget estimate.

### Key Entities *(include if feature involves data)*

- **Backend health record**: Adds a cumulative error counter (distinct from the consecutive counter), a windowed error-rate signal, an auth-quarantine repeat count (for exponential TTL), and per-backend quota-reset configuration/policy.
- **Probe result**: Extends the active-probe payload with the observed status code and any Retry-After value, and marks whether an inference probe was used and its estimated cost.
- **Model family classification**: A mapping from a request's target model to a Claude family (sonnet/haiku/opus) or "other/unknown", used to decide whether an error counts against health.
- **Backend metrics record**: Per-backend counters and timers for isolation-by-code, failsafe triggers, probe cost, per-state dwell time, and the first-403 estimated-spend-vs-limit snapshot.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: When a burst drops routable count below the floor, a routable backend is available within one selection attempt (no multi-second availability gap tied to the periodic cycle).
- **SC-002**: 100% of probe-driven isolations use the same TTL policy as the passive path for the same observed code, and probe-observed rate limits honor Retry-After.
- **SC-003**: Probe cycles send zero billable inference requests to quota-isolated backends, and any inference probe uses the minimum token budget with its cost attributed to the backend.
- **SC-004**: No backend that can serve inference is permanently disabled at startup for a listing-only failure.
- **SC-005**: In a mixed-provider fleet, no backend is held isolated more than a documented tolerance beyond its actual quota reset, and none is released only to immediately re-hit quota within the same short window.
- **SC-006**: For Claude backends, 0 health-state changes result from errors on non-sonnet/haiku/opus models, while errors on those families still eject as before.
- **SC-007**: A backend fed a sustained alternating failure pattern settles into a non-healthy state instead of oscillating through healthy on every cycle.
- **SC-008**: A repeatedly auth-quarantined backend's re-probe interval grows monotonically, and no auth-quarantine re-probe fires more frequently than the grown interval absent a key-rotation signal.
- **SC-009**: Under a simulated quota cascade, per-request failover attempts are bounded by the chain deadline and the system fast-fails rather than walking every backend.
- **SC-010**: No single upstream Retry-After can isolate a backend beyond the documented maximum, and none shorter than the documented minimum.
- **SC-011**: Every documented transition into degraded is demonstrably reachable in tests.
- **SC-012**: Operators can read distinct cumulative and consecutive error counts, and can export per-backend transition/dwell/probe-cost metrics plus the first-403 spend-vs-limit calibration data point.

## Assumptions

- This feature modifies the existing `002-backend-auto-eject-recovery` implementation in place; it does not replace the five-state machine, SWRR selection, or the reconcile loop, but hardens them.
- "Claude backends" refers to backends serving the Anthropic-style `/v1/messages` API; the sonnet/haiku/opus families are identified by matching the request's model name against those family keywords. Non-Claude backend types are out of scope for the model-scoped rule (FR-015–FR-017) and retain existing behavior.
- The daily budget limit remains an estimate (as in spec 001/002); calibration data (FR-030) is collected to improve it but the budget factor stays a soft, non-gating signal.
- The active probe and startup validation share a single health-determination routine after this change; the inference fallback model used for probing is the same minimal probe defined in spec 002, reduced to the minimum token budget.
- Alerting delivery for quarantine remains a structured log (as in spec 002); this feature adds metrics/counters but does not require a new alerting channel.
- Prioritization follows the reviewer's guidance: items 1–5 (US1–US5) plus the explicitly required model-scoped rule (US6) are P1; robustness items are P2; documentation/observability items are P3.
- Exact numeric thresholds (recovery target count, windowed-rate threshold and window length, failover deadline, Retry-After min/max, auth-TTL growth factor) will be finalized during planning; the spec fixes the required behavior, not the constants.
