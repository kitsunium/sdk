<!-- updated: 2026-09-11T00:00:00Z -->
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

// The two implementations.
var System Timed                            // delegates to package time
func NewManualClock(start time.Time) *ManualClock
```

`*ManualClock` adds the driving surface on top of `Timed`:

| Method | Contract |
|---|---|
| `Advance(d)` | move forward by `d`, firing every wait that comes due, in deadline order. **Panics on a negative `d`** — use `Set`. |
| `Set(t)` | move to `t`. Forward = `Advance`. Backward = move the reading, fire nothing (deadlines are absolute and re-fire on the way back). |
| `Pending() int` | number of armed waits. |
| `BlockUntil(n)` | block until at least `n` waits are armed. |

### File layout

One type per file, per `KTN-STRUCT-ONEFILE`; at most two interfaces per file,
per `KTN-INTERFACE-FILENAME`.

| File | Holds |
|---|---|
| `clock.go` | package doc + `Clock` + `Timed` |
| `waiter.go` | `Waiter` + `requirePositivePeriod` (the shared refusal) |
| `timer.go` / `ticker.go` | the `Timer` / `Ticker` contracts |
| `system.go` | `systemClock` + the `System` singleton |
| `system_timer.go` / `system_ticker.go` | the `*time.Timer` / `*time.Ticker` adapters |
| `manual.go` | `ManualClock` and every method on it |
| `manual_wait.go` | `manualWait` + `fireWait` / `drainWait` / `rearmWait`, and the `maxDuration` bound the rearm stays under |
| `manual_timer.go` / `manual_ticker.go` | the handles `ManualClock` hands out |
| `clock_compliance.go` | every compile-time interface assertion |

## Why `Clock` was NOT extended

`Clock` is a **published port**, not an internal detail. `pkg/v1/cache.Config`
is a *type alias* for `internal/kernel/cache.Config[K,V]`, whose `Clock` field
carries this exact interface — so any consumer of the released `pkg` module can
already write a two-method double and pass it in. Go interfaces are structural:
they cannot *name* `clock.Clock`, but they satisfy it, and adding a method
breaks them at compile time with no deprecation window.

In-tree there are **seven** such doubles today (all in `_test.go` files):

| Package | Type |
|---|---|
| `internal/kernel/cache` | `fakeClock`, `fixedClock` |
| `internal/service/logger` | `frozenClock` |
| `internal/service/writer/dbsink` | `frozenClock` |
| `internal/service/writer/rotfile` | `fakeClock` |
| `internal/service/resilience` | `steppedClock` |
| `internal/service/id` | `steppedClock` |

Widening `Clock` would have broken all seven plus every downstream one. Adding
`Waiter` alongside breaks nothing: `Clock` is byte-identical to what it was,
and `System` moved from `Clock` to `Timed`, which is a *widening* of the value
— every `clk = clock.System` assignment into a `Clock` field still compiles.
`waiter_external_test.go`'s `TestTwoMethodDoubleStillSatisfiesClock` is the
regression guard: it declares a bare `Now`/`Since` type and assigns it to a
`clock.Clock`, so re-widening the interface fails that test first.

The seven in-tree doubles are **not** migrated to `ManualClock` in this change
— they still compile and still pass, and rewriting seven packages' tests to
prove a point belongs in its own commit. They are the obvious first consumers.

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

## clock vs `testing/synctest`

Go's `testing/synctest` (stable since 1.25) runs a function in a *bubble* where
package `time` is replaced by a fake clock. **It overlaps this package and it
is often the better tool.** Every claim below is either quoted from
`go doc testing/synctest` on the pinned toolchain or pinned by a test in
`synctest_external_test.go` — nothing here is inferred.

**What synctest already does for you.** Inside a bubble, `clock.System` is
*already* fake, because it delegates to package `time`. You do not need to
inject anything to get simulated time in a bubble — that is
`TestSystemObservesSynctestFakeClock`, which reads `clock.System.Now().Year()
== 2000`, sleeps an hour and a day, and costs no wall-clock time. Any advice to
"replace `clock.System` to get fake time under synctest" is wrong.

**What synctest does that `ManualClock` cannot.**

- It fakes time for code you cannot inject into: a third-party library's
  `time.After`, `net/http`'s internal timers, `context.WithTimeout`.
- `synctest.Wait()` blocks until every *other* goroutine in the bubble is
  durably blocked — real happens-before synchronisation. `BlockUntil(n)` only
  counts armed waits; it is strictly coarser. `synctest.Sleep(d)` is documented
  as "exactly equivalent to `time.Sleep(d); synctest.Wait()`" — i.e. *advance
  and settle*, in one call. `ManualClock` has no equivalent: `Advance` moves
  time and wakes waiters, but cannot wait for the woken goroutines to settle,
  because it has no way to know what "settled" means outside a bubble. That is
  the single largest thing synctest does better, and no amount of API on
  `ManualClock` closes it.
- It detects deadlock: "when every goroutine in a bubble is durably blocked …
  and there is no time that will unblock one, there is a deadlock and Test
  panics." A `ManualClock` in the same situation just blocks to the test
  binary's timeout.

**What `ManualClock` does that synctest cannot.**

- **Run in a normal parallel table test.** `synctest.Test` documents that
  `T.Run`, `T.Parallel` and `T.Deadline` "must not be called" on the bubbled
  `*testing.T`. The SDK's test convention is table-driven `t.Run` + `t.Parallel`
  everywhere; a bubble ends that for the whole test function.
- **Coexist with real I/O and syscalls.** Time in a bubble only advances when
  *every* goroutine is durably blocked, and the doc's own list excludes
  "blocking on I/O, such as reading from a network socket" and "system calls".
  Measured, not assumed: a `select` mixing `time.After(1s)` with a channel
  created outside the bubble never advanced and hit the 60-second binary
  timeout. Anything that writes files (`writer/rotfile`), spawns processes
  (`proc`) or talks to a driver (`writer/dbsink`) is in that class.
- **Start anywhere, and move backwards.** "The initial time is midnight UTC
  2000-01-01", per bubble, with no API to change it and no way to go back.
  `NewManualClock` takes any instant — pre-Unix, 2038, any location — and `Set`
  moves the clock backwards to model an NTP step or a VM restore. That matters
  for `id`'s snowflake clock-regression path and for any epoch-encoding test.
- **Be a value you can hand to a constructor.** SDK constructors take a
  `clock.Clock` / `clock.Timed`. synctest patches a package; it hands you
  nothing to inject, and a consumer of `pkg/v1` who needs a double still has to
  write one.
- **Step time explicitly.** `Advance(30*time.Second)` and then assert. In a
  bubble time jumps to the next deadline on its own whenever everything blocks;
  holding time still while you poke at the system is not a thing you control.

**Rule of thumb.** Concurrent code you own, all inside one function, no real
I/O → `synctest`. Everything else — a parallel table test, a test that touches
the filesystem, a non-2000 epoch, a backwards jump, or a `Clock` a constructor
demands → `ManualClock`. They compose: `TestManualClockIgnoresSynctestBubble`
runs a `ManualClock` *inside* a bubble and shows the two clocks are independent.

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

## Typical use

```go
// Production — reading only.
type handler struct{ clk clock.Clock }
func New(clk clock.Clock) *handler {
    if clk == nil { clk = clock.System }
    return &handler{clk: clk}
}

// Production — waiting.
type poller struct{ clk clock.Timed }
func (p *poller) run(stop <-chan struct{}) {
    tk := p.clk.NewTicker(time.Second)
    defer tk.Stop()
    for {
        select {
        case <-tk.C():  /* work */
        case <-stop:    return
        }
    }
}

// Test — deterministic, no sleeping, t.Parallel-safe.
m := clock.NewManualClock(time.Date(2031, 3, 7, 4, 5, 6, 789, time.UTC))
p := &poller{clk: m}
go p.run(stop)
m.BlockUntil(1)          // the ticker is armed
m.Advance(time.Second)   // exactly one tick, at exactly +1s
```

## Do NOT

- **Add a method to `Clock`.** See §"Why `Clock` was NOT extended". Waiting
  capabilities go on `Waiter`; anything else needs a third interface and a
  reason. *(This reverses the pre-2026-09 "Do NOT add `Sleep`, `After`,
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
- **Alias `ManualClock` into `pkg/v1` on a whim.** It would be genuinely useful
  to downstream test suites, but it is a new public package with the full
  README/gomarkdoc obligation (rules 8 and 10) — a deliberate decision, not a
  side effect of this one.

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

A longer-form companion lives in `README.md`.
