# Contract: LoadBalancer / Backend internal API

Package `internal/proxy`. These are the Go methods the request path (`handler.go`), the server wiring (`cmd/server/main.go`), and the reconcile loop rely on. Signatures marked *(existing)* must keep their current behavior for callers listed; **new** methods are added by this feature.

## Request-path methods (hot path — must stay lock-cheap)

### `(*LoadBalancer) Pick() *Backend` *(existing, behavior change)*
- Selects a routable backend using **smooth weighted round-robin** over `effectiveWeight()` (was: weighted random). (FR-031)
- Routable = state ∈ {Healthy, Degraded, Probing}. Returns `nil` when total effective weight is 0.
- Probing backends participate at weight 1.

### `(*LoadBalancer) PickExcluding(exclude map[string]bool) *Backend` *(existing, behavior change)*
- Same SWRR selection, skipping excluded names. Used by quota/insufficient-balance failover.

### `(*Backend) RecordResult(class ErrorClass)` *(existing, behavior change)*
- Drives exit rules on the **real** classified outcome. (FR-004..FR-008)
- `ErrRateLimit`/`ErrServer`/`ErrTransport` → Isolated with code-specific TTL, `consecErr++`. (FR-005)
- `ErrAuth` when code==401 → Quarantine + alert. (FR-006)
- 403 (currently folded into `ErrAuth`) → Isolated with 2min base TTL, NOT Quarantine. **Requires splitting `ErrAuth`** (see classification change below). (FR-005, FR-010)
- `consecErr` reaching 5 → Quarantine regardless of class. (FR-007)
- `ErrClient` (400/404/422) and `ErrCanceled` → no state change, no counter change. (FR-008)
- Must respect `probeShieldUntil`: a shielded Probing backend is not re-isolated. (FR-024)

### `(*Backend) RecordRequest(code int, latencyMs int64)` *(existing)*
- Unchanged ring-buffer recording. Now also updates `lastHTTPCode`, `lastLatencyMs`, and (on 2xx) `lastSuccessAt`.

### Classification change — `ClassifyError` *(existing, behavior change)*
Split the current `ErrAuth` (401/403) so 403 no longer disables like 401:
```
ErrAuth      -> 401 only         (→ Quarantine + alert)
ErrForbidden -> 403 only         (→ Isolated, 2min base, quota-body may extend to daily reset)
ErrRateLimit -> 429              (→ Isolated, 30s base, honor Retry-After)
ErrServer    -> 5xx              (→ Isolated, 30s base)
ErrClient    -> 400/404/422 &c.  (→ passthrough, no state change)
```

## Recovery / reconcile methods (new)

### `(*LoadBalancer) Reconcile(now time.Time)` **new**
- One convergence pass in the documented order (release → judge probing → soft up/down → refresh weights → failsafe). (FR-032, FR-033)
- Pure function of current state + clock; safe to call from a 60s ticker and from tests with an injected `now`.

### `(*LoadBalancer) SetHealthStatus(name string, healthy bool, latencyMs int64) bool` *(existing, behavior change)*
- Called by `check --health` sync (`/admin/api/backends/health`). (FR-035)
- `healthy=true` on Isolated/Quarantine → Probing (was: disabled→degraded). (FR-016, FR-035)
- `healthy=true` on Probing/Degraded → advances toward Healthy.
- `healthy=false` → demote per state machine (Healthy→Degraded→Isolated).

### `(*LoadBalancer) SetQuotaStatus(name string, exhausted bool, limit, usage float64) bool` *(existing)*
- Still records `quotaLimit`/`quotaUsage` for the **budget factor**. `exhausted=true` no longer forces weight 0; it feeds soft demotion only. (FR-009)
- **Behavior change**: budget never yields non-routable. Existing callers unaffected in signature.

### `(*Backend) SetState(s int32)` *(existing)*
- Extended to accept the new state constants; resets counters on Healthy.

## Observability

### `(*LoadBalancer) GetBackends() []BackendInfo` *(existing, extended)*
- `BackendInfo.State` string gains `"isolated"`, `"probing"`, `"quarantine"`.
- Add fields: `TTLUntil int64`, `ConsecFailures int64`, `LastHTTPCode int`, `BudgetFactor/LatencyFactor float64` (for the admin UI). Existing fields unchanged.

## Alerting

### `alert(backend, reason string)` **new (minimal)**
- Invoked on Quarantine entry (401 or consec≥5). (FR-038)
- v1: structured `log.Printf`/logrus warning at minimum; delivery channel reuses existing logging. Spec only requires an alert be raised.
