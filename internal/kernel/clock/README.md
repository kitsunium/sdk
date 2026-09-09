# clock

Time abstraction for the SDK kernel — **reading** time and **waiting** on it.
Stdlib-only, no domain vocabulary, no error codes.

Two capabilities behind two interfaces, on purpose:

```go
type Clock  interface { Now() time.Time; Since(time.Time) time.Duration }
type Waiter interface {
	After(time.Duration) <-chan time.Time
	NewTimer(time.Duration) Timer
	NewTicker(time.Duration) Ticker
	Sleep(time.Duration)
}
type Timed interface { Clock; Waiter }
```

Ask for the narrowest half you need. `Clock` is a published port — reachable
downstream through `pkg/v1/cache.Config.Clock` — so it stays at two methods and
new capabilities land on `Waiter`.

## Production

```go
import "github.com/kitsunium/sdk/internal/kernel/clock"

type poller struct{ clk clock.Timed }

func New(clk clock.Timed) *poller {
	if clk == nil {
		clk = clock.System // the wall clock
	}
	return &poller{clk: clk}
}

func (p *poller) run(stop <-chan struct{}) {
	tk := p.clk.NewTicker(time.Second)
	defer tk.Stop()
	for {
		select {
		case <-tk.C():
			// ... a unit of work ...
		case <-stop:
			return
		}
	}
}
```

## Test

`ManualClock` is a `Timed` whose instant moves only when you move it. No
sleeping, no wall-clock tolerance, and it works in an ordinary `t.Parallel()`
table test.

```go
m := clock.NewManualClock(time.Date(2031, 3, 7, 4, 5, 6, 789, time.UTC))
p := New(m)
go p.run(stop)

m.BlockUntil(1)        // wait until the ticker is armed — closes the wake race
m.Advance(time.Second) // exactly one tick, at exactly +1s
```

| Method | Contract |
|---|---|
| `Advance(d)` | forward by `d`, firing due waits in deadline order; panics on a negative `d` |
| `Set(t)` | to `t`; forward fires, backward only moves the reading (deadlines are absolute) |
| `Pending()` | number of armed waits |
| `BlockUntil(n)` | block until at least `n` waits are armed |

A ticker delivers at most one tick per `Advance` — the channel holds one anyway.
To observe N ticks, advance N times and drain between calls.

## Non-positive durations

`After` / `NewTimer` / `Sleep` treat `d <= 0` as already elapsed and deliver at
once. `NewTicker` / `Ticker.Reset` **panic** — a ticker has no meaningful zero
period, and any value the SDK substituted would be arbitrary (ADR 0031). The
message is raised by this package and is identical on both implementations:

```
clock: NewTicker requires a period > 0, got 0s
```

## Relationship to `testing/synctest`

Complementary, not redundant. Inside a synctest bubble `clock.System` is
*already* fake, because it delegates to package `time` — nothing needs
replacing. Reach for `synctest` for concurrent code you own with no real I/O;
reach for `ManualClock` for a parallel table test, a test that also touches the
filesystem or a socket, an epoch other than 2000-01-01, a clock that must move
backwards, or anywhere a constructor demands a `Clock` value.

The full comparison — with every claim traced to `go doc testing/synctest` or
to a test in `synctest_external_test.go` — is in `CLAUDE.md`.

Benchmarks: `BENCH.md`. Design notes and the "do not extend `Clock`" rationale:
`CLAUDE.md`.
