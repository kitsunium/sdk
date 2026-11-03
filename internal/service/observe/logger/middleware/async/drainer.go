package async

import corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"

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

// drain is the drainer loop body, shared by production and the white-box
// tests. Production runs it inside a worker.LoopDaemon (whose Stop owns the
// join); the tests spawn it directly via `go s.drain()` and join on done. It
// selects on the sink's own stop channel and closes done on exit so the
// test-path Close (which has no daemon) can join.
func (s *asyncSink) drain() {
	//: doneOnce guards close(done) so the test-path drainer exit is idempotent
	//: and the join signal for the test-path Close fires exactly once.
	defer s.doneOnce.Do(func() {
		//: signal the test-path Close that the drainer has fully exited.
		close(s.done)
	})
	//: run the shared loop body off the sink's stop channel.
	s.drainLoop(s.stop)
}

// drainLoop is the kernel-worker loop body: it drains ring entries into the
// downstream sink and returns promptly once stop is closed (the worker
// contract). On stop it flushes the ring via drainRemaining so nothing the
// producer accepted is lost. No done close here; the caller (drain() or the
// worker.LoopDaemon) owns the join signal.
//
// An empty ring parks the drainer on wake, the signal the next accepted Write
// sends (#806): an idle sink costs no CPU, where a Gosched loop kept one P busy
// with nobody to hand it to.
func (s *asyncSink) drainLoop(stop <-chan struct{}) {
	//: deliver what the ring holds; park when it holds nothing.
	for {
		//: up to batchMax entries, counted in flight before the lock drops.
		batch := s.take()
		//: something to deliver: deliver it and look again.
		if len(batch) > 0 {
			//: the slots just freed may let a Write waiting under Block in.
			s.signalSpace()
			s.deliver(batch)
			continue
		}
		//: the ring is empty: park until a Write or Close says otherwise.
		if !s.park(stop) {
			//: drain remaining entries before returning so the queue is empty.
			s.drainRemaining()
			//: drainer's terminal exit — the caller closes the join channel.
			return
		}
	}
}

// park waits for a producer's wake-up or for stop, and reports false on stop.
// parked is set BEFORE the ring is looked at once more under ringMu — the
// Dekker step: a Write that landed before the flag was visible is found here,
// and one that lands after it reads the flag (wakeDrainer) and sends. A stale
// token left by a Write the second look already caught costs one empty pass.
func (s *asyncSink) park(stop <-chan struct{}) bool {
	s.parked.Store(true)
	defer s.parked.Store(false)
	//: the second look: an entry that raced the flag is delivered at once.
	if s.pending() {
		//: not idle after all.
		return true
	}
	select {
	case <-stop:
		//: Close: the caller drains what is left and returns.
		return false
	case <-s.wake:
		//: a Write accepted an entry: back to the ring.
		return true
	}
}

// pending reports whether the ring holds an entry, read under ringMu as every
// ring access is.
func (s *asyncSink) pending() bool {
	s.ringMu.Lock()
	defer s.ringMu.Unlock()
	//: one entry is enough to resume.
	return s.queue.Len() > 0
}

// take reads up to batchMax entries off the ring into the drainer's scratch,
// under ringMu (async.Write under DropOldest also reads, so the SPSC ring never
// sees a concurrent consumer), and counts them in flight before releasing it,
// so a concurrent Flush never samples Len()==0 with inFlight==0 while they are
// undelivered. The returned slice is the drainer's scratch: valid until the
// next take.
func (s *asyncSink) take() []*recordEntry {
	batch := s.batch[:0]
	s.ringMu.Lock()
	//: stop at the first empty read or at batchMax entries.
	for len(batch) < s.batchMax {
		ent, err := s.queue.TryRead()
		//: the ring is empty.
		if err != nil {
			break
		}
		batch = append(batch, ent)
	}
	//: the entries left the ring but are not yet delivered downstream.
	s.inFlight += len(batch)
	s.ringMu.Unlock()
	//: keep the grown scratch for the next take.
	s.batch = batch
	return batch
}

// deliver hands batch to the downstream: in one WriteBatch when it is a
// corelogger.BatchSink and the batch holds more than one entry, one Write per
// entry otherwise — the record-at-a-time delivery a sink without the sibling
// port receives (#807).
func (s *asyncSink) deliver(batch []*recordEntry) {
	//: the record-at-a-time path.
	if s.batchSink == nil || len(batch) == 1 {
		for _, ent := range batch {
			s.forward(ent)
		}
		return
	}
	s.forwardBatch(batch)
}

// forwardBatch delivers batch through one WriteBatch, surfaces a failure
// through OnError once per record the batch did not deliver, then releases the
// entries as forward does for one.
func (s *asyncSink) forwardBatch(batch []*recordEntry) {
	recs, payloads := s.recs[:0], s.payloads[:0]
	for _, ent := range batch {
		recs = append(recs, ent.rec)
		payloads = append(payloads, ent.data)
	}
	written, err := s.batchSink.WriteBatch(asyncCtx(), recs, payloads)
	//: one report per undelivered record, never one per batch.
	if err != nil {
		for range len(batch) - min(max(written, 0), len(batch)) {
			s.onError(err)
		}
	}
	//: the scratch keeps no record or payload alive past the call.
	clear(recs)
	clear(payloads)
	s.recs, s.payloads = recs[:0], payloads[:0]
	s.settle(len(batch))
	for _, ent := range batch {
		s.recycle(ent)
	}
	clear(batch)
}

// forward delivers ent to the downstream sink and returns the entry to the
// recycler.
func (s *asyncSink) forward(ent *recordEntry) {
	//: the downstream sink owns its own concurrency model; surface errors
	//: through the configured OnError callback (or no-op default).
	n, err := s.downstream.Write(asyncCtx(), ent.rec, ent.data)
	s.forwardDownstreamError(n, err)
	s.settle(1)
	s.recycle(ent)
}

// settle records that n in-flight entries were delivered (written downstream
// or surfaced via OnError) and wakes a Flush waiter.
func (s *asyncSink) settle(n int) {
	//: drop the in-flight count under ringMu so Flush can observe the records
	//: as fully accounted for. Done before flushSignal so a woken Flush
	//: re-sampling inFlight sees the decremented value.
	s.ringMu.Lock()
	s.inFlight -= n
	s.ringMu.Unlock()
	//: signal any Flush waiter that progress was made — non-blocking send
	//: so an idle-Flush-less sink never stalls the drainer.
	select {
	case s.flushSignal <- struct{}{}:
		//: Flush (if waiting) wakes up and re-checks queue.Len().
	default:
		//: no Flush waiting (or its slot is already full) — drop the signal.
	}
}

// recycle clears ent and returns it to the pool.
func (s *asyncSink) recycle(ent *recordEntry) {
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
	//: keep taking until the ring reports empty.
	for {
		batch := s.take()
		//: empty queue ends the close-time flush.
		if len(batch) == 0 {
			//: queue is empty — close-time flush is complete.
			return
		}
		//: deliver and recycle as the steady-state path does.
		s.deliver(batch)
	}
}
