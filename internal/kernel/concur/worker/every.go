package worker

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// EveryOption tunes [Every]. Options apply in order, so a later one wins; a nil
// option is skipped.
//
// An option takes the option set by value and returns the amended copy rather
// than writing through a pointer: handing an unknown function a pointer to the
// set would move the set to the heap on every Every call, and Every is built
// once per stream or connection by the network domains.
type EveryOption func(everyConfig) everyConfig

// everyConfig is the resolved option set. It is unexported because each zero
// field has one meaning — the wall clock, no early end — decided in
// resolveEvery rather than by whoever fills the struct.
type everyConfig struct {
	// waiter builds the ticker. Nil means clock.System.
	waiter clock.Waiter
	// done, when non-nil, ends the loop as soon as it is closed.
	done <-chan struct{}
}

// WithClock makes [Every] tick on w instead of the wall clock. Its use is a
// test's: a clock.ManualClock advanced past the interval delivers a tick
// exactly then, so a cadence is asserted by moving time rather than by
// sleeping and hoping. A nil w is the wall clock.
func WithClock(w clock.Waiter) EveryOption {
	//: applied in order by Every, so a later option deliberately wins.
	return func(c everyConfig) everyConfig {
		c.waiter = w
		//: the amended copy.
		return c
	}
}

// WithDone ends the loop as soon as done is closed, without waiting for Stop.
//
// It is for an owner whose work can end on its own — a stream the peer closed,
// a connection a tick found dead — so the ticking stops at that end rather
// than at the owner's later Stop. Stop is still the join: it returns at once
// on a loop that has already left. A nil done never fires.
func WithDone(done <-chan struct{}) EveryOption {
	//: applied in order by Every, so a later option deliberately wins.
	return func(c everyConfig) everyConfig {
		c.done = done
		//: the amended copy.
		return c
	}
}

// Every starts a LoopDaemon whose loop fires tick on each interval until Stop
// is called — or until the channel given by [WithDone] is closed. The daemon
// owns the ticker and stops it when the loop exits, so Stop both ends the
// ticking and joins the goroutine.
//
// The ticker is built on the wall clock unless [WithClock] names another. A
// tick that comes due while the previous one is still running is dropped, not
// queued: the clock.Ticker contract, which is time.Ticker's.
//
// A nil tick and a non-positive interval panic HERE, at the caller: a ticker
// that does nothing is a bug, and clock.Waiter.NewTicker panics on a
// non-positive period, which would otherwise happen inside the spawned
// goroutine and take the process with it.
func Every(interval time.Duration, tick func(), opts ...EveryOption) *LoopDaemon {
	//: a nil tick is a programmer error — a ticker that does nothing is a bug.
	if tick == nil {
		//: fail fast at the call site.
		panic("worker: nil tick")
	}
	//: a non-positive interval would panic inside the spawned goroutine; refuse
	//: it here so the panic lands at the caller, not in the daemon.
	if interval <= 0 {
		//: mirror the ticker's own contract at the construction site.
		panic("worker: non-positive interval")
	}
	cfg := resolveEvery(opts)
	//: spawn the ticker loop behind the generic LoopDaemon lifecycle.
	return Start(func(stop <-chan struct{}) {
		//: the loop owns the ticker so it is stopped exactly when the loop exits.
		ticker := cfg.waiter.NewTicker(interval)
		//: release the ticker's resources once the loop returns.
		defer ticker.Stop()
		//: bound once: the channel belongs to the ticker this loop built and is
		//: never closed (clock.Ticker's contract).
		ticks := ticker.C()
		//: fire tick on each interval; exit promptly on Stop or on the owner's end.
		for {
			select {
			case <-stop:
				//: Stop signalled — return so the daemon's done channel closes.
				return
			case <-cfg.done:
				//: the owner's work is over; a nil done never selects.
				return
			case <-ticks:
				//: interval elapsed — run the caller's tick.
				tick()
			}
		}
	})
}

// resolveEvery folds opts onto the defaults: the wall clock and no early end.
func resolveEvery(opts []EveryOption) everyConfig {
	var cfg everyConfig
	//: options apply in order, so a later one deliberately wins.
	for _, opt := range opts {
		//: a nil entry in a sparse variadic list is skipped, not called.
		if opt == nil {
			//: nothing to apply.
			continue
		}
		cfg = opt(cfg)
	}
	//: no clock named, or a nil one: the wall clock, the only non-arbitrary default.
	if cfg.waiter == nil {
		cfg.waiter = clock.System
	}
	//: the resolved set.
	return cfg
}
