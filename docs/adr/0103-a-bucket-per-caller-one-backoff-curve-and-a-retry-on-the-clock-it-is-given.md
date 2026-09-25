# ADR 0103 — a bucket per caller, one backoff curve, and a retry that waits on the clock it is given

- **Status**: Accepted
- **Date**: 2026-09-25
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0026](0026-sdk-resilience-domain.md) (two policies' worth of surface: a keyed limiter and a public backoff)
- **Related**: [ADR 0031](0031-policy-zero-values-are-never-inert.md) (zero values), [ADR 0090](0090-a-port-named-in-public-must-be-implementable-in-public.md) (the public clock)

## Context

Three gaps in `resilience`, each found by a framework that closed it itself:

- **One bucket for every caller.** `NewRateLimiter` is right for a dependency
  and wrong for an endpoint: on a sign-in route one client guessing passwords
  empties the bucket and every other user is refused. The framework kept one
  `NewRateLimiter` per client key in its own LRU — ten thousand keys, ten
  minutes idle, sliding — and forgot an idle key only when some OTHER new key
  arrived and triggered its eviction scan.
- **The backoff is private.** `NewRetry` computes `BaseDelay × Multiplier^(n−1)`
  held at `MaxDelay`, and nothing else can ask for it; the same framework
  computed "one second, doubling, up to a minute" three times, in a supervised
  loop, an outbox and a state machine.
- **The retry sleeps on a real timer.** The breaker and the rate limiter take a
  `Clock`; the retry — the one policy here that actually WAITS — did not, so a
  test of a retrying component slept through every backoff or used delays too
  small to mean anything.

Reading the private backoff found a defect. The growth was a `float64`
converted to a `time.Duration` unchecked. Past 2^63 nanoseconds Go leaves that
conversion implementation-defined, and it differs by architecture — measured
with the delay one-second-doubled 79 times: `math.MinInt64` on amd64,
`math.MaxInt64` on arm64. A negative wait fires at once, and the ceiling did
not catch it because a negative duration is below every ceiling. So on amd64 a
one-second retry stopped backing off at attempt 35 — the first whose delay
passes 2^63 ns — exactly where the wait was meant to be longest. A NaN
`Multiplier` slipped past the `<= 1` guard the same way and poisoned every
product.

## Decision

### D1 — `NewKeyedRateLimiter`: a token bucket per key, bounded

`KeyedRateLimiterConfig{Key, Rate, Burst, MaxKeys, IdleTimeout, Clock}` returns
a `Runner` that charges each call to the bucket of `Key(ctx)`. Every key gets
`Rate` and `Burst` of its own, from the same `tokenBucket` `NewRateLimiter`
uses. The keys come from outside, so their number is bounded: `MaxKeys` held at
once, the least recently used forgotten first. A key unused for `IdleTimeout`
is forgotten too — on its OWN next call, deterministically, not whenever some
unrelated key triggers a scan. A forgotten key starts over with a full bucket.

`Rate`, `Key`, `MaxKeys` and `IdleTimeout` have no defaults and are refused at
zero with `PolicyMisconfigured`, naming the field (ADR 0031): a zero `Rate` for
the reason `RateLimiterConfig.Rate` gives; a nil `Key` because a limiter with
nothing to key on is `NewRateLimiter`; a zero `MaxKeys` because it reads as
"unbounded", which lets a stream of distinct keys grow the process, or as
"none"; a zero `IdleTimeout` because it reads as "forget at once", which hands
every call a full bucket and limits nothing, or as "never". `Burst` clamps to 1
as it always has.

The recency list is intrusive, as the kernel cache's is — each bucket carries
its links, so a hit is two pointer swaps under one mutex and nothing is boxed.
The bucket is charged after the lock is released, so two keys never contend
beyond the lookup. There is no sweeper and no timer: an idle limiter costs
nothing.

### D2 — `BackoffValue`: the retry's curve, published

`BackoffValue{BaseDelay, MaxDelay, Multiplier, Jitter}.Delay(attempt)` —
`resilience.Backoff` in the facade — is the curve `NewRetry` waits, under the
same four names `RetryConfig` already uses, and the retry now builds its waits
from it, so the two cannot drift. `attempt` counts from 1 and a smaller value
is read as 1. Every field is normalised on each call: a `Multiplier` of 1 or
below, or NaN, doubles; a `Jitter` outside `[0, 1]`, or NaN, is clamped.

The growth is compared against its bound in the float domain BEFORE any
conversion, and stops multiplying as soon as the bound is reached: at
`MaxDelay` when one is set, at the longest `time.Duration` when not. It never
returns a negative duration, and `Delay(math.MaxInt)` costs the few dozen
multiplications it takes to reach the ceiling. The jitter is still ADDED after
the ceiling, as `RetryConfig.Jitter` documents.

### D3 — `RetryConfig.Clock`

A `clock.Timed`; nil is `clock.System`. The retry arms its backoff with
`NewTimer` on it and stops the timer when the context wins, so a
`pkg/v1/clock.ManualClock` drives a retry attempt by attempt and a test asserts
the exact wait. Adding a field to a published construction struct is free
(ADR 0040); every existing caller's timing is unchanged.

## Consequences

- The framework's `ratelimit.go` becomes one call with its `clientKey` as
  `Key`, and its three backoffs become `Backoff{BaseDelay: time.Second,
  MaxDelay: time.Minute}.Delay(n)` and its five-minute variant.
- A retry configured with a budget long enough to overflow now backs off at its
  ceiling on every architecture, where on amd64 it stopped waiting. That is a
  behaviour change, and it is the fix.
- What the keyed limiter does not protect against is stated in its config: a
  flood of NEW keys pushes active ones out, and a key pushed out comes back
  with a full bucket, so `MaxKeys` must exceed the clients active within
  `IdleTimeout`. Forgetting an idle key is observable only when `IdleTimeout`
  is shorter than a bucket's refill (`Burst / Rate`).

## Breaking changes

None. Two constructors' worth of new symbols and one new field; no signature
changes. The retry's delays are unchanged below 2^63 ns.

## Alternatives considered

- **The keyed limiter over the kernel `cache`.** Its TTL is set at write and
  expired lazily on read, so a sliding window means a `Fetch` then a `Set`
  under two lock acquisitions, and two goroutines missing the same key would
  each build a bucket and one would win — a double burst. A dedicated list
  under one lock is fifty lines and exact.
- **Keep the framework's eviction (idle keys dropped only when a new key
  arrives).** Whether a key's bucket was reset then depended on unrelated
  traffic; deciding on the key's own call makes it a property of that key.
- **A `Backoff` interface with pluggable curves.** Three downstream copies
  computed the same exponential curve; an interface for one implementation is
  over-abstraction, and a caller with another curve writes a function.
- **Clamp the float before converting, and keep the loop.** It would fix the
  sign and keep a loop that multiplies once per attempt forever; stopping at
  the bound fixes both.

## Deferred

- **Wait mode for the rate limiters** — still ADR 0026's deferred item; both
  limiters reject.
- **A per-key `Burst` or `Rate`.** One policy per limiter; a caller with tiers
  composes two.

## Verification

- `internal/service/resilience/backoff_external_test.go` — the loop's curve,
  the normalisations, `TestBackoffNeverWrapsNegative` at attempts 64, 2^20 and
  `math.MaxInt` with and without a ceiling and with an infinite multiplier, and
  the jitter band; `backoff_internal_test.go` pins that the retry inherits the
  fix. The same assertion run against the previous `retryRunner.backoff` under
  `GOARCH=amd64` returned `-2562047h47m16.854775808s` at attempt 35 with a
  one-hour ceiling, and one hour under arm64.
- `internal/service/resilience/retry_clock_external_test.go` — each attempt
  arrives exactly when a `ManualClock` passes its backoff, not a nanosecond
  before.
- `internal/service/resilience/keyed_ratelimit_external_test.go` — the four
  refusals and the clamp, one key refused while another is admitted, a refill
  on the injected clock, the sliding idle window;
  `keyed_ratelimit_internal_test.go` — the bound under fifty distinct keys, the
  recency order with its links checked both ways, the idle sweep on insertion.
- `pkg/v1/resilience/keyed_external_test.go` — both through the facade, and
  `TestBackoffAndRetryShareOneCurve`.

## References

- [ADR 0026](0026-sdk-resilience-domain.md) — the policies and what was deferred.
- [ADR 0040](0040-changing-a-published-shape-while-v0.md) — adding a field to a construction struct.
- [The Go Programming Language Specification, Conversions](https://go.dev/ref/spec#Conversions) — "if the result type cannot represent the value the conversion succeeds but the result value is implementation-dependent".
