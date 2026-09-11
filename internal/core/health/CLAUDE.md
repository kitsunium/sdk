# internal/core/health/

## Purpose

The contract for the three questions an orchestrator asks — **startup**,
**readiness**, **liveness** — and the checks that answer them (ADR 0060).

The domain exists because the industry answers two of those questions with one
mechanism, and that conflation has a production signature: a dependency slows,
every replica's liveness probe fails because its check talks to that dependency,
the orchestrator kills them all, and the survivors take the redistributed load
and fail faster. **The probe caused the outage.**

## Surface

| Item | Role |
|---|---|
| `Check func(ctx context.Context) error` | a **readiness or startup** check. It receives a context because it is expected to talk to something outside this process. |
| `SelfCheck func() error` | a **liveness** check. It receives **no context, on purpose** — see below. |
| `Health` | registry + probe answering; implementations MUST be concurrency-safe. |
| `Probe` | `ProbeStartup` / `ProbeReadiness` / `ProbeLiveness`. |
| `Status`, `Worst`, `Status.Serving()` | the aggregate verdict and its combination rule. `Serving` is true for exactly `StatusDegraded` and `StatusHealthy`; a `Status` outside the three does not serve. |
| `ResultValue` | one check's outcome, carried into the report. |

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
cd internal/core && GOWORK=off go test -race ./health/...
```
