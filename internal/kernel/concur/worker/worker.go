// Package worker provides LoopDaemon, a generic goroutine lifecycle primitive.
// A LoopDaemon spawns a single background goroutine running a Loop, exposes an
// idempotent Stop that signals the loop to exit and joins it, and a Done
// channel closed once the loop has returned. Stdlib-only and domain-neutral:
// any background drainer, batcher, ticker, or watch loop can reuse it.
//
// Three real consumers collapse onto this primitive — the async middleware
// drainer and the s3/cloudwatch batching sinks — each previously carried a
// byte-identical stop/stopOnce/done/doneOnce scaffold. The mechanism lives
// here; the loop body stays with the consumer (ADR 0014 §D6).
//
// The "Daemon" role suffix (the linter requires a recognized role suffix on
// every exported struct, and the bare noun is rejected); Start / Every are the
// idiomatic constructors, with NewLoopDaemon as the New-prefixed alias the
// struct-constructor lint expects.
package worker

import "sync"

// Loop is the goroutine body run by a LoopDaemon. It receives a stop channel
// that is closed when Stop is called and MUST return promptly once stop is
// closed — a Loop that ignores stop deadlocks Stop, which blocks on the join.
// A Loop MUST NOT panic: it runs in a bare goroutine, so a panic crashes the
// process. Callers that run risky work recover inside their own Loop.
type Loop func(stop <-chan struct{})

// LoopDaemon is a running background goroutine with an idempotent Stop and a
// join. It is a concrete struct (not an interface) by deliberate choice,
// mirroring recycler.Pool / snapshot.Value — a single-impl interface would be
// over-abstraction. Always used behind a pointer returned by Start: the
// embedded sync.Once and channels must not be copied.
type LoopDaemon struct {
	// stop is closed once by Stop to signal the loop to exit.
	stop chan struct{}
	// stopOnce guards close(stop) so concurrent Stop calls never panic.
	stopOnce sync.Once
	// done is closed by the spawning goroutine once the loop has returned;
	// Stop blocks on it to join.
	done chan struct{}
	// doneOnce guards close(done). The spawning goroutine is the sole closer
	// in practice, but the lint heuristic (KTN-GOROUTINE-DOUBLECLOSE) requires
	// an explicit guard on every close(field).
	doneOnce sync.Once
}

// Start spawns loop in a new goroutine and returns the LoopDaemon controlling
// it. The returned daemon's Done channel closes when loop returns; Stop signals
// loop to exit and blocks until it has. A nil loop is a programmer error and
// panics at construction rather than spawning a goroutine that does nothing.
func Start(loop Loop) *LoopDaemon {
	//: refuse a nil loop loudly at construction, not with a silent no-op goroutine.
	if loop == nil {
		//: programmer error — fail fast at the call site.
		panic("worker: nil loop")
	}
	//: build the daemon with channels primed for the lifecycle.
	ld := &LoopDaemon{
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	//: spawn the loop; closing done on return is the join signal for Stop.
	go func() {
		//: guarantee done closes even if the loop returns early or via
		//: runtime.Goexit; doneOnce keeps the close single + lint-clean.
		defer ld.doneOnce.Do(func() {
			//: the loop has returned — release every Stop waiter.
			close(ld.done)
		})
		//: run the caller's loop body, handing it the stop channel.
		loop(ld.stop)
	}()
	//: hand the controller back to the caller.
	return ld
}

// NewLoopDaemon is the New-prefixed constructor alias mandated by the
// struct-constructor lint; it delegates to Start, which is the idiomatic verb.
func NewLoopDaemon(loop Loop) *LoopDaemon {
	//: single source of truth — Start owns the spawn + validation.
	return Start(loop)
}

// Stop signals the loop to exit and blocks until it has returned. It is
// idempotent and safe to call concurrently: the sync.Once guards close(stop),
// and the join on done is a receive on a closed channel after the first call.
func (d *LoopDaemon) Stop() {
	//: sync.Once makes close(stop) safe under concurrent / repeated Stop.
	d.stopOnce.Do(func() {
		//: signal the loop to exit; it MUST observe this and return promptly.
		close(d.stop)
	})
	//: join — block until the loop's goroutine has closed done.
	<-d.done
}

// Done returns the channel closed when the loop has returned. Callers that
// want to react to loop exit without joining (which Stop does) select on it.
func (d *LoopDaemon) Done() <-chan struct{} {
	//: expose the join channel read-only so callers can observe exit.
	return d.done
}
