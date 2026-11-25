# internal/kernel/clock/ — reference

Moved unchanged from `internal/kernel/clock/CLAUDE.md` (ADR 0253); each section keeps its title.

## Surface

### File layout

Types that belong to one another share a file, per `KTN-STRUCT-PARTITION`;
at most two interfaces per file, per `KTN-INTERFACE-FILENAME`. `system.go`
holds `systemClock` with the `systemTimer` / `systemTicker` adapters it is the
only producer of — splitting them left three files nothing else referenced.

| File | Holds |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `gen_decls.go` | written by kit gen from the design (ADR 0168): the `Clock`, `Timed`, `Waiter`, `Timer` and `Ticker` contracts, the `ManualClock` struct with every field, `NewManualClock`, `ManualClock.NewTimer` and `ManualClock.Sleep` as one call of their unexported bodies, and `ManualClock`'s assertions against `Clock`, `Waiter` and `Timed` |
| `waiter.go` | `requirePositivePeriod` (the shared refusal) |
| `system.go` | `systemClock` + the `System` singleton, and the `*time.Timer` / `*time.Ticker` adapters |
| `coarse.go` | `CoarseClock` + `NewCoarse` / `Now` / `Since` / `Stop` — the publishing goroutine and its atomic reading (#814) |
| `manual.go` | the bodies `newManualClock`, `newTimer` and `sleep`, and every other `ManualClock` method whole — `Now`, `Since`, `After`, `NewTicker`, `Advance`, `Set`, `Pending` and `BlockUntil` keep their bodies because theirs do not inline (a lock and its `defer`): `ManualClock` is reached through `Clock` and `Timed`, where a wrapper over a body that does not inline would be a call more (ADR 0168) |
| `manual_wait.go` | `manualWait` + `fireWait` / `drainWait` / `rearmWait`, and the `maxDuration` bound the rearm stays under |
| `manual_timer.go` / `manual_ticker.go` | the handles `ManualClock` hands out |
| `clock_compliance.go` | every compile-time interface assertion of an unexported type (`ManualClock`'s are the design's, in `gen_decls.go`) |

## Why `Clock` was NOT extended

`Clock` is a **published port**, not an internal detail. `pkg/v1/clock`
publishes it as an alias (ADR 0090), and `pkg/v1/data/cache.Config` is a *type
alias* for `internal/kernel/collections/cache.Config[K,V]`, whose `Clock` field carries this
exact interface — so any consumer of the released SDK module can write a
two-method double and pass it in. Go interfaces are structural: a double
satisfies `clock.Clock` whether or not it names it, and adding a method breaks
it at compile time with no deprecation window.

In-tree there are **ten** hand-written implementations today, all in
`_test.go` files — nine two-method doubles, and `pkg/v1/clock`'s `handClock`,
a complete `Timed`:

| Package | Type |
|---|---|
| `internal/kernel/collections/cache` | `fakeClock`, `fixedClock` |
| `internal/service/observe/logger` | `frozenClock` |
| `internal/service/observe/logger/writer/dbsink` | `frozenClock` |
| `internal/service/observe/logger/writer/rotfile` | `fakeClock` |
| `internal/service/app/resilience` | `steppedClock` |
| `internal/service/app/id` | `steppedClock` |
| `internal/service/data/queue` | `steppingClock` |
| `pkg/v1/clock` | `readOnlyClock`, `handClock` |

Widening `Clock` would break all ten plus every downstream one. Adding
`Waiter` alongside breaks nothing: `Clock` is byte-identical to what it was,
and `System` moved from `Clock` to `Timed`, which is a *widening* of the value
— every `clk = clock.System` assignment into a `Clock` field still compiles.
`waiter_external_test.go`'s `TestTwoMethodDoubleStillSatisfiesClock` is the
regression guard: it declares a bare `Now`/`Since` type and assigns it to a
`clock.Clock`, so re-widening the interface fails that test first.

None of the ten is migrated to `ManualClock` — they compile and pass as they
are. The two in `pkg/v1/clock` exist to prove a consumer can write a double
with no access to `internal/`, and `queue`'s `steppingClock` moves on at every
reading, which `ManualClock` never does; the other seven are the obvious first
consumers.

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
  nothing to inject. A consumer of `pkg/v1` injects `pkg/v1/clock`'s
  `ManualClock`, the alias of this one (ADR 0090).
- **Step time explicitly.** `Advance(30*time.Second)` and then assert. In a
  bubble time jumps to the next deadline on its own whenever everything blocks;
  holding time still while you poke at the system is not a thing you control.

**Rule of thumb.** Concurrent code you own, all inside one function, no real
I/O → `synctest`. Everything else — a parallel table test, a test that touches
the filesystem, a non-2000 epoch, a backwards jump, or a `Clock` a constructor
demands → `ManualClock`. They compose: `TestManualClockIgnoresSynctestBubble`
runs a `ManualClock` *inside* a bubble and shows the two clocks are independent.

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
