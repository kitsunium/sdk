<!-- updated: 2026-10-03T00:00:00Z -->
# internal/kernel/backoff/

## Purpose

The SDK's one **exponential backoff curve**: `Value{BaseDelay, MaxDelay,
Multiplier, Jitter}.Delay(attempt)` — the wait after the attempt-th consecutive
failure, `BaseDelay × Multiplier^(n−1)`, held at `MaxDelay`, widened by
`Jitter`. A kernel primitive: stdlib-only (`math`, `math/rand/v2`, `time`) and
domain-neutral — a duration, a factor, a ceiling and a jitter, with no `Retry`,
`Job` or `Delivery` in a signature.

It was published by ADR 0103 as `service/app/resilience.BackoffValue` and moved
here because every domain that backs off on its own terms — `lifecycle`'s
supervisor, `queue`'s retry delay, `statemachine`'s per-entity backoff,
`mail/spool`'s parked delivery, `net/server`'s failed `Accept` — imported the
whole `resilience` service package for this one stdlib-only type. The curve
names no policy, so it belongs to the layer below every domain; those five now
import it directly, and `resilience` keeps `BackoffValue` as an alias.

Emits **no error codes**: no input is refused — every field is normalised.

## Surface

```go
type Value struct {
    BaseDelay  time.Duration // wait after the first failure; <= 0 waits nothing
    MaxDelay   time.Duration // ceiling on the growth; 0 is none
    Multiplier float64       // growth factor; <= 1 and NaN mean 2
    Jitter     float64       // [0, Jitter) of the wait ADDED after the ceiling; clamped into [0, 1], NaN is 0
}

func (b Value) Delay(attempt int) time.Duration // Widen(Grow(...)); attempt < 1 reads as 1; never negative

func Grow(base, ceiling time.Duration, multiplier float64, attempt int) time.Duration // the deterministic half, pure
func Widen(delay time.Duration, jitter float64) time.Duration                          // the random half
func NormalMultiplier(multiplier float64) float64 // <= 1 and NaN -> 2
func NormalJitter(jitter float64) float64         // clamp into [0, 1], NaN -> 0
```

Public reach: `pkg/v1/app/resilience.Backoff` is an alias of `Value` (ADR 0074 —
the alias points at the layer that owns the type), and
`internal/service/app/resilience.BackoffValue` is another. **A method added to
`Value` is public API**; a package-level function is not, which is why the two
halves and the normalisers are functions.

## Why two halves

`Grow` is a PURE function of its arguments; `Widen` is the randomisation. The
deterministic curve and the jitter are two claims, and a suite that cannot
assert the growth without the jitter can assert neither precisely — which is
why the retry policy's `backoff(attempt)` and `jittered(delay)` stay separate
methods delegating to `Grow` and `Widen`, and why `Delay` is literally
`Widen(Grow(...))`.

## The defect this curve closed (ADR 0103)

The copy in the retry policy grew its delay as a `float64` and converted it to
a `time.Duration` unchecked. Past 2^63 ns Go leaves that conversion
implementation-defined — `math.MinInt64` on amd64, `math.MaxInt64` on arm64 —
and a negative wait fires at once and is below every ceiling, so a one-second
retry on amd64 stopped backing off at attempt 35. `grow` compares against the
bound in the float domain BEFORE converting, and stops multiplying once the
bound is reached, so `Delay(math.MaxInt)` costs a few dozen multiplications.
`widen` keeps `delay + width` representable by spreading across the headroom
below `math.MaxInt64` rather than past it, and never calls `rand.Int64N` with a
non-positive bound. `NormalMultiplier` and `NormalJitter` resolve NaN FIRST,
because `<= 1` lets NaN through and Go's `min`/`max` propagate it.

## Conventions

- **Total.** Every exported function accepts any input; the unexported `grow`
  and `widen` trust their callers' normalisation, and only the exported entry
  points call them.
- **No clock.** A curve computes a duration; waiting it is the caller's job, on
  the caller's clock (`clock.Timed`), so a test drives the wait with a
  `ManualClock` and asserts the curve exactly.
- Cross-OS: 100 % portable.

## Do NOT

- Add a method to `Value` without deciding it as public API — it surfaces in
  `pkg/v1/app/resilience.Backoff` through the alias.
- Convert the grown float to a `time.Duration` before comparing it with the
  bound. That conversion is the defect above.
- Make jitter apply by default. Its zero being "deterministic" is what let it
  land on a shipped retry policy without re-timing every caller.

## Verification

```
cd internal/kernel && GOWORK=off go test -race -count=1 ./backoff/
bazel test --config=race //internal/kernel/backoff:backoff_test
```

`backoff_external_test.go` pins the curve (`TestValueDelay`), the negative-wrap
fix (`TestValueDelayNeverWrapsNegative`), the jitter band, and each exported
half and normaliser on inputs `Delay` never hands them; `backoff_internal_test.go`
pins that `grow` stops at its bound and that `widen` stays representable.
