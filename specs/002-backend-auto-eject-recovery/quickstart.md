# Quickstart: Backend Auto-Eject & Recovery

Validation guide proving the feature works end-to-end. Assumes the Go gateway builds and the existing `cmd/check` binary runs. All state lives in `internal/proxy` (`Backend`, `LoadBalancer`); see [data-model.md](./data-model.md) and [contracts/balancer-api.md](./contracts/balancer-api.md).

## Prerequisites

- Go 1.24 toolchain.
- A `config.yaml` with several backends (mirror production: a couple of high-weight nodes + a batch of small ones).
- Gateway server buildable: `go build ./cmd/server`
- Check tool buildable: `go build ./cmd/check`

## Unit-level validation (fast, no network)

The state machine is a pure function of outcomes + clock. Drive it directly.

```bash
go test ./internal/proxy/ -run 'State|Reconcile|Failsafe|Weight|TTL' -v
```

Expected coverage (add to `balancer_test.go` / a new `state_test.go`):
1. **Exit codes** — 429/403/5xx → Isolated with correct base TTL; 401 → Quarantine + alert; 400/404/422 → no state change, `consecFailures` unchanged. (FR-005..FR-008, FR-010)
2. **Retry-After** — 429 carrying `Retry-After: 120` sets TTL≈120s, not 30s. (FR-012)
3. **403 quota body** — 403 whose body flags quota → TTL to daily reset. (FR-013)
4. **Consec→Quarantine** — 5 consecutive failures → Quarantine regardless of code. (FR-007)
5. **Progressive recovery** — Isolated + expired TTL →(Reconcile)→ Probing; probe success → Degraded → (5min clean) → Healthy, `consecFailures=0`; probe fail → Isolated, TTL×2 (capped). (FR-016..FR-019)
6. **Jitter** — releasing many backends spreads TTLs (no identical `ttlUntil`). (FR-014)
7. **Budget factor** — usage <80% → 1.0; 80–95% → linear to 0.3; 95–99% → to 0.05; ≥99% → 0.05 (never 0, still routable). (FR-009, FR-028)
8. **Latency factor** — latency>8s → 8000/latency, floor 0.3. (FR-029)
9. **SWRR** — low-weight backend is not starved across N picks. (FR-031)
10. **Failsafe** — routable<3 force-promotes best candidates to Probing until ≥7 (or all); ranking order honored; shielded 60s; hysteresis prevents immediate re-trigger; rotation avoids same dead node. (FR-021..FR-026)

Use an injected `now time.Time` into `Reconcile(now)` so TTL/window tests are deterministic.

## Integration validation (with a fake upstream)

Stand up a stub HTTP server that returns a scripted status code, point one backend at it.

1. **Auto-eject**: script it to return `429`. Send one request through the gateway → the backend leaves the routable set within that one request; subsequent `Pick()` routes elsewhere. (SC-001)
2. **Passthrough 400**: script `400`. Send a request → caller receives 400, and `GET /admin/api/...` backend state is unchanged. (SC-002)
3. **Recovery via check**: after eject, run the per-minute probe manually:
   ```bash
   ./check --config config.yaml --health
   ```
   Stub now returns 200 → backend moves Isolated→Probing, then a successful real request promotes it toward Healthy. (SC-003, SC-009, FR-035)
4. **Failsafe**: script most backends to fail so routable<3 → confirm the gateway force-promotes candidates back to Probing and keeps ≥3 routable, without thrashing. (SC-004, SC-005)

## Reconcile loop wiring check

- Confirm `cmd/server/main.go` starts a 60s ticker calling `lb.Reconcile(time.Now())` (aligned with existing per-minute flush/health cadence). (FR-032)
- With no live traffic, isolate a backend and confirm it advances to Probing after its TTL on the next reconcile tick. (SC-009)

## Manual smoke against the admin API

```bash
# view state, TTLs, factors
curl -s -H "Authorization: Bearer $SECRET" http://127.0.0.1:$PORT/admin/api/backends | jq '.[] | {name, state, ttl_until, consec_failures, effective_weight}'
```

Expect states drawn from `healthy|degraded|isolated|probing|quarantine` and effective weights reflecting budget/latency factors.

## Done when

- All unit tests in the list pass.
- Integration eject + recovery + failsafe scenarios behave as described.
- `check --health` recovers an isolated backend on the next run.
- No backend is ever taken out of rotation on budget estimate alone (SC-006).
