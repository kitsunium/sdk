package worker

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

// newLoopDaemon is NewLoopDaemon's body: decl_gen.go writes NewLoopDaemon, from the
// design, as one call of it.
func newLoopDaemon(loop Loop) *LoopDaemon {
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
