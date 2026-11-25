<!-- updated: 2026-10-05T00:00:00Z -->
# internal/kernel/clock/

## Purpose

Time abstraction for the whole SDK — **reading** time and **waiting** on it —
so higher layers can inject a deterministic source instead of coupling to the
wall clock. Stdlib-only, domain-neutral. Code range `0.1.2.*` reserved
(ADR 0005); **no sentinels are emitted today and none are planned** — like
`cache` (ADR 0025), the failure modes here are programmer errors and are
refused with a panic, not an error value.

## Surface

```go
// The two capabilities, deliberately separate.
type Clock interface {                      // READ time
    Now() time.Time
    Since(t time.Time) time.Duration
}
type Waiter interface {                     // WAIT on time
    After(d time.Duration) <-chan time.Time
    NewTimer(d time.Duration) Timer
    NewTicker(d time.Duration) Ticker
    Sleep(d time.Duration)
}
type Timed interface { Clock; Waiter }      // the union

// The handles Waiter returns. C() is a METHOD because *time.Timer exposes
// its channel as a FIELD, and no interface can require a field.
type Timer  interface { C() <-chan time.Time; Stop() bool; Reset(time.Duration) bool }
type Ticker interface { C() <-chan time.Time; Stop();      Reset(time.Duration) }

// The two implementations, and a coarse reader over either.
var System Timed                            // delegates to package time
func NewManualClock(start time.Time) *ManualClock
func NewCoarse(base Timed, resolution time.Duration) *CoarseClock // Now = one atomic load
```

`*CoarseClock` (#814) is a `Clock` whose `Now` is an atomic load: a goroutine
of its own reads its base every `resolution` and publishes the `time.Time`
through an `atomic.Pointer` (one 24-byte allocation per tick, none per read;
the monotonic reading travels with it). A reading is at most one
resolution behind the base; `Stop` ends the goroutine and joins it, and a
stopped clock reads its base directly, so a component that outlives it never
sees time stand still. A `ManualClock` base makes it deterministic
(`TestCoarseLagsByAtMostOneResolution`); `TestCoarseStopJoinsItsGoroutine`
runs in a synctest bubble, which fails on a goroutine left behind. It does not
implement `Waiter`: waiting on a coarse clock would make every timer late by
the resolution, and a caller that waits keeps its own `Timed`.

`*ManualClock` adds the driving surface on top of `Timed`:

| Method | Contract |
|---|---|
| `Advance(d)` | move forward by `d`, firing every wait that comes due, in deadline order. **Panics on a negative `d`** — use `Set`. |
| `Set(t)` | move to `t`. Forward = `Advance`. Backward = move the reading, fire nothing (deadlines are absolute and re-fire on the way back). |
| `Pending() int` | number of armed waits. |
| `BlockUntil(n)` | block until at least `n` waits are armed. |

## Non-positive durations (ADR 0031)

ADR 0031 says a zero value is a safe default or an explicit refusal, never an
inert policy. Both halves apply here, and they land differently:

| Call | `d <= 0` | Why |
|---|---|---|
| `After`, `NewTimer`, `Sleep`, `Timer.Reset` | **fires / returns at once** | "Already elapsed" is the only sensible reading of a delay with no future. It is total, it is the stdlib's own contract, and it needs no explanation — the clamp half of ADR 0031. |
| `NewTicker`, `Ticker.Reset` | **panics** | A ticker has no meaningful zero period, and any positive value the SDK invented would be arbitrary — the refusal half of ADR 0031. |

The panic is raised **by this package**, in `requirePositivePeriod`, *before*
`time.NewTicker` is reached — so `time.NewTicker`'s own panic never escapes and
`System` and `ManualClock` refuse identically:

```
clock: NewTicker requires a period > 0, got 0s
clock: Ticker.Reset requires a period > 0, got -1m0s
```

`TestNewTickerNonPositivePanics` and `TestTickerResetNonPositivePanics` assert
those strings **verbatim, against both implementations**, so the two cannot
drift. A panic rather than an error is deliberate and matches the kernel's
existing posture (`worker.Start` panics on a nil loop, `recycler` panics on
programmer error): a zero ticker period is a bug in the caller, not a runtime
condition, and returning an error would require a code range this package has
never needed.

`ManualClock.Advance` refuses a negative `d` the same way, pointing at `Set`.

## Conventions

- **Ask for the narrowest half.** A type that only stamps records takes
  `clock.Clock`. A type that waits takes `clock.Timed`. Never take `Timed` when
  `Clock` is enough — that is the whole reason the port is split.
- **Inject, don't mutate.** Constructor takes `clk clock.Clock` (or `Timed`);
  nil falls back to `clock.System`. NEVER mutate `clock.System` at package
  scope — it breaks parallel tests.
- **Implementations MUST be safe for concurrent use.** `systemClock` is a
  zero-sized value type; `ManualClock` guards every field with an `RWMutex`,
  and each wait's channel is created once at registration and never reassigned
  (which is why `Timer.C()` needs no lock).
- **`Advance` wakes, it does not schedule.** After `Advance` returns, the value
  is in the channel but the woken goroutine may not have run. Synchronise on
  something that goroutine signals; use `BlockUntil` for the mirror race, where
  the test would advance before the code under test had armed its wait.
- **A ticker delivers at most one tick per `Advance`.** The channel holds one
  anyway, and the alternative — one send per elapsed period — makes
  `Advance(time.Hour)` on a nanosecond ticker an unbounded loop. To observe N
  ticks, call `Advance(period)` N times and drain between calls.
- **The rearm is O(1) however far the clock jumps, and it cannot go
  backwards.** `time.Time.Sub` saturates at ~292 years, and past that distance
  `period × steps` wrapped negative: the deadline moved backwards, stayed due,
  and a `Set` three centuries ahead looped forever under the lock. A ticker
  left that far behind now re-arms one period after the target — strictly
  after it, within one period of it, and off its phase, since the phase is not
  computable in a `Duration` there. `TestManualSetFarPastATickerDeadlineReturns`
  bounds the call with a wall-clock wait, so a regression fails instead of
  hanging the binary.
- **`Stop` and `Reset` drain, on BOTH handles.** Since Go 1.23 a `time.Timer`
  or `time.Ticker` channel is synchronous, and nothing prepared before a
  `Stop`/`Reset` is received after it — for tickers too, which this package
  once claimed the opposite of. `TestSystemTickerLeavesNoStaleTickAcrossStopAndReset`
  pins that on the real ticker and
  `TestManualTickerStopAndResetLeaveNoStaleTick` holds the double to it.

## Do NOT

- **Add a method to `Clock`, `Waiter`, `Timed`, `Timer` or `Ticker`.** See
  §"Why `Clock` was NOT extended". `pkg/v1/clock` publishes all five, so all
  five are frozen (ADR 0090): a new capability is a sibling interface
  discovered by type assertion (ADR 0039), and it needs a reason. *(This reverses the pre-2026-09 "Do NOT add `Sleep`, `After`,
  `NewTimer` here" rule, which kept the package thin at the cost of making
  "injecting this clock makes time testable" false: with only `Now`/`Since`,
  no timeout, retry backoff or ticker cadence could be driven from a test. The
  thinness is preserved where it was load-bearing — on `Clock` itself.)*
- **Replace `System` at package scope** — it breaks parallel tests. Inject.
- **Clamp a non-positive ticker period.** Any value chosen would be arbitrary;
  refuse instead (ADR 0031).
- **Let `time.NewTicker`'s own panic escape.** Guard first, so both
  implementations refuse with the same message.
- **Introduce monotonic-vs-wall split methods** — the stdlib already handles
  that inside `time.Since`.
- **Emit an error code from this package.** It has none and needs none; a
  range allocation in `codeRangeOwners` (ADR 0035) is a separate decision.
- **Add an exported symbol here without its `pkg/v1/clock` alias.** The
  facade is this package in full — `Clock`, `Waiter`, `Timed`, `Timer`,
  `Ticker`, `System`, `ManualClock`, `NewManualClock` — as aliases and nothing
  else (ADR 0090), and a facade that published a subset would recreate, on
  whatever it left behind, the problem that ADR closed.

## Verification

```
bazel test --config=race //internal/kernel/clock:clock_test
# OR
cd internal/kernel && GOWORK=off go test -race -cover ./clock
# coverage target: 100%
```

Tests:

| File | Covers |
|---|---|
| `clock_external_test.go` | wall-clock monotonicity, drift-vs-`time.Now` |
| `clock_internal_test.go` | the unexported `systemClock` value |
| `waiter_external_test.go` | the waiting surface on **both** clocks: non-positive durations, the verbatim ticker-refusal messages, timer/ticker Stop+Reset, and the `Clock`-compatibility guard |
| `manual_external_test.go` | `ManualClock` determinism: arbitrary origins, deadline-ordered firing, tick drop, Stop/Reset drain semantics, backwards `Set`, `Sleep`/`BlockUntil`, and a race-detector concurrency stress |
| `synctest_external_test.go` | the two load-bearing claims of §"clock vs `testing/synctest`" |
| `clock_bench_test.go` | the numbers in `BENCH.md`, including the `Stdlib_NewTimer` control pair |
| `perf_fixtures_test.go` | race off: the fixtures of the design's budgets on `Clock.Now` and `Clock.Since` — a call through `System` allocates nothing (ADR 0165) |
| `gen_perf_test.go` | written by kit gen from those budgets, run by the alloc lane |

A longer-form companion lives in `README.md`.

## Reference in NOTES.md

Moved unchanged to `internal/kernel/clock/NOTES.md`, under the same titles, and read only by a change that needs its mechanics: `File layout`; `Why `Clock` was NOT extended`; `clock vs `testing/synctest``; `Typical use`.
