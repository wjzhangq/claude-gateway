# Contract: Balancer Internal Go Surface (delta over 002)

Package `proxy`. Only additions/changes vs the 002 contract are listed.

## Event-driven failsafe (FR-001..004)

```go
// setIsolated / setQuarantine: after storing the non-routable state, if
// lb.routableCount() < failsafeTrigger, call lb.failsafe(now) synchronously.
// failsafe keeps its lastFailsafeAt (30s) hysteresis so tick + event callers
// don't thrash. It already promotes up to min(failsafeTargetCap, total) in one
// pass — one-shot, not one-per-cycle.

// pick(exclude) : if the SWRR scan yields best == nil, call lb.failsafe(now)
// once, then re-scan a single time before returning nil.
```

Guarantee: after any transition that drops routable below the trigger, `routableCount()` is restored to `min(failsafeTargetCap, total)` before the call returns (subject to available candidates), without waiting for the 60s tick. Promotions use only atomic writes and are safe under `lb.mu.RLock`.

## Probe result with observed code (FR-005..007)

```go
func (lb *LoadBalancer) SetHealthStatusDetailed(
    name string, healthy bool, latencyMs int64,
    observedCode int, retryAfterUnix int64) bool
```

- `healthy=false`: isolate via `lb.setIsolated(b, observedCode, retryAfterUnix, false, now)` — same `computeTTL` policy as the passive path. `observedCode == 0` → server-band base TTL.
- `healthy=true`: unchanged gated progression (isolated/quarantine → probing → degraded → healthy).
- `SetHealthStatus(name, healthy, latencyMs)` retained as a thin wrapper calling `...Detailed(name, healthy, latencyMs, 0, 0)` for compatibility.

## Model-scoped error validity (FR-015..017) — handler-side

```go
// isValidHealthModel reports whether an error on this model counts against a
// Claude backend's health. True only when the lowercased name contains
// "sonnet", "haiku", or "opus". Empty/undeterminable model => true (count it).
func isValidHealthModel(model string) bool
```

Handler: on non-2xx, only call `RecordResult*` (health-impacting) when `isValidHealthModel(reqModel)`; otherwise record the request in the ring buffer / usage log but leave state/consecErr/ttl untouched (equivalent to `ErrClient` "ignored").

## Windowed error-rate signal (FR-018..019)

```go
// windowedErrorRate returns (rate, sampleCount) over the last <=20 ring entries
// within 5 min, counting impacting codes (403/429/5xx/transport) as errors.
func (b *Backend) windowedErrorRate(now time.Time) (rate float64, samples int)
```

`softUpDown`: samples ≥ 8 and rate ≥ 0.5 → at least Degraded; rate ≥ 0.8 → Isolated (server band). A probing/degraded backend recovers normally once rate < 0.3.

## Auth-quarantine exponential TTL (FR-020..021)

```go
// setQuarantine(code=401): ttl = min(ttlBase401 << authQuarantineCount, authQuarantineMaxTTL); authQuarantineCount++.
// 2xx (recordResult ErrNone) or key_rotated signal resets authQuarantineCount and ttlUntil.
```

## Retry-After clamp (FR-025..026)

```go
// computeTTL / parseRetryAfter path: honored Retry-After clamped to
// [retryAfterMin=5s, retryAfterMax=30min]. <=0 / unparseable => code base TTL.
```

## Quota reset policy (FR-013..014)

```go
// Quota isolation (403-quota body or MarkQuotaExhausted): default policy sets
// ttlUntil = now + jitter(quotaIsolationTTL=6h). If the backend config sets
// QuotaReset, compute the per-backend reset instant instead.
```

## Metrics (FR-028..030)

```go
type BackendMetrics struct {
    Name                 string           `json:"name"`
    TotalErr             int64            `json:"total_err"`
    ConsecErr            int64            `json:"consec_err"`
    IsolationCountByCode map[string]int64 `json:"isolation_count_by_code"`
    FailsafePromotions   int64            `json:"failsafe_promotions"`
    ProbeCostUSD         float64          `json:"probe_cost_usd"`
    DwellSecondsByState  map[string]int64 `json:"dwell_seconds_by_state"`
    EstSpendAtFirst403USD float64         `json:"est_spend_at_first_403_usd"`
    LimitAtFirst403USD    float64         `json:"limit_at_first_403_usd"`
}

func (lb *LoadBalancer) Metrics() []BackendMetrics
```

`BackendInfo`: `ErrCount` now maps to `totalErr` (was `consecErr`); `ConsecFailures` stays `consecErr`.
