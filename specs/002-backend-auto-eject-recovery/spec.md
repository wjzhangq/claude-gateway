# Feature Specification: Backend Auto-Eject & Recovery

**Feature Branch**: `002-backend-auto-eject-recovery`

**Created**: 2026-07-11

**Status**: Draft

**Input**: User description: "模型路由 Backend 自动退出与恢复设计方案 — 以 provider 真实 HTTP code 为退出依据的节点状态机、TTL 分级隔离、渐进恢复与保底可用性。"

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Auto-eject a failing backend based on real upstream response (Priority: P1)

The gateway routes requests across a fleet of upstream backends. When a backend starts returning node-level failures from the upstream provider (rate-limited, quota/forbidden, auth-failed, or server errors), the gateway must stop routing new traffic to it automatically, without an operator intervening, so that callers stop hitting a known-bad node.

**Why this priority**: This is the core value. Today a saturated or broken backend keeps receiving traffic and returning errors to callers. Ejection based on the provider's real response is the single most impactful behavior and delivers value on its own.

**Independent Test**: Point the gateway at a backend that returns 429/403/401/5xx. Confirm that within one request the node is removed from the routable pool and subsequent requests are routed to other backends.

**Acceptance Scenarios**:

1. **Given** a healthy backend, **When** it returns a 429 on a real request, **Then** the backend is moved out of the routable pool and given a short cooldown before it can be tried again.
2. **Given** a healthy backend, **When** it returns a 403 (forbidden/quota), **Then** the backend is ejected with a longer cooldown than a 429.
3. **Given** a healthy backend, **When** it returns a 401 (auth failure), **Then** the backend is moved to long isolation and an alert is raised, because auth failure is unlikely to self-heal.
4. **Given** a backend that returns 400/404/422 on a request, **Then** the error is passed back to the caller and the backend's status and failure count are unchanged.
5. **Given** a backend that has failed on several consecutive real requests, **When** the consecutive-failure count reaches the threshold, **Then** the backend is moved to long isolation regardless of the specific error code.

---

### User Story 2 - Progressive recovery of a cooled-down backend (Priority: P1)

After a backend's cooldown expires, the gateway must not immediately restore it to full traffic. It should first send a limited trial (probe) of real traffic, and only restore normal weight after the trial succeeds. If the trial fails, the cooldown lengthens.

**Why this priority**: Without progressive recovery, a still-broken backend is re-flooded the instant its cooldown expires, producing a flap loop. This is required for ejection to be safe in production.

**Independent Test**: Eject a backend, wait for its cooldown, confirm it receives only limited trial traffic, then confirm that a successful trial restores it and a failed trial re-ejects it with a longer cooldown.

**Acceptance Scenarios**:

1. **Given** an isolated backend whose cooldown has expired, **When** the gateway reconsiders it, **Then** it enters a half-open probing state with limited weight and receives a single trial of real traffic.
2. **Given** a probing backend, **When** the trial request succeeds, **Then** the backend moves to a degraded state, is observed briefly, and is restored to healthy; its consecutive-failure count resets to zero.
3. **Given** a probing backend, **When** the trial request fails, **Then** the backend returns to isolation with its cooldown doubled (up to the per-code cap).
4. **Given** any backend leaving isolation, **When** its cooldown is applied, **Then** a random jitter is added so that many backends do not all recover at the same instant.

---

### User Story 3 - Minimum-availability failsafe (Priority: P1)

If too many backends are ejected at once, the gateway must force-recover the most promising candidates so that a minimum number of backends remain routable, even if their cooldowns have not expired.

**Why this priority**: A correlated outage (e.g. many nodes hitting quota near end of day) could eject nearly the whole fleet and leave the gateway unable to serve traffic. The failsafe guarantees a minimum serving capacity.

**Independent Test**: Force most backends into isolation. Confirm that when the routable count drops below the trigger threshold, the gateway pulls the best candidates back into probing until the recovery target is met, and that it does not thrash.

**Acceptance Scenarios**:

1. **Given** the routable backend count drops below the trigger threshold, **When** the gateway reconciles, **Then** it force-promotes the best isolated/quarantined candidates into probing until the recovery target is reached (or all backends are routable if the fleet is smaller than the target).
2. **Given** the failsafe promotes a candidate, **When** the candidate is being tried, **Then** it is protected from re-ejection for a minimum probing window.
3. **Given** the failsafe has just restored the routable count, **When** a short time passes, **Then** the failsafe does not immediately re-trigger (hysteresis prevents thrashing).
4. **Given** multiple failed candidates exist, **When** the failsafe selects candidates, **Then** it rotates by fewest-failures so it does not repeatedly pick the same dead node.

---

### User Story 4 - Budget and latency as soft routing signals (Priority: P2)

Daily-spend estimates and recent latency must bias routing weight, not eject nodes. A backend over its estimated daily limit keeps serving (at reduced weight) until the provider itself signals a real limit; a slow backend receives proportionally less traffic.

**Why this priority**: Budget estimates are known to be inaccurate, so they must never be a hard gate. Using them (and latency) as soft weighting steers traffic toward cheaper, faster nodes while still relying on real provider responses for hard ejection. Valuable, but the system is still correct without it.

**Independent Test**: Drive a backend's estimated spend past its daily limit without triggering a provider error. Confirm it stays routable at reduced weight. Raise a backend's latency and confirm its share of traffic drops.

**Acceptance Scenarios**:

1. **Given** a backend whose estimated spend exceeds its daily limit, **When** the provider has not returned a limiting response, **Then** the backend continues to receive traffic at a reduced (non-zero) weight and is never ejected on budget alone.
2. **Given** a backend below 80% of its daily limit, **Then** its budget factor does not reduce its weight.
3. **Given** two backends of equal budget standing but different recent latency, **When** routing decisions are made, **Then** the lower-latency backend receives a larger share of traffic.
4. **Given** a backend with a transient single error or elevated latency, **Then** it is soft-demoted (reduced weight) rather than fully ejected.

---

### User Story 5 - Periodic state reconciliation (Priority: P2)

On a fixed interval, the gateway runs a convergence pass that releases expired cooldowns, judges probing backends, applies soft demote/promote transitions, refreshes routing weights, and runs the failsafe check — so that state stays consistent even without a steady stream of live requests. It reuses the existing per-minute health check as one of its recovery signals.

**Why this priority**: Live traffic alone cannot recover an idle or fully-isolated fleet. A periodic reconciler makes recovery reliable and time-bounded. Supports the P1 stories but is not the primary deliverable.

**Independent Test**: With no live traffic flowing, isolate a backend and confirm that after its cooldown the reconciler moves it to probing and the periodic health check can drive recovery.

**Acceptance Scenarios**:

1. **Given** an isolated backend whose cooldown has expired, **When** the reconciler runs, **Then** the backend is moved to probing.
2. **Given** a degraded backend that has run cleanly for the observation window, **When** the reconciler runs, **Then** it is promoted back to healthy.
3. **Given** a healthy backend that has had no traffic for the idle window, **When** the reconciler runs, **Then** it is marked as due-for-probe so it is preferred for the next trial.
4. **Given** a backend running cleanly for the decay window, **When** the reconciler runs, **Then** its consecutive-failure count is decremented so the next backoff restarts from the base.

---

### Edge Cases

- **429 with Retry-After**: When a 429 response carries a Retry-After value, that value is used directly as the cooldown instead of the default 429 cooldown.
- **403 with explicit quota**: When a 403 body clearly indicates quota exhaustion, the cooldown is extended to the daily reset time rather than the default 403 cooldown.
- **Sparse HTTP history**: Backend HTTP codes are held in memory. The exit/recovery decision references recent codes only (the last ~20 requests or codes within the last ~5 minutes); older codes are not used to eject a node that has since recovered.
- **Misconfigured node returning 400**: A node whose own misconfiguration causes 400s will not be auto-ejected (400 is request-level). This is an accepted trade-off to avoid a burst of bad requests inflating a node's failure count.
- **Fleet smaller than the recovery target**: When the total backend count is below the failsafe recovery target, the failsafe restores all backends rather than an impossible count.
- **Simultaneous cooldown expiry**: Jitter on cooldown release prevents a thundering-herd of backends recovering in the same instant.
- **Failsafe flap**: Force-promoted candidates are shielded for a minimum window and the trigger has hysteresis, so the failsafe cannot spin (promote → mass-fail → drop below trigger → re-promote).

## Requirements *(mandatory)*

### Functional Requirements

#### Node state model

- **FR-001**: The gateway MUST model each backend in one of five states: Healthy, Degraded, Isolated, Probing (half-open), and Quarantine (long isolation).
- **FR-002**: The gateway MUST treat Healthy, Degraded, and Probing backends as routable, and Isolated and Quarantine backends as non-routable.
- **FR-003**: The routable count used for the failsafe MUST be the sum of Healthy + Degraded + Probing backends (probing nodes count as routable).

#### Exit rules (driven by real provider response)

- **FR-004**: The gateway MUST decide backend ejection based on the backend's real recent provider HTTP codes, not on the local spend ledger.
- **FR-005**: On a 429, 403, or 5xx from a backend, the gateway MUST move the backend to Isolated, start the code-specific cooldown, and increment its consecutive-failure count.
- **FR-006**: On a 401 from a backend, the gateway MUST move the backend directly to Quarantine, start a 15–30 minute cooldown, and raise an alert.
- **FR-007**: When a backend's consecutive-failure count reaches 5, the gateway MUST move it to Quarantine regardless of the specific error code.
- **FR-008**: On a 400, 404, or 422 from a backend, the gateway MUST pass the error back to the caller and MUST NOT change the backend's state or increment its failure count.
- **FR-009**: A backend exceeding its estimated daily limit without a provider limiting response MUST continue to be routed (soft-demoted), never ejected on budget alone.

#### Cooldown / TTL policy

- **FR-010**: The gateway MUST set cooldown length by the most recent HTTP code: 429 base 30s, 403 base 2min, 5xx base 30s, 401 → Quarantine 15–30min.
- **FR-011**: The gateway MUST cap cooldown backoff per code: 429/5xx capped at 5min, 403 capped at 30min.
- **FR-012**: When a 429 carries a Retry-After value, the gateway MUST use that value as the cooldown.
- **FR-013**: When a 403 body clearly indicates quota exhaustion, the gateway MUST extend the cooldown to the daily reset time.
- **FR-014**: The gateway MUST apply ±20% random jitter to every cooldown on release.
- **FR-015**: On a failed probe, the gateway MUST double the backend's cooldown (subject to the per-code cap).

#### Recovery (progressive)

- **FR-016**: When an Isolated backend's cooldown expires, the gateway MUST move it to Probing with limited weight and try it with a single trial of real traffic.
- **FR-017**: On a successful probe, the gateway MUST move the backend to Degraded, observe it for an observation window (~5min), then restore it to Healthy, and MUST reset its consecutive-failure count to zero.
- **FR-018**: On a failed probe, the gateway MUST return the backend to Isolated with a doubled cooldown.
- **FR-019**: The gateway MUST auto-promote a Degraded backend to Healthy after it runs cleanly for the observation window.
- **FR-020**: The gateway MUST mark a Healthy backend with no traffic for the idle window (~10min) as due-for-probe so it is preferred for the next trial.

#### Failsafe (minimum availability)

- **FR-021**: When the routable count drops below 3, the gateway MUST force-recover candidates until the routable count reaches 7 (or all backends if the fleet is smaller than 7).
- **FR-022**: The failsafe MUST promote candidates to Probing by ignoring remaining cooldown, since Probing counts as routable and the effect is immediate.
- **FR-023**: The failsafe MUST rank candidates by: shortest remaining cooldown first; transient errors (429/5xx) before quota (403); Quarantine/401 last; then lower estimated budget usage, lower recent latency, and fewer consecutive failures.
- **FR-024**: A failsafe-promoted candidate MUST be protected from re-ejection for a minimum probing window of 60s.
- **FR-025**: After the routable count reaches the target, the failsafe MUST apply hysteresis so it does not immediately re-trigger.
- **FR-026**: The failsafe MUST rotate candidate selection by fewest-failures to avoid repeatedly selecting the same dead node.

#### Weight & routing

- **FR-027**: The gateway MUST compute each backend's effective weight as base weight × health factor × budget factor × latency factor.
- **FR-028**: The budget factor MUST be 1.0 below 80% usage, decay linearly to 0.3 across 80–95%, decay to 0.05 across 95–99%, and hold at 0.05 (never 0) at ≥99%.
- **FR-029**: The latency factor MUST reduce weight when recent latency exceeds 8s, scaled as 8000 / recent-latency-ms, with a floor of 0.3.
- **FR-030**: Probing backends MUST route at weight 1 (rate-limited trial); Degraded backends MUST route at a reduced weight (×0.2–0.5).
- **FR-031**: The gateway MUST select backends using smooth weighted round-robin so low-weight backends are not starved.

#### Reconciliation loop

- **FR-032**: The gateway MUST run a reconciliation pass on a fixed 60s interval, aligned with the existing per-minute health check.
- **FR-033**: Each reconciliation pass MUST execute in order: (1) release expired cooldowns to Probing, (2) judge probing backends by their latest real result, (3) apply soft demote/promote transitions, (4) refresh effective weights, (5) run the failsafe check.
- **FR-034**: The reconciler MUST decrement a backend's consecutive-failure count after it runs cleanly for the decay window (~5min) so the next backoff restarts from the base.
- **FR-035**: The gateway MUST be able to use the existing per-minute health check (`check --health`) as a recovery signal for non-routable backends.

#### State fields & observability

- **FR-036**: Each backend MUST maintain the fields needed to support the above rules: current state, last HTTP code, entered-at time, cooldown-until time, consecutive-failure count, last-success time, estimated spend and daily limit, recent latency, and any Retry-After value.
- **FR-037**: The gateway MUST reference recent HTTP codes (the last ~20 requests or codes within the last ~5 minutes) rather than a single stale code when judging a backend.
- **FR-038**: The gateway MUST raise an alert when a backend enters Quarantine (401 auth failure or repeated failures).

### Key Entities *(include if feature involves data)*

- **Backend node**: An upstream provider endpoint the gateway routes to. Attributes: current state, last HTTP code, recent HTTP code history (in memory), entered-at, cooldown-until, consecutive-failure count, last-success time, estimated daily spend, daily limit, recent latency, Retry-After. Two large-quota nodes and ~40 small-quota nodes exist today.
- **Node state**: One of Healthy, Degraded, Isolated, Probing, Quarantine — determines routability and weight treatment.
- **Effective weight**: The routing weight derived from base weight and the health, budget, and latency factors; consumed by the smooth weighted round-robin selector.
- **Reconciliation pass**: A periodic (60s) convergence step that transitions node states, refreshes weights, and enforces the failsafe.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A backend that returns a node-level failure (429/403/401/5xx) stops receiving new traffic within one request of that response.
- **SC-002**: When a caller sends a request-level bad input (400/404/422), the affected backend's state and failure count are unchanged 100% of the time.
- **SC-003**: A recovered backend is never restored directly to full traffic — it always passes through a limited trial first, and a failed trial lengthens its cooldown.
- **SC-004**: The gateway maintains at least 3 routable backends whenever 3 or more backends physically exist, even during a correlated outage.
- **SC-005**: The failsafe does not thrash: after restoring the routable count it does not re-trigger within its hysteresis window, and it does not repeatedly select the same dead node.
- **SC-006**: A backend that has exceeded its estimated daily limit but has not received a limiting provider response continues to serve traffic (at reduced weight) and is never taken out of the pool on budget alone.
- **SC-007**: When many backends recover at once, their recovery times are spread out (no single-instant thundering herd) due to cooldown jitter.
- **SC-008**: Traffic measurably shifts toward lower-latency backends when latency across the fleet differs significantly.
- **SC-009**: An isolated or idle fleet recovers without operator intervention within a bounded time driven by the 60s reconciliation loop and the per-minute health check.

## Assumptions

- The existing per-request record of each backend's HTTP code and latency is available in memory and can be read at routing time; the recent-code window (last ~20 requests or ~5 minutes) is derived from that in-memory record.
- The daily-spend figure is an estimate with known inaccuracy and is therefore used only as a soft weighting/ordering signal, never as a hard ejection gate.
- A per-minute health check (`check --health`) already runs and can be reused as a recovery signal; the 60s reconciliation interval is aligned to it.
- "Node-level" 4xx codes are 401/403/429; "request-level" 4xx codes are 400/404/422. Any other 4xx defaults to request-level (passed to caller, no state change) unless later specified.
- Alerting delivery (channel, recipients) reuses whatever alerting mechanism the gateway already has; this spec only requires that an alert is raised on Quarantine.
- The numeric parameters (thresholds, TTLs, factors, windows) in this spec are the recommended defaults and are expected to be configurable rather than hard-coded, but exact configuration surface is an implementation detail.
- Persistence of node state across gateway restarts is out of scope for v1; state is held in memory and reconstructed from live traffic and health checks after a restart.
