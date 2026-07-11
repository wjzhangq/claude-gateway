# Implementation Plan: Backend Health Hardening

**Branch**: `003-backend-health-hardening` | **Date**: 2026-07-11 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/003-backend-health-hardening/spec.md`

## Summary

Harden the shipped `002` backend health machine along twelve review points, in three tiers. The P1 work makes the failsafe **event-driven** (evaluated on every transition into a non-routable state and synchronously when `Pick` finds no candidate, not only on the 60s tick); threads the **probe-observed HTTP code + Retry-After** through the same `computeTTL` the passive path uses; stops **billable inference probes** to quota-isolated backends and shrinks any real probe to `max_tokens=1` with its cost attributed; unifies **startup validation with the runtime probe fallback**; replaces the hardcoded **00:00-CST quota clock** with a per-backend / long-TTL policy; and adds the **model-scoped error-validity rule** (only sonnet/haiku/opus errors count against a Claude backend). P2/P3 add a windowed error-rate signal, exponential auth-quarantine backoff, a bounded budget-aware failover chain, Retry-After min/max clamping, reachable/documented `degraded` semantics, and observability (split cumulative vs consecutive counters, per-backend transition/probe-cost metrics, and a compact per-request **error-reason** persisted to `usage_logs`).

The change stays localized to `internal/proxy/balancer.go` (state machine, TTL, failsafe, windowed signal, metrics), `internal/proxy/handler.go` (model-family gate on RecordResult, bounded failover, error-reason capture), `cmd/check/main.go` + the `/admin/api/backends/health` endpoint (carry observedCode/Retry-After, skip quota-isolated, minimal probe), `internal/stats` + `internal/model` + `internal/db` (one additive `error_reason` column + migration `{36}`), and a new read-only `/admin/api/backends/metrics` surface. No new dependency; the only new storage is one nullable text column.

## Technical Context

**Language/Version**: Go 1.24

**Primary Dependencies**: standard library (`net/http`, `sync`, `sync/atomic`, `math/rand`, `time`, `strconv`, `strings`); `gin-gonic/gin` (admin endpoints); `sirupsen/logrus` (logging); `mattn/go-sqlite3` via the existing `internal/db` layer. No new dependencies.

**Storage**: One additive column `error_reason TEXT NOT NULL DEFAULT ''` on `usage_logs` (SQLite), added as migration `{36}` (next free version; current max is `{35}`). All health/state/metrics remain in memory on `Backend`/`LoadBalancer` as in 002 — metric counters are new atomic fields, not persisted.

**Testing**: `go test ./...`. Extend `internal/proxy/balancer_test.go` and `state_test.go` (state transitions, windowed rate, event-driven failsafe, auth backoff, Retry-After clamp, probe-code TTL) and `handler`-level tests (model-family gate, bounded failover, error-reason mapping). Time and randomness are already injected via `lb.nowFn`/`lb.rng`.

**Target Platform**: Linux server (single gateway process; also darwin for dev).

**Project Type**: Single Go project — HTTP proxy/load-balancer with companion CLIs under `cmd/`.

**Performance Goals**: `Pick` stays O(n) over ~42 backends with no per-call allocation; the event-driven failsafe hook must not hold `lb.mu` for the write path longer than the current reconcile pass. Error-reason capture stays on the existing async `stats.Collector` path — no added DB write on the hot request path. Ejection still takes effect within one request.

**Constraints**: All new backend fields use `atomic.*`; the windowed error-rate signal reads the existing `statusMu`-guarded ring buffer (no new lock). The event-driven failsafe must be re-entrant-safe with the 60s reconcile (reuse `lastFailsafeAt` hysteresis). No change to proxied request/response bytes. Budget stays a soft factor (never gates). `error_reason` is capped at 32 bytes and only written for non-2xx outcomes.

**Scale/Scope**: ~42 backends. Feature touches ~7 files; the majority of logic remains in `balancer.go`. One DB migration, one new admin read endpoint.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

The project constitution (`.specify/memory/constitution.md`) is an unfilled template — no ratified principles to gate against. Applying the same implicit conventions used for 002:

- **Localized change / no new abstractions**: PASS — extends existing `Backend`/`LoadBalancer`/`stats.Record` types. One new admin endpoint and one additive column; no new package or dependency.
- **Testability**: PASS — state/TTL/windowed-rate/failsafe logic remains pure functions of backend fields + injected `now`/`rng`. Model-family classification is a pure string function reusing `isClaudeModel`.
- **Observability**: PASS — keeps the `[backend:%s] ...` log pattern, adds metric counters and a persisted error-reason for post-hoc analysis (the explicit US12 goal).
- **Backward compatibility**: PASS with note — `BackendInfo.State` enum and existing admin endpoints are preserved; the health payload gains **optional** `observed_code`/`retry_after` fields (older `check` binaries omitting them fall back to today's behavior); the `error_reason` column defaults to `''`.

No violations. Complexity Tracking table omitted.

## Project Structure

### Documentation (this feature)

```text
specs/003-backend-health-hardening/
├── plan.md              # This file
├── research.md          # Phase 0 output
├── data-model.md        # Phase 1 output
├── quickstart.md        # Phase 1 output
├── contracts/           # Phase 1 output
│   ├── balancer-api.md      # internal Go surface (failsafe, windowed rate, metrics, TTL)
│   ├── admin-endpoints.md   # health payload additions + new /metrics endpoint
│   └── error-reason.md      # reason-code vocabulary + usage_logs column contract
└── tasks.md             # Phase 2 output (/speckit-tasks — NOT created here)
```

### Source Code (repository root)

```text
internal/proxy/
├── balancer.go          # PRIMARY: event-driven failsafe (FR-001..004); shared computeTTL
│                        #   for probe path (FR-005..007); per-backend quota-reset policy
│                        #   (FR-013..014); windowed error-rate signal (FR-018..019);
│                        #   auth-quarantine exponential TTL (FR-020..021); Retry-After
│                        #   clamp (FR-025..026); degraded trigger fix (FR-027); metric
│                        #   counters + cumulative-error field (FR-028..030)
├── balancer_test.go     # extend: windowed rate, event failsafe, auth backoff, TTL clamp
├── state_test.go        # extend: probe-code TTL, degraded reachability, quota policy
├── handler.go           # model-family gate before RecordResult (FR-015..017); bounded
│                        #   budget-aware failover chain (FR-022..024); classify + emit
│                        #   error_reason on non-2xx
├── handler_test.go / fixes_test.go  # extend: model-family gate, failover deadline
└── errorreason.go       # NEW (small): ErrorClass/HTTP -> compact reason code (<=32B)

cmd/check/
└── main.go              # health payload carries observed_code + retry_after; skip billable
                         #   probe for quota-isolated backends; probe max_tokens=1 (FR-008..010)

cmd/server/
└── main.go              # /admin/api/backends/health: accept observed_code/retry_after and
                         #   route through shared TTL; NEW GET /admin/api/backends/metrics

internal/stats/
└── collector.go         # Record gains ErrorReason; recordToLog maps it

internal/model/
└── model.go             # UsageLog gains ErrorReason `db:"error_reason"`

internal/db/
├── db.go                # migration {36}: ALTER TABLE usage_logs ADD COLUMN error_reason;
│                        #   add error_reason to usage_logs CREATE + BatchInsertUsageLogs
└── stats.go             # include error_reason in INSERT column list

config/
└── config.go            # optional: per-backend quota_reset + tunables (default-in-code)
```

**Structure Decision**: Single Go project, existing layout — an in-place evolution of the 002 load balancer, mirroring that plan's structure. The bulk of logic stays in `internal/proxy/balancer.go`; `handler.go` supplies the model-family signal and bounded failover; `cmd/check` + the health admin endpoint carry the richer probe result; `internal/stats`/`internal/model`/`internal/db` add the single `error_reason` column. One new tiny file `errorreason.go` isolates the reason-code vocabulary. No new packages beyond that file.

## Complexity Tracking

No constitution violations — section intentionally empty.
