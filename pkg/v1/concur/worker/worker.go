//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/concur/worker .

// Package worker starts a background goroutine you can stop and join: one
// loop, an idempotent Stop that signals it and waits for it to return, and a
// Done channel closed once it has.
//
//	d := worker.Start(func(stop <-chan struct{}) {
//	    for {
//	        select {
//	        case <-stop:
//	            return
//	        case job := <-jobs:
//	            handle(job)
//	        }
//	    }
//	})
//	defer d.Stop() // signals the loop, then waits for it to return
//
// [Every] is the same daemon with a ticker in it, built on the clock you give
// it:
//
//	d := worker.Every(30*time.Second, flushMetrics)
//	defer d.Stop() // ends the ticking and joins the goroutine
//
// It is the loop primitive the SDK's own drainers, batching sinks and stream
// heartbeats run on, published as an alias of that kernel package (ADR 0159
// §4).
//
// # When to use it
//
// Wherever a component owns one background goroutine for its lifetime and
// would otherwise carry a stop channel, a sync.Once to close it, a done
// channel and a second Once — the scaffold this package exists to stop being
// copied. Use [Every] for a periodic task: it owns the ticker and stops it
// when the loop ends.
//
// For N tasks that run to completion and report errors, use concur/group
// instead: a [LoopDaemon] runs ONE loop, returns nothing, and lives until it
// is stopped.
//
// # The contract a loop keeps
//
// A [Loop] receives a stop channel and MUST return promptly once it is closed:
// Stop blocks on the join, so a loop that ignores stop deadlocks Stop. A Loop
// MUST NOT panic — it runs in a bare goroutine, where a panic ends the process;
// a loop running risky work recovers inside itself.
//
// # Ticking on an injected clock
//
// [WithClock] makes [Every] tick on any [clock.Waiter], the SDK's
// [clock.ManualClock] included, so a test asserts a cadence by moving time
// rather than by sleeping. The ticker is armed on the daemon's own goroutine:
// wait for it — ManualClock.BlockUntil(1) — before advancing the clock. A tick
// that comes due while the previous one still runs is dropped, not queued,
// as time.Ticker drops it. [WithDone] ends the loop when its owner's work ends
// on its own — a stream the peer closed — rather than at the owner's later
// Stop.
//
// A nil loop, a nil tick and a non-positive interval panic at the call that
// passed them, not later inside the spawned goroutine where they would take
// the process with them.
package worker

import (
	"time"

	kworker "github.com/kitsunium/sdk/internal/kernel/concur/worker"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// Loop is the goroutine body a [LoopDaemon] runs. It receives a stop channel,
// closed when Stop is called, and MUST return promptly once it is: Stop blocks
// until the loop has returned. A Loop MUST NOT panic.
type Loop = kworker.Loop

// LoopDaemon is a running background goroutine with an idempotent Stop and a
// join. [LoopDaemon].Stop signals the loop to exit and blocks until it has
// returned — idempotent, and safe to call from several goroutines.
// [LoopDaemon].Done returns the channel closed once the loop has returned, for
// a caller that reacts to the end without joining.
//
// Always used behind the pointer [Start], [NewLoopDaemon] or [Every] returns:
// it must not be copied.
type LoopDaemon = kworker.LoopDaemon

// EveryOption tunes [Every]: [WithClock] and [WithDone]. Options apply in
// order, so a later one wins, and a nil option is skipped.
type EveryOption = kworker.EveryOption

// Start spawns loop in a new goroutine and returns the [LoopDaemon] that
// controls it. A nil loop panics here rather than spawning a goroutine that
// does nothing.
func Start(loop Loop) *LoopDaemon {
	//: the kernel owns the daemon; this facade only forwards.
	return kworker.Start(loop)
}

// NewLoopDaemon is [Start] under the New-prefixed name: the same daemon, the
// same panic on a nil loop.
func NewLoopDaemon(loop Loop) *LoopDaemon {
	//: the kernel owns the daemon; this facade only forwards.
	return kworker.NewLoopDaemon(loop)
}

// Every starts a [LoopDaemon] whose loop calls tick on each interval until
// Stop is called, or until the channel given by [WithDone] is closed. The
// daemon owns the ticker — the wall clock unless [WithClock] names another —
// and stops it when the loop ends, so Stop both ends the ticking and joins
// the goroutine. A tick due while the previous one still runs is dropped.
//
// A nil tick and a non-positive interval panic here, at the caller.
func Every(interval time.Duration, tick func(), opts ...EveryOption) *LoopDaemon {
	//: the kernel owns the ticking loop; this facade only forwards.
	return kworker.Every(interval, tick, opts...)
}

// WithClock makes [Every] tick on w instead of the wall clock — a
// [clock.ManualClock] in a test, advanced past the interval once the ticker is
// armed. A nil w is the wall clock.
func WithClock(w clock.Waiter) EveryOption {
	//: the kernel owns the option set; this facade only forwards.
	return kworker.WithClock(w)
}

// WithDone ends the loop of [Every] as soon as done is closed, without waiting
// for Stop, which still joins — returning at once on a loop that has already
// left. A nil done never fires.
func WithDone(done <-chan struct{}) EveryOption {
	//: the kernel owns the option set; this facade only forwards.
	return kworker.WithDone(done)
}
