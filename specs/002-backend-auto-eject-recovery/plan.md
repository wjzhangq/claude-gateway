# Implementation Plan: Backend Auto-Eject & Recovery

**Branch**: `002-backend-auto-eject-recovery` | **Date**: 2026-07-11 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/002-backend-auto-eject-recovery/spec.md`

## Summary

Replace today's 3-state backend health model (healthy / degraded / disabled) with a 5-state machine (Healthy / Degraded / Isolated / Probing / Quarantine) driven by the **real provider HTTP code** of recent requests, plus a 60s reconciliation loop that releases TTL-expired isolations into half-open probing and enforces a minimum-routable failsafe. Budget (estimated daily spend) and latency become **soft weight factors only** — never a hard gate. The existing per-minute `cmd/check --health` probe is reused as the recovery signal that promotes non-routable backends.

The change is localized to `internal/proxy/balancer.go` (state machine + weighting + selection), with small hooks in `internal/proxy/handler.go` (record real code + Retry-After at request time), `cmd/server/main.go` (start the reconcile ticker; the health/quota admin endpoints already exist), and `cmd/check/main.go` (surface Retry-After / recent-code context — optional, minor). No new storage; all state stays in memory as it is today.

## Technical Context

**Language/Version**: Go 1.24

**Primary Dependencies**: standard library (`net/http`, `sync`, `sync/atomic`, `math/rand`, `time`); `gin-gonic/gin` for the admin endpoints; `sirupsen/logrus` for logging. No new dependencies.

**Storage**: N/A for feature state — backend health/TTL/failure state is held in memory on the `Backend` struct (as today). Per-backend spend estimate is sourced from the externally-probed `quotaUsage`/`quotaLimit` fields and the configured per-backend `daily_limit`; no new DB tables.

**Testing**: `go test ./...` — extend `internal/proxy/balancer_test.go` (table-driven, `package proxy_test`, uses the existing `makeBackends` helper) with state-transition, TTL, weight-factor, and failsafe cases.

**Target Platform**: Linux server (single gateway process; also runs on darwin for dev).

**Project Type**: Single Go project — HTTP proxy/load-balancer service with companion CLIs under `cmd/`.

**Performance Goals**: Selection stays on the hot request path — `Pick` must remain O(n) over backends (~42 nodes) with no per-call allocation, matching the current two-pass scan. Reconcile runs off the hot path every 60s. Ejection must take effect within one request (SC-001).

**Constraints**: All backend-state mutation must remain lock-safe under concurrent request handling (current code uses `atomic.*` per-field plus a `statusMu` for the ring buffer and `lb.mu` RWMutex for the slice). No behavior change to the request/response bytes; only routing and state. Budget must never zero out a backend (floor 0.05) or eject it (P1).

**Scale/Scope**: ~42 backends (2 large-quota + ~40 small-quota). Ring buffer already sized `max(50, 10×count)`. Feature touches ~4 files, majority in `balancer.go`.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

The project constitution (`.specify/memory/constitution.md`) is an unfilled template — no ratified principles to gate against. Applying the project's implicit engineering conventions instead:

- **Localized change / no new abstractions**: PASS — feature extends the existing `Backend`/`LoadBalancer` types rather than introducing a new subsystem. No new project, no new dependency.
- **Testability**: PASS — state machine and weighting are pure functions of backend fields + `now`; covered by unit tests in the existing test file. Time is the only external input; will be injected so TTL/jitter logic is testable.
- **Observability**: PASS — every state transition already logs via `log.Printf("[backend:%s] ...")`; feature keeps that pattern and adds a Quarantine alert log.
- **Backward compatibility**: PASS with note — `BackendInfo.State` string and the `/admin/api/backends/health` + `/admin/api/backends/quota` endpoints are preserved; new states extend the string enum (see research R7 for the compatibility mapping the web UI relies on).

No violations. Complexity Tracking table omitted (nothing to justify).

## Project Structure

### Documentation (this feature)

```text
specs/002-backend-auto-eject-recovery/
├── plan.md              # This file
├── research.md          # Phase 0 output
├── data-model.md        # Phase 1 output
├── quickstart.md        # Phase 1 output
├── contracts/           # Phase 1 output (internal Go + admin endpoint contracts)
│   ├── balancer-api.md
│   └── admin-endpoints.md
└── tasks.md             # Phase 2 output (/speckit-tasks — NOT created here)
```

### Source Code (repository root)

```text
internal/proxy/
├── balancer.go          # PRIMARY: 5-state machine, TTL policy, weight factors,
│                        #   smooth weighted round-robin, reconcile pass, failsafe
├── balancer_test.go     # extend: state transitions, TTL+jitter, weight factors, failsafe
├── handler.go           # hook: record real HTTP code + parse Retry-After on 429;
│                        #   route request-level 4xx (400/404/422) straight back to caller
└── handler_test.go / fixes_test.go  # extend if request-path classification changes

cmd/server/
└── main.go              # start reconcile ticker (60s); health/quota admin endpoints
                         #   (/admin/api/backends/health, /admin/api/backends/quota) already wired

cmd/check/
└── main.go              # existing --health probe reused as recovery signal; optional:
                         #   forward Retry-After / recent-code hints (minor, may be deferred)

config/
└── config.go            # optional: expose tunable params (TTLs, thresholds) — default-in-code,
                         #   config override is a nice-to-have per Assumptions
```

**Structure Decision**: Single Go project, existing layout. The feature is an in-place evolution of the load balancer. `internal/proxy/balancer.go` holds the state machine, weighting, and reconcile logic; `internal/proxy/handler.go` supplies the real-code + Retry-After signal from the request path; `cmd/server/main.go` starts the periodic reconcile loop and hosts the already-existing admin sync endpoints that `cmd/check` posts to. No new packages or directories.

## Complexity Tracking

No constitution violations — section intentionally empty.
