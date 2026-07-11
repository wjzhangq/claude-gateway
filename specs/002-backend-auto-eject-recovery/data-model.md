# Phase 1 Data Model: Backend Auto-Eject & Recovery

All state is in-memory on the existing `internal/proxy.Backend` struct. No database tables. Fields below extend the current struct; existing fields are noted as *(existing)*.

## Entity: Backend (extended)

| Field | Type | Existing? | Purpose | Maps to FR |
|---|---|---|---|---|
| `Name`, `URL`, `APIKey`, `Weight` | string/int | existing | identity + base weight | — |
| `client` | `*http.Client` | existing | per-backend HTTP client | — |
| `state` | `atomic.Int32` | existing (extended enum) | current state: Healthy/Degraded/Isolated/Probing/Quarantine | FR-001 |
| `enteredAt` | `atomic.Int64` (unix s) | **new** | when current state was entered; drives 5min clean / 10min idle windows | FR-017, FR-019, FR-020, FR-034 |
| `ttlUntil` | `atomic.Int64` (unix s) | **new** | cooldown expiry for Isolated/Quarantine; 0 = none | FR-010..FR-015, FR-016 |
| `retryAfter` | `atomic.Int64` (unix s) | **new** | absolute expiry parsed from a 429 `Retry-After`; 0 = none | FR-012 |
| `lastHTTPCode` | `atomic.Int64` | **new** (derivable) | most recent response code; drives exit decision | FR-004, FR-005..FR-008 |
| `consecErr` | `atomic.Int64` | existing | consecutive health-impacting failures; →Quarantine at ≥5; backoff driver | FR-007, FR-015 |
| `consecOK` | `atomic.Int64` | existing | consecutive successes for Degraded→Healthy | FR-017, FR-019 |
| `lastErr` | `atomic.Int64` (unix s) | existing | timestamp of last error | — |
| `lastSuccessAt` | `atomic.Int64` (unix s) | **new** (rename/extend of lastErr semantics) | last 2xx; drives clean-window promote + idle detection | FR-019, FR-020, FR-034 |
| `lastLatencyMs` | `atomic.Int64` | existing | most recent latency; latency factor | FR-029 |
| `probeShieldUntil` | `atomic.Int64` (unix s) | **new** | failsafe-promoted node protected from re-isolation until this time | FR-024 |
| `currentWeight` | `int` (guarded by `selMu`) | **new** | SWRR running cursor | FR-031 |
| `quotaExhausted` | `atomic.Bool` | existing | external hard quota flag (kept, but no longer the primary gate) | FR-009 |
| `quotaLimit`, `quotaUsage`, `quotaCheckedAt` | `atomic.Int64` | existing | budget factor inputs (cents) | FR-009, FR-028 |
| `validationFailed` | `atomic.Bool` | existing | startup validation failure; never auto-recovers | — |
| `statusEntries`, `statusCodeDist`, `statusCapacity`, `statusMu` | ring buffer | existing | recent code/latency window (last ~20 / ~5min) | FR-037 |

### Derived / not stored
- **effectiveWeight** = `base × healthFactor(state) × budgetFactor(usage) × latencyFactor(latency)`; Probing pins to 1; Isolated/Quarantine/validationFailed → 0. (FR-027..FR-030)
- **routableCount** = count of backends in {Healthy, Degraded, Probing}. (FR-003)
- **recentCodes(window)** = codes from `statusEntries` within last 20 entries or 5 minutes. (FR-037)

## Entity: State (enum on `state atomic.Int32`)

```
stateHealthy    = 0   // routable, full weight
stateDegraded   = 1   // routable, weight ×0.2–0.5
stateDisabled   = 2   // (retained for back-compat mapping; superseded by isolated/quarantine)
stateIsolated   = 3   // NOT routable, TTL cooldown
stateProbing    = 4   // routable, weight = 1 (half-open trial)
stateQuarantine = 5   // NOT routable, long TTL + alert
```

### State transitions

| From | Event | To | Side effects |
|---|---|---|---|
| Healthy | 429/403/5xx/transport (real code) | Isolated | set `ttlUntil` by code (R4), `consecErr++` |
| Healthy | 401 | Quarantine | `ttlUntil`=15–30min, **alert** |
| Healthy | consecErr ≥ 5 | Quarantine | **alert** |
| Healthy | budget/latency soft signal (reconcile) | Degraded | weight reduced only |
| Degraded | 429/403/5xx | Isolated | as above |
| Degraded | clean 5min (reconcile) | Healthy | reset consecOK |
| Isolated | `now > ttlUntil` (reconcile) | Probing | weight=1, single trial |
| Isolated | consecErr ≥ 5 / was 401 | Quarantine | **alert** |
| Probing | trial success | Degraded → (5min) Healthy | `consecErr=0`, `consecOK=0` |
| Probing | trial failure | Isolated | `ttlUntil = min(ttl×2, cap)` |
| Quarantine | `now > ttlUntil` / failsafe (reconcile) | Probing | **alert** on entry only; single trial |
| Isolated/Quarantine | failsafe force-promote (routable<3) | Probing | ignore TTL, set `probeShieldUntil=now+60s` |
| any request-level 4xx (400/404/422) | — | *(unchanged)* | error returned to caller; no counter change |

All TTLs get ±20% jitter on set/release (FR-014).

## Entity: Reconcile pass (behavior, not stored)

Ordered steps per 60s tick (FR-033):
1. **Release**: Isolated/Quarantine with `now > ttlUntil` → Probing.
2. **Judge probing**: by latest recent result → Degraded (success) or Isolated ×2 (fail).
3. **Soft up/down**: Degraded clean 5min → Healthy; Healthy soft-signal → Degraded; consec decay after 5min clean; Healthy idle >10min → mark due-for-probe.
4. **Refresh weights**: recompute factor cache.
5. **Failsafe**: if routable < 3 → promote ranked candidates to Probing until routable ≥ min(7, total), respecting shield + hysteresis + fewest-failures rotation.

## Validation rules

- `budgetFactor` MUST return a value in `[0.05, 1.0]` and MUST NOT return 0 (FR-009, FR-028).
- `latencyFactor` MUST return a value in `[0.3, 1.0]` (FR-029).
- A backend in Isolated/Quarantine MUST have `effectiveWeight()==0` (FR-002).
- A Probing backend MUST have `effectiveWeight()==1` regardless of budget/latency (FR-030).
- `ttlUntil` MUST never exceed the per-code cap except the 403-quota→daily-reset case (FR-011, FR-013).
- Request-level 4xx MUST NOT mutate `state`, `consecErr`, or `ttlUntil` (FR-008; assert in tests).
