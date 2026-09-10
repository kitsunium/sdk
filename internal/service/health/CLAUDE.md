# internal/service/health/

## Purpose

The concrete `core/health.Health`: the check registry, per-check timeouts,
panic recovery, the staleness bound, the drain latch, and the HTTP handler
(ADR 0060).

## Contents

| File | Role |
|---|---|
| `health.go`, `entry.go` | the registry and one registered check |
| `inflight.go` | one execution of a check, shared by every probe waiting on it |
| `runner.go` | runs a check under its timeout, recovers a panic |
| `probe.go` | answers a probe: startup gating, drain latch, aggregation |
| `handler.go` | the HTTP surface; renders only the `errs` Public half |
| `handler_config.go` | `HandlerConfig` — the one knob the three handlers take |
| `body.go` | the wire shape of a probe response, and of one check inside it |
| `component.go` | the `lifecycle` bridge |
| `notify.go` | opt-in `sd_notify`, delegating to `service/proc/sdnotify` |
| `config.go` | timeouts and staleness, with their ADR 0031 clamps and refusals |

## The three behaviours worth knowing

**Startup gates liveness.** While any registered startup check has yet to pass,
liveness reports serving. A slow boot mistaken for a dead process is the failure
mode that makes teams delete their liveness probes altogether.

**Draining is a state, not a check result.** `Drain` makes readiness report
not-ready **permanently, without running a check**, while liveness keeps
answering. That is what lets an orchestrator take a replica out of rotation
*before* ADR 0043's drain signal begins, rather than racing it.

**A check that can hang is the failure this domain exists to prevent**, so every
`Check` runs under a timeout and a panicking check is recovered
(`CHECK_PANICKED`) rather than taking the probe handler with it.

## Sentinels (`0.3.59.*`)

`CHECK_FAILED` · `CHECK_TIMEOUT` · `STALE_CACHE_WINDOW` · `STARTUP_PENDING`

## Do NOT

- Let a check run unbounded. An unbounded check turns the health endpoint into
  a place the process can hang.
- Serve a cached result older than the configured staleness bound: a stale
  "healthy" is a lie with a timestamp.
- Write to stdout (ADR 0030).

## Verification

```
cd internal/service && GOWORK=off go test -race ./health/...
bazel test //internal/service/health:health_test
```

The Bazel target carries `data = ["//:audit_sources"]` because
`TestPackageNeverWaitsOnTheWallClock` parses this package's own sources — its
`audit_srcs` filegroup and the matching `//:audit_sources` entry are what make
those files reachable inside the sandbox (rule 12).
