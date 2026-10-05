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
