// Package async: drainer.go declares the goroutine loop that consumes the
// ring buffer and forwards entries to the downstream sink. Pulled into its
// own file so async_sink.go stays focused on the Sink contract.
package async

import corelogger "github.com/kitsunium/sdk/internal/core/logger"

// drain runs in the background draining ring entries into the downstream
// sink. Exits when the stop channel is closed AND the queue is empty.
func (s *asyncSink) drain() {
	//: doneOnce guards close(done) so the drainer's exit is idempotent.
	defer s.doneOnce.Do(func() {
		//: signal Close that the drainer has fully exited.
		close(s.done)
	})
	//: spin-loop drains the ring; sleeps when empty until a stop signal arrives.
	for {
		//: read under ringMu so the SPSC ring never sees a concurrent
		//: producer (async.Write under DropOldest also reads). Held for
		//: the TryRead only — forward runs outside the lock to avoid
		//: holding it across the downstream Write.
		s.ringMu.Lock()
		ent, err := s.queue.TryRead()
		s.ringMu.Unlock()
		//: empty queue → fall through to the stop-channel select.
		if err == nil {
			//: forward the entry to the downstream sink and recycle it.
			s.forward(ent)
			continue
		}
		//: queue is empty — block on stop or yield to other goroutines.
		select {
		case <-s.stop:
			//: drain remaining entries before returning so the queue is empty.
			s.drainRemaining()
			//: drainer's terminal exit — defer above closes the done channel.
			return
		default:
			//: brief yield avoids burning a full core when idle.
			yieldOnce()
		}
	}
}

// forward delivers ent to the downstream sink and returns the entry to the
// recycler.
//
// Params:
//   - ent: the entry pulled out of the ring by the drainer.
func (s *asyncSink) forward(ent *recordEntry) {
	//: the downstream sink owns its own concurrency model and error handling.
	swallowDownstreamError(s.downstream.Write(asyncCtx(), ent.rec, ent.data))
	//: clear the entry's payload before recycling so leaked references release.
	ent.rec = corelogger.RecordEvent{}
	ent.data = ent.data[:0]
	s.pool.Put(ent)
}

// drainRemaining flushes the ring after a stop signal so no in-flight
// entries are dropped on Close.
func (s *asyncSink) drainRemaining() {
	//: keep reading until TryRead reports Empty.
	for {
		//: pull the next entry without blocking, under ringMu for SPSC
		//: safety (DropOldest's producer-side TryRead shares the ring).
		s.ringMu.Lock()
		ent, err := s.queue.TryRead()
		s.ringMu.Unlock()
		//: empty queue ends the close-time flush.
		if err != nil {
			//: queue is empty — close-time flush is complete.
			return
		}
		//: forward and recycle as the steady-state path does.
		s.forward(ent)
	}
}
