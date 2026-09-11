# ADR 0060 — health: liveness and readiness are different questions, so they take different types

- **Status**: Accepted
- **Date**: 2026-09-10
- **Deciders**: SDK maintainers
- **Related**: [ADR 0050](0050-sdk-lifecycle-domain.md) (`lifecycle`, whose drain this reports), [ADR 0043](0043-drain-is-a-signal-not-a-cancellation.md) (draining is announced), [ADR 0031](0031-policy-zero-values-are-never-inert.md), [ADR 0018](0018-sdk-cross-platform-portability.md)

## Context

An orchestrator asks three questions, and the industry answers two of them with
one mechanism. That conflation has a name in production: a dependency slows
down, every replica's liveness probe fails because its check talks to that
dependency, the orchestrator kills them all, the survivors take the redistributed
load and fail faster. **The probe caused the outage.**

The distinction is not subtle and it is not a matter of taste:

- **startup** — still coming up. Do not route to it, and **do not kill it**.
- **readiness** — can serve traffic *now*. A failing dependency belongs here.
- **liveness** — is this process **irrecoverable**, such that only a restart
  can help. A saturated service is not a dead service.

Every deployment guide says this. Documentation has not been enough, because
the two probes accept the same shape of function, so the wrong wiring is one
copy-paste away and looks correct in review.

## Decision

1. **The two probes take different types, so the wrong wiring does not
   compile.**
   - `Check func(ctx context.Context) error` — readiness and startup. It gets a
     context because it is expected to talk to something.
   - `SelfCheck func() error` — liveness. **It takes no context at all.**

   Every API that talks outside this process wants a context. A `SelfCheck`
   therefore cannot hold a dependency call without a closure that *visibly
   throws the deadline away* — which is a decision somebody has to write down,
   not a slip. What belongs in a `SelfCheck` is process-local evidence: a
   supervised goroutine that stopped reporting, a queue that has not drained, a
   broken in-memory invariant. What never belongs: a database, a cache, a
   broker, DNS.

   This is the same technique as `token`'s per-algorithm constructors and
   `session`'s absent subject mutator — the error is made unwritable rather
   than forbidden.

2. **Startup gates liveness, not the other way round.** While any registered
   startup check has yet to pass, the liveness probe reports serving, so a slow
   boot is never mistaken for a dead process — the failure mode that makes
   teams delete their liveness probes entirely.

3. **Draining is a state, not a check result.** `Drain` makes readiness report
   not-ready **permanently and without running a check**, while liveness keeps
   answering. That is what lets an orchestrator take a replica out of rotation
   *before* ADR 0043's drain signal starts, rather than racing it.

4. **A check that can hang is the failure this domain exists to prevent**, so
   every `Check` runs under a timeout and a panicking check is recovered and
   reported (`CHECK_PANICKED`) rather than taking the probe handler with it.

5. **ADR 0031, both halves.** A `Health` with no checks registered is legitimate
   — the process is alive and serving. A check registered with an empty name or
   a nil body is **refused**, and so is a duplicate name within one probe:
   silently replacing a check would make a registration disappear at the moment
   somebody thought they were adding coverage.

6. **The HTTP body carries only the `errs` Public half.** A probe endpoint is
   routinely exposed more widely than its authors assume, and a raw driver
   error names hosts, ports and sometimes credentials.

## Consequences

- `lifecycle` and `health` compose without either importing the other's
  vocabulary: the application calls `Drain` from its own shutdown hook.
- Blocks `0.2.29.*` (port) and `0.3.59.*` (runner, timeouts, staleness).
- A caller who genuinely wants a dependency check on liveness can still write
  one — through a closure that discards the deadline. It is possible, visible,
  and reviewable, which is the point.

## Why not

- **One `Check` type and a `Probe` argument.** Rejected: that is exactly the
  shape that makes the mistake invisible. The argument is one token, at a call
  site, with no type to disagree with it.
- **Fail liveness when a dependency fails.** Rejected — §Context. It converts a
  dependency incident into a restart storm.
- **Let a check run without a timeout when the caller omits one.** Rejected: an
  unbounded check turns the health endpoint into a place the process can hang,
  which is the exact failure this domain exists to prevent.

## References

- `internal/core/health/health.go` (the two function types and their contracts)
- `internal/service/health/{runner,handler,probe}.go`
- ADR 0050 (`lifecycle`), ADR 0043 (the drain signal this precedes)
