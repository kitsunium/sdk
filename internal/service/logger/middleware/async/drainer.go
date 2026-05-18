// Package async — declares the goroutine loop that consumes the
// ring buffer and forwards entries to the downstream sink. Pulled into its
// own file so async_sink.go stays focused on the Sink contract.
package async

import corelogger "github.com/kitsunium/sdk/internal/core/logger"

// maxSaneCap caps the per-entry backing array size the pool will retain
// after a forward() call. An attacker-influenced record (huge Message or
// attr value) otherwise leaves the entry with a massive cap(ent.data)
// which the pool keeps for the lifetime of the sink. Over many producers
// and many slots the pool can grow to hold several very large buffers
// (CWE-400 / CWE-789 — pool amplification). Dropping the slice reference
// when cap exceeds this threshold forces the pool to allocate a fresh
// small buffer on the next Get; 64 KiB comfortably covers every normal
// record while preventing unbounded retention.
const maxSaneCap int = 1 << 16

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
func (s *asyncSink) forward(ent *recordEntry) {
	//: the downstream sink owns its own concurrency model; surface errors
	//: through the configured OnError callback (or no-op default).
	n, err := s.downstream.Write(asyncCtx(), ent.rec, ent.data)
	s.forwardDownstreamError(n, err)
	//: signal any Flush waiter that progress was made — non-blocking send
	//: so an idle-Flush-less sink never stalls the drainer.
	select {
	case s.flushSignal <- struct{}{}:
		//: Flush (if waiting) wakes up and re-checks queue.Len().
	default:
		//: no Flush waiting (or its slot is already full) — drop the signal.
	}
	//: clear the entry's payload before recycling so leaked references release.
	ent.rec = corelogger.RecordEvent{}
	//: drop the backing array when it grew pathologically large (attacker-
	//: influenced payload) so the pool never retains unbounded memory.
	//: next Get() allocates a fresh small slice.
	if cap(ent.data) > maxSaneCap {
		//: release the oversized backing array for the GC to reclaim.
		ent.data = nil
	} else {
		//: normal recycle — truncate to zero length, keep the backing array.
		ent.data = ent.data[:0]
	}
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
