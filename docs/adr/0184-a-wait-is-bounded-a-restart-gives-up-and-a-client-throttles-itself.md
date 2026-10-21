# ADR 0184 — a wait is bounded, a restart gives up, and a client throttles itself

- **Status**: Accepted
- **Date**: 2026-10-21
- **Accepted**: 2026-10-21, decided autonomously by the SDK maintainers for
  kitsunium/sdk#482. It can be revisited while the SDK is v0
  ([ADR 0040](0040-changing-a-published-shape-while-v0.md)): every symbol it
  adds is a field whose zero is the behaviour before it, or a constructor a
  later decision may change or withdraw.
- **Deciders**: SDK maintainers
- **Resolves**: [ADR 0026](0026-sdk-resilience-domain.md) Deferred, "wait mode
  for the bulkhead and the rate limiter"; the `resilience` CLAUDE.md "Do NOT"
  that requires an ADR before wait mode and before adaptive throttling
- **Extends**: [ADR 0026](0026-sdk-resilience-domain.md) (resilience),
  [ADR 0103](0103-a-bucket-per-caller-one-backoff-curve-and-a-retry-on-the-clock-it-is-given.md)
  (the backoff curve), [ADR 0112](0112-a-loop-that-must-keep-running-is-supervised-beside-the-lifecycle.md)
  (the supervisor), [ADR 0041](0041-sdk-scheduler-domain.md) (the scheduler)
- **Related**: [ADR 0031](0031-policy-zero-values-are-never-inert.md) (zero
  values), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md)
  (siblings), [ADR 0090](0090-a-port-named-in-public-must-be-implementable-in-public.md)
  (the injected clock), kitsunium/sdk#482

## Context

kitsunium/sdk#482 compared the SDK's second-rank reliability bounds with the
state of the art (resilience4j, Polly v8, failsafe-go, gRPC, the AWS Builders'
Library, Google's SRE book, Erlang/OTP, Kubernetes' CronJob). Six gaps:

1. The supervisor (ADR 0112) restarts forever. Erlang bounds restarts with an
   intensity — more than `MaxR` within `MaxT` and the supervisor stops — so the
   level above decides. A loop that cannot succeed should be visible as such.
2. The bulkhead and the rate limiter only reject. Polly's
   `ConcurrencyLimiter(permitLimit, queueLimit)` and failsafe-go's
   `WithMaxWaitTime` let a call wait a bounded time. ADR 0026 deferred the
   wait mode and the package's CLAUDE.md forbids adding it without an ADR.
3. The backoff has one jitter, added to the curve. The Builders' Library
   ("Timeouts, retries and backoff with jitter") measures two others — full
   and decorrelated — that spread synchronised callers further.
4. A HalfOpen breaker closes on one successful trial. resilience4j's
   `permittedNumberOfCallsInHalfOpenState` asks for several.
5. The scheduler runs a late fire however late, and its overlap policy is
   skip or allow. Kubernetes' CronJob has `startingDeadlineSeconds` and the
   `Replace` concurrency policy.
6. Nothing throttles on the client side. The SRE book's adaptive throttling
   refuses requests locally once the backend has been refusing more than its
   share: `requests ≤ K × accepts`, with `K = 2`.

The rules of the SDK frame every answer: the design first (ADR 0163), a field
whose zero is the behaviour before it or a refusal (ADR 0031), a sibling
rather than a widened port (ADR 0039), every wait on an injected clock
(ADR 0090), the standard library only (ADR 0156).

## Decision

### D1 — The supervisor gives up after an Erlang intensity

`SupervisorConfig.GiveUpAfter` is a `GiveUpValue{Restarts, Within}`. Its zero
restarts forever, as before. The `(Restarts+1)`-th early end within any span
of `Within` on the supervisor's clock is not restarted: the supervision ends,
its observer hears a fifth phase, `SupervisionGaveUp` (then `Stopped`), and
`Supervisor.Check(ctx) error` returns the new sentinel `SUPERVISOR_GAVE_UP`
(`0.3.49.9`, the supervisor's range) with the last run's error as a field.
`Restarts` 0 with a positive `Within` is Erlang's intensity 0: it gives up at
the first early end. A negative field, and a positive `Restarts` without a
positive `Within`, are refused by `NewSupervisor` (`SUPERVISOR_MISCONFIGURED`
naming the field): a bound with no span is no bound.

`Check` has `health.Check`'s shape, so a liveness probe takes `sup.Check`
as it is — the hook to `health` the issue asks for, without `lifecycle`
importing `health`. A later `Start` clears it. The early ends kept are bounded
by the bound itself: those older than `Within` are dropped before a new one is
added.

### D2 — The bulkhead and the rate limiter wait a bounded time

`NewBulkheadWait(BulkheadWaitConfig{Max, Queue, MaxWait, Clock})` is a new
constructor beside `NewBulkhead`: at most `Max` operations run, at most `Queue`
more wait, each at most `MaxWait` on `Clock`. A full queue or a wait run out
is `BULKHEAD_FULL`; a caller whose context ends while waiting gets `ctx.Err()`
(the caller left, #477) and frees its place. A zero `Queue` or `MaxWait` is no
waiting, so the zero configuration is `NewBulkhead(1)`: no queue nobody sized.
Both bounds are mandatory for the wait to exist because an unbounded queue
pins memory and goroutines behind a saturated dependency, and an unbounded
wait only moves the timeout to the caller. A call that finds a slot free pays
what `NewBulkhead`'s does — a buffered channel, no allocation.

`RateLimiterConfig.MaxWait` makes the limiter reserve a token due within it,
the way `golang.org/x/time/rate`'s `Reserve` does: the bucket goes into debt,
the calls after it queue behind the debt, and the call waits on the limiter's
clock. A token due later is rejected at once with its `retry_after_seconds`.
A cancelled wait hands its token back. Waiting needs a clock that can wait: a
positive `MaxWait` with a `Clock` that is a `clock.Clock` but not a
`clock.Timed` is refused naming `Clock`, rather than waiting on a second clock
the bucket does not read. The keyed limiter keeps rejecting: its buckets are
forgotten when idle, and a debt in a forgotten bucket would be a free token.

### D3 — Three jitters, on the one curve

The kernel curve (`kernel/backoff.Value`, ADR 0103) gains `JitterMode`:
`JitterAdditive` — the zero, `Jitter` added as before —, `JitterFull` — a
uniform draw from `[0, the capped curve]` — and `JitterDecorrelated` — a draw
from `[BaseDelay, 3 × the previous wait]`, held at `MaxDelay`. The two new
modes randomise the whole wait and ignore `Jitter`. The kernel publishes the
two halves as functions, `Full` and `Decorrelate`, as it published `Grow` and
`Widen`, so the curve stays one.

Decorrelated jitter depends on the previous wait, which `Value.Delay(n)` — a
function of the attempt — does not have. `Delay` reads the curve's own wait
for the attempt before as the previous one; the retry, which knows what it
waited, keeps that wait per call and calls `Decorrelate` with it. A wait the
failure imposed through `RetryConfig.Delay` is not randomised and is not the
previous wait a draw grows from: it is the other side's, not the curve's.
`RetryConfig.JitterMode` mirrors the field, as `RetryConfig` mirrors the four
others, and `BreakerConfig.OpenBackoff` and `SupervisorConfig.Backoff`, being
`Value`s, have it for free.

### D4 — A breaker may ask for several trials

`BreakerConfig.HalfOpenTrials` is how many trials in a row must succeed before
a HalfOpen breaker closes; zero or less is one, as before. The trials still
run one at a time — the slot discipline of #470 is unchanged — and any failed
or slow one opens the breaker again. The count lives within one HalfOpen
phase and starts over on every change of phase.

### D5 — A late fire may expire, and a fire may replace the run still going

`EntryValue.StartingDeadline`: when the scheduler comes to a due instant more
than that after it — a machine that slept, a timer that overran — the fire is
not run but reported with `ResultValue.Expired`. Zero is no deadline, the
behaviour before: the latest missed instant runs however late. It bounds the
catch-up ADR 0041 already limits to one run.

`EntryValue.Overlap` is an `OverlapPolicy`: `OverlapSkip` (the zero),
`OverlapAllow` (what `AllowOverlap` says, kept because it was there first) and
`OverlapReplace`. Replace cancels the running run's context and starts the new
fire **once that run has returned** — never beside it, which is what makes it
safe for a job whose copies must not overlap. A fire due while its predecessor
is still returning replaces the waiting one in turn, so at most one run waits:
the newest. The replaced run is reported with `ResultValue.Replaced`; one
replaced before it started has no `Started`. ADR 0041's refusal of a queueing
overlap mode stands: Replace holds one successor, not a queue. `AllowOverlap`
beside `OverlapReplace`, and an `Overlap` beyond the three, are `INVALID_ENTRY`
naming the field.

### D6 — The client throttles itself, adaptively, on what the dependency refuses

`NewAdaptiveThrottle(AdaptiveThrottleConfig{K, Window, Buckets, Rejected,
Clock, Random})` is the SRE book's client-side throttling ("Handling
Overload"). Over a sliding window on `Clock` it counts requests — every call
`Run` is asked to make, the ones it refuses included — and accepts, and
refuses each new call with probability

    max(0, (requests − K × accepts) / (requests + 1))

returning the new sentinel `THROTTLED` (`0.2.8.9`, HTTP 503, `EX_TEMPFAIL`)
without running the operation. Decisions the implementation makes:

- **Defaults are the book's.** `K` 2, a two-minute window; ten slots, as the
  breaker's window, so it reaches back between `Window − Window/10` and
  `Window`. `K` below 1 would refuse calls to a dependency accepting all of
  them and is refused, as is a non-finite one, a negative window, more than
  1000 slots, and a window too short to split.
- **What an accept is.** A nil error, an error `Rejected` does not call a
  refusal, and a `context.Canceled` once the caller's context is closed (#477:
  a caller who left says nothing about the dependency). A nil `Rejected` calls
  every error a refusal, as a nil `Retryable` counts every error against the
  breaker.
- **The probability is read before the request is counted**, so the first
  call of an empty window is never refused, and a healthy dependency never
  even draws: `Random` is called only when the probability is positive. It is
  injectable — nil is `math/rand/v2`'s `Float64` — so a test decides each
  refusal, and it is called under the throttle's lock, so a caller's source
  need not be safe for concurrent use.
- **A Runner, not a port.** The throttle is a policy that runs an Operation,
  like the six others; it composes by nesting. Its state is not published yet
  (below).

A dependency that refuses everything still receives a trickle — the traffic
that tells it, and us, that it has recovered — and the refusals fade as its
accepts come back into the window: unlike a breaker, the throttle has no
phases to cross.

## Consequences

- No published port changes. Every addition is a field whose zero is the
  behaviour before it, a new constructor, a new type or a new constant.
- Two codes, each in its domain's existing range: `0.2.8.9` `THROTTLED`
  (`core/app/resilience`), `0.3.49.9` `SUPERVISOR_GAVE_UP`
  (`core/app/lifecycle`). `RATE_LIMITED` and `BULKHEAD_FULL` now also say
  "within MaxWait".
- `ResultValue` gains `Expired` and `Replaced`, and `SupervisionPhase` a fifth
  value: published shapes changed while v0, by addition (ADR 0040).
- Every wait is on an injected clock, and every suite runs on a `ManualClock`
  with no sleep: the bulkhead's and the limiter's waits, the supervisor's
  intensity, the scheduler's deadline and Replace, the throttle's window.
- The paths that do not wait stay allocation-free: a bulkhead slot free, a
  token available, a throttle with nothing to refuse
  (`pkg/v1/app/resilience/BENCH.md`).

## Alternatives considered

- **`NewBulkhead` with a second argument, or a config replacing its int.**
  Rejected: it breaks every caller. A new constructor whose zero config is
  the old one costs nothing.
- **An unbounded queue in the bulkhead, or a wait without a ceiling.**
  Rejected (ADR 0031): both are what a forgotten field would buy, and both pin
  resources behind the very dependency that is saturated.
- **Waiting on the wall clock when the limiter's clock cannot wait.** Rejected:
  the bucket would refill on one clock and wait on another, and a test's
  `ManualClock` would no longer drive the wait.
- **Decorrelated jitter computed from the attempt alone.** Rejected for the
  retry, which knows its previous wait; kept, documented, for the memoryless
  `Value.Delay`, which has nothing else.
- **Replace that starts the new run beside the cancelled one** (Kubernetes
  deletes the Job and creates the next; its pods may overlap while one
  terminates). Rejected: a caller choosing Replace over Allow says the copies
  must not overlap.
- **The throttle as a mode of the breaker or of the retry budget.** Rejected:
  a breaker decides by phases, a budget throttles extra attempts only; the
  throttle throttles first attempts too, probabilistically, which neither may
  do (`retry_budget.go`'s "Do NOT").
- **Adaptive concurrency** (Netflix concurrency-limits' Vegas, Gradient2,
  AIMD; failsafe-go's `AdaptiveLimiter`). **Deferred**, below.

## Deferred

- **Adaptive concurrency.** A limit that moves with measured latency changes
  what a policy may do on the caller's behalf more deeply than a throttle: it
  needs a latency signal, a smoothing, and measurement on real traffic rather
  than a plausible implementation. The resilience CLAUDE.md "Do NOT" stands for
  it; this ADR lifts it for the SRE throttle only.
- **Load priorities** (Envoy's per-priority circuit breakers, the SRE book's
  criticality). They need a priority carried by the call, which the
  `Operation` port does not have.
- **The throttle's state** — its probability and counts for a metric or a
  readiness body — through a sibling port, as `BreakerState` is the breaker's.
  Its first consumer should shape it.
- **A keyed bulkhead or a keyed limiter that waits.** See D2.
- **The scheduler's per-host jitter** (spreading one cron instant across
  replicas). It needs a host identity the scheduler does not have.
- **The framework and the Studio**: the default wiring and configuration of
  these bounds, and their visualisation (kitsunium/sdk#482, framework and
  Studio stages).

## References

- [ADR 0026](0026-sdk-resilience-domain.md), [ADR 0103](0103-a-bucket-per-caller-one-backoff-curve-and-a-retry-on-the-clock-it-is-given.md),
  [ADR 0112](0112-a-loop-that-must-keep-running-is-supervised-beside-the-lifecycle.md),
  [ADR 0041](0041-sdk-scheduler-domain.md), [ADR 0031](0031-policy-zero-values-are-never-inert.md),
  [ADR 0039](0039-extending-a-published-port-without-breaking-it.md),
  [ADR 0040](0040-changing-a-published-shape-while-v0.md),
  [ADR 0090](0090-a-port-named-in-public-must-be-implementable-in-public.md).
- Erlang/OTP, `supervisor` — restart intensity (`intensity`, `period`).
- Polly v8 `ConcurrencyLimiter`; failsafe-go `bulkhead.WithMaxWaitTime`;
  `golang.org/x/time/rate` `Limiter.Reserve`.
- AWS Builders' Library, "Timeouts, retries and backoff with jitter".
- resilience4j `permittedNumberOfCallsInHalfOpenState`.
- Kubernetes CronJob `startingDeadlineSeconds`, `concurrencyPolicy: Replace`.
- Google, *Site Reliability Engineering*, ch. 21 "Handling Overload",
  client-side throttling.
- Impl: `internal/kernel/backoff` (`jitter.go`), `internal/core/app/resilience`
  (`THROTTLED`), `internal/service/app/resilience` (`bulkhead_wait.go`,
  `ratelimit.go`, `breaker_run.go`, `retry.go`, `throttle.go`,
  `throttle_window.go`), `internal/core/app/lifecycle` (`SUPERVISOR_GAVE_UP`),
  `internal/service/app/lifecycle` (`supervise.go`, `supervise_config.go`),
  `internal/core/app/scheduler` (`scheduler_overlap.go`),
  `internal/service/app/scheduler` (`entry.go`, `run.go`), and their facades
  under `pkg/v1/app`.
