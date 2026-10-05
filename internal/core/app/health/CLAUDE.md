<!-- updated: 2026-10-04T11:15:00Z -->
# internal/core/app/health/

## Purpose

The contract for the three questions an orchestrator asks — **startup**,
**readiness**, **liveness** — and the checks that answer them (ADR 0060).

The domain exists because the industry answers two of those questions with one
mechanism, and that conflation has a production signature: a dependency slows,
every replica's liveness probe fails because its check talks to that dependency,
the orchestrator kills them all, and the survivors take the redistributed load
and fail faster. **The probe caused the outage.**

**The port is generated from the design** (ADR 0163): `Health` is declared,
with its doc comment, under `ports:` in `design/app/health.yaml`, and `kit gen`
writes it into `design_gen.go`. A port or its doc comment changes in the
design, then `kit gen`, then `make api` — never in `design_gen.go`, whose
header digests `make api-check` verifies. It moved there from `health.go`,
content moved and never deleted.

## Surface

| Item | Role |
|---|---|
| `Check func(ctx context.Context) error` | a **readiness or startup** check. It receives a context because it is expected to talk to something outside this process. |
| `SelfCheck func() error` | a **liveness** check. It receives **no context, on purpose** — see below. |
| `StartupCheckValue` / `ReadinessCheckValue` / `LivenessCheckValue` | one registration: a `Name`, the body (`Check`, or a `SelfCheck` for liveness) and a `Timeout`; readiness adds `NonCritical` and `MaxAge`. |
| `Health` | registry + probe answering — `AddStartup` / `AddReadiness` / `AddLiveness` / `Probe` / `Drain` (one-way: readiness reports not-ready from then on); implementations MUST be concurrency-safe. |
| `Probe` | `ProbeStartup` / `ProbeReadiness` / `ProbeLiveness`. |
| `Status`, `Worst`, `Status.Serving()` | the aggregate verdict and its combination rule. `Serving` is true for exactly `StatusDegraded` and `StatusHealthy`; a `Status` outside the three does not serve, and `Worst` ranks it with `StatusUnhealthy`, so it cannot fold into a healthier verdict. |
| `ResultValue` | one check's outcome, carried into the report; `Age(now)` says how stale it is. |
| `ReportValue` | one probe's whole answer: the `Probe`, the aggregate `Status`, `At`, and the `Results` that produced it. |

## What each registration may express

The three registrations differ in what they are ALLOWED to express, and the
differences are absences rather than documentation (this note sat above
`StartupCheckValue` in `health_check.go` until kit came to declare the three
types in `decl_gen.go` — ADR 0170):

|  | Startup | Readiness | Liveness |
|---|---|---|---|
| body | `Check` (ctx) | `Check` (ctx) | `SelfCheck` (none) |
| may call out | yes | yes | no — no ctx to bound it |
| `NonCritical` | absent | present | absent |
| `MaxAge` (cache) | absent | present | absent |

Each absence is a decision recorded in the field set:

- Liveness has no ctx, so a dependency API cannot be assigned to it.
- Liveness has no `NonCritical`, because "somewhat irrecoverable" restarts
  nothing; the process is beyond saving or it is not.
- Liveness has no `MaxAge`, because a cached liveness answer means a dead
  process can keep reporting the "alive" it recorded before it died.
- Startup has no `NonCritical`, because a startup check that does not gate
  startup is not a startup check.
- Startup has no `MaxAge`, because a startup check runs until it passes once
  and then never runs again — the strongest cache there is.

## Why the two checks are different types

This is the load-bearing decision, and it is structural rather than
documentary. Every API that talks outside this process wants a
`context.Context`. A `SelfCheck` does not have one, so it **cannot hold a
dependency call without a closure that visibly throws the deadline away** — and
that is a line somebody has to write on purpose, in a diff, under review.

A single `Check` type plus a `Probe` argument was rejected for exactly this
reason: the argument is one token at a call site, with no type to disagree with
it, so the classic mis-wiring stays invisible.

What belongs in a `SelfCheck`: a supervised goroutine that stopped reporting, a
queue that has not drained, a deadlock detector, a broken in-memory invariant.
What never belongs: a database, a cache, a broker, an HTTP dependency, a DNS
lookup, or a mutex whose holder is waiting on one of those.

Same technique as `token`'s per-algorithm constructors and `session`'s absent
subject mutator: the mistake is made unwritable rather than forbidden.

## Sentinels (`0.2.29.*`)

| Code | Reason | When |
|---|---|---|
| `0.2.29.1` | `INVALID_CHECK` | empty name or nil body |
| `0.2.29.2` | `DUPLICATE_CHECK` | a name already registered for that probe |
| `0.2.29.3` | `UNKNOWN_PROBE` | a `Probe` value outside the three |
| `0.2.29.4` | `CHECK_PANICKED` | a check panicked; recovered, never propagated to the handler |

The engine's verdicts are declared here too, in the `0.3.59.*` range that was
allocated to `internal/service/app/health`, which raises them (ADR 0160 — a
code keeps its value when its declaration moves):

| Code | Reason | When |
|---|---|---|
| `0.3.59.1` | `CHECK_FAILED` | a check returned a plain error |
| `0.3.59.2` | `CHECK_TIMEOUT` | a check had not answered when its budget expired |
| `0.3.59.3` | `STALE_CACHE_WINDOW` | a readiness `MaxAge` above the service's `MaxCacheAge` |
| `0.3.59.4` | `STARTUP_PENDING` | readiness asked while a startup check has yet to pass |
| `0.3.59.5` | `DRAINING` | readiness asked after `Drain` |
| `0.3.59.6` | `NOTIFY_FAILED` | an opt-in `sd_notify` datagram was not delivered |
| `0.3.59.7` | `ASK_MISCONFIGURED` | an `Ask` refused before anything was dialled |
| `0.3.59.8` | `ASK_UNREACHABLE` | an `Ask` got no answer: the connection or the request failed |
| `0.3.59.9` | `ASK_TIMEOUT` | an `Ask` whose budget or caller ended first |
| `0.3.59.10` | `ASK_NOT_READY` | an `Ask` answered with a status other than 200 |

## Do NOT

- Register a dependency check on liveness. It is possible — through a closure
  that discards the deadline — and it is the failure this domain exists to
  prevent. If you do it, say why in the same commit.
- Replace a duplicate registration silently. A registration that disappears at
  the moment somebody thought they were adding coverage is worse than a refusal.
- Render anything but the `errs` **Public** half into a probe body: a probe
  endpoint is routinely exposed more widely than its authors assume.

## Verification

```
cd internal/core && GOWORK=off go test -race ./app/health/...
```

## Declarations

`decl_gen.go` is written by kit gen from the design (ADR 0170): the declarations of `Check`, `SelfCheck`, `StartupCheckValue`, `LivenessCheckValue`, `ReadinessCheckValue`, `Probe`, `ResultValue`, `ReportValue` and `Status` — each struct with every field, unexported ones included; `Status.Serving` and `Worst`, each one call of its unexported body, measured to inline with the body inlined into it. Every body stays hand-written, in the files this document names — each wrapper's under its unexported name.
