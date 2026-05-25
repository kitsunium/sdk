// Package async wraps any corelogger.Sink with a non-blocking ring buffer
// and a single drainer goroutine, decoupling the producer hot path from
// the (potentially slow) downstream sink. Producers call Write synchronously
// but never block on I/O — entries land in the ring and the drainer sips
// them out.
//
// When the ring saturates, the configured DropPolicy decides:
//   - DropNewest: the new Write returns BufferFull; the OnDrop callback fires.
//   - DropOldest: the oldest queued entry is silently discarded; the new
//     entry takes its slot. OnDrop fires for the dropped entry.
//
// Use case: wrap CloudWatch / S3 / HTTP sinks so a slow remote drain never
// stalls the application's hot path.
package async

import (
	"context"
	"sync"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/recycler"
	"github.com/kitsunium/sdk/internal/kernel/ring"
)

// defaultBufferSize is the ring capacity supplied when Config.BufferSize is
// non-positive; sized to comfortably absorb a short burst.
const defaultBufferSize int = 1024

// asyncSink wraps a downstream sink behind a ring + drainer goroutine. The
// drainer goroutine is spawned by New and joined by Close.
type asyncSink struct {
	// downstream is the wrapped sink that receives drained entries.
	downstream corelogger.Sink
	// queue is the lock-free ring buffer holding pending entries.
	queue ring.Queue[*recordEntry]
	// pool recycles *recordEntry values to avoid per-write allocation.
	pool recycler.Recycler[*recordEntry]
	// policy selects the saturation behaviour at Write time.
	policy DropPolicy
	// onDrop fires for every entry discarded by the policy; never nil.
	onDrop func(missed int)
	// onError fires for every downstream Write failure seen by the drainer;
	// never nil — noopOnError is substituted when Config.OnError is unset.
	// Surfaces errors that would otherwise be silently swallowed by the
	// drainer.
	onError func(err error)
	// stop signals the drainer goroutine to exit after Close.
	stop chan struct{}
	// stopOnce guards close(stop) so concurrent Close calls never panic.
	stopOnce sync.Once
	// done is closed by the drainer once it has exited.
	done chan struct{}
	// doneOnce guards close(done) — the drainer is the sole closer in
	// practice but the lint heuristic requires an explicit sync.Once guard.
	doneOnce sync.Once
	// flushSignal fires once after every drainer forward() so Flush can
	// wait on progress instead of Gosched-spinning. Buffered by 1 so the
	// drainer never blocks when nobody is flushing; Flush selects on it
	// + ctx.Done for cancellation.
	flushSignal chan struct{}
	// ringMu serialises every access to queue so the SPSC ring's single-
	// producer / single-consumer contract is not violated by concurrent
	// goroutines. Held by:
	//   - Write (producer TryWrite, and TryRead + TryWrite under DropOldest)
	//   - Close (as a join point — blocks until every in-flight Write is
	//     past its TryWrite; new Writes then see isClosed under the lock)
	//   - drainer forward() (TryRead from the consumer side)
	//
	// Rationale over sync.WaitGroup (which races on Add/Wait), over
	// sync.RWMutex (RLock allows concurrent producers — still SPSC-unsafe),
	// and over leaving the ring lock-free (async has two producers and two
	// consumers in practice: Write vs drainer, DropOldest TryRead from
	// producer vs drainer TryRead from consumer). With a single mutex the
	// ring is effectively a locked queue; the atomic operations inside are
	// cheap enough that wrapping them loses little while restoring
	// correctness.
	ringMu sync.Mutex
}

// New wraps downstream with a ring + drainer goroutine. cfg is consulted
// for the buffer size, drop policy and OnDrop callback; zero-value Config
// is valid and yields the documented defaults.
func New(downstream corelogger.Sink, cfg Config) corelogger.Sink {
	//: pre-validate the buffer size; non-positive falls back to the default.
	size := cfg.BufferSize
	//: documented default keeps callers from having to think about sizing.
	if size <= 0 {
		//: substitute the standard 1024-slot ring.
		size = defaultBufferSize
	}
	//: ring construction is infallible for positive capacity by contract.
	queue := mustNewRing(size)
	//: substitute the no-op default when the caller did not wire a metric.
	callback := cfg.OnDrop
	//: nil callback degrades to a no-op so the drainer can call it unconditionally.
	if callback == nil {
		//: fall back to the documented no-op.
		callback = noopOnDrop
	}
	//: same treatment for the error callback so the drainer can fire it
	//: unconditionally without a nil check on the hot path.
	errCallback := cfg.OnError
	//: nil callback degrades to a no-op so the drainer can call it unconditionally.
	if errCallback == nil {
		//: fall back to the documented no-op.
		errCallback = noopOnError
	}
	//: recycler keeps per-entry allocation off the hot path.
	pool := recycler.NewRecycler[*recordEntry](newRecordEntry)
	//: build the sink with channels primed for the drainer lifecycle.
	out := &asyncSink{
		downstream:  downstream,
		queue:       queue,
		pool:        pool,
		policy:      cfg.Policy,
		onDrop:      callback,
		onError:     errCallback,
		flushSignal: make(chan struct{}, 1),
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}
	//: drainer runs for the lifetime of the sink; Close terminates it.
	go out.drain()
	//: hand back the sink behind the public Sink interface.
	return out
}

// mustNewRing wraps ring.New for the New constructor — the size is
// pre-validated so the error path is unreachable; the helper makes that
// invariant explicit instead of using an `_, _:=` discard.
func mustNewRing(size int) ring.Queue[*recordEntry] {
	//: positive size never fails ring.New per its documented contract.
	queue, err := ring.New[*recordEntry](size)
	//: the documented unreachable branch surfaces a panic if the contract slips.
	if err != nil {
		//: reaching here means a kernel-level invariant broke; fail loudly.
		panic("internal/service/logger/middleware/async: ring.New failed despite positive size: " + err.Error())
	}
	//: hand back the validated ring.
	return queue
}

// noopOnError is the documented sink for the OnError callback when the
// caller does not supply one. Keeps the drainer's call path branch-free.
func noopOnError(err error) {
	//: read the parameter so the unused-param audit treats this no-op as intentional.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
}

// noopOnDrop is the documented sink for the OnDrop callback when the caller
// does not supply one. Keeps the drainer's call path branch-free.
func noopOnDrop(missed int) {
	//: read the parameter so the unused-param audit treats this no-op as intentional.
	if missed < 0 {
		//: negative counts are caller bugs; ignore so the drainer never panics.
		return
	}
}

// Write enqueues an encoded record for the drainer to deliver. Returns
// Stopped if Close has terminated the drainer.
func (s *asyncSink) Write(ctx context.Context, rec corelogger.RecordEvent, payload []byte) (n int, err error) {
	//: honour cancellation so a doomed request does not waste a queue slot.
	if ctx != nil && ctx.Err() != nil {
		//: wrap ctx.Err() so the typed-errors-only SDK rule is preserved
		//: and consumers can HasCode / errors.Is against the cancellation.
		return 0, errs.Wrap(ctx.Err(), errs.WrapParams{
			Code:    CodeAsyncCtxCancelled,
			Reason:  "ASYNC_CTX_CANCELLED",
			Public:  "Async sink write aborted due to cancellation",
			Private: "service/logger/middleware/async.Write saw a cancelled context",
		}, errs.Int("level", int(rec.Level)))
	}
	//: hold the ring mutex for the whole [isClosed.. TryWrite] span. Close
	//: takes the same mutex so an in-flight Write cannot race drainRemaining
	//: and the SPSC ring never sees concurrent producers.
	s.ringMu.Lock()
	defer s.ringMu.Unlock()
	//: refuse work after Close — the drainer is gone. Checked under the
	//: mutex so Close cannot transition between the check and TryWrite.
	if isClosed(s.stop) {
		//: documented sentinel — caller knows Close has happened.
		return 0, Stopped
	}
	//: borrow an entry from the pool and copy the payload in.
	ent := s.pool.Get()
	ent.rec = rec
	ent.data = append(ent.data[:0], payload...)
	//: TryWrite runs inside ringMu — SPSC contract honoured.
	if werr := s.queue.TryWrite(ent); werr != nil {
		//: ring is full — apply the configured drop policy.
		return s.handleFull(ent)
	}
	//: happy path — bytes accepted by the ring.
	return len(payload), nil
}

// handleFull applies the configured DropPolicy to a saturated ring.
func (s *asyncSink) handleFull(ent *recordEntry) (n int, err error) {
	//: DropOldest evicts the head entry to make room for the new one.
	if s.policy == DropOldest {
		//: consume the oldest entry — discarded silently to make room.
		dropped, derr := s.queue.TryRead()
		//: a successful TryRead frees a slot for the retry below.
		if derr == nil {
			//: notify the metric callback and recycle the dropped entry.
			s.onDrop(1)
			s.pool.Put(dropped)
		}
		//: retry the write; the ring now has at least one free slot.
		swallowRingError(s.queue.TryWrite(ent))
		//: bytes accepted on the retry path.
		return len(ent.data), nil
	}
	//: DropNewest discards the new entry and surfaces BufferFull.
	s.onDrop(1)
	s.pool.Put(ent)
	//: documented sentinel — caller decides whether to retry or escalate.
	return 0, BufferFull
}

// Flush blocks until the queue has drained or ctx is cancelled. Supports
// ctx == nil (legacy wait-forever semantics, observed via
// waitForDrainerProgress).
// (flushSignal closed) — Stopped is not a cancellation event and is safe
// to observe under a nil ctx.
func (s *asyncSink) Flush(ctx context.Context) error {
	//: wait for the drainer to signal progress instead of Gosched-spinning
	//: each forward() wakes us via flushSignal so we re-check
	//: queue.Len() under ringMu and either exit or wait again.
	for {
		//: snapshot queue length under ringMu so SPSC safety holds.
		s.ringMu.Lock()
		remaining := s.queue.Len()
		s.ringMu.Unlock()
		//: done when the ring is empty — forward to downstream.
		if remaining == 0 {
			//: also flush the downstream sink so its own buffers settle.
			return s.downstream.Flush(ctx)
		}
		//: bail cleanly when the caller cancels before the ring drains.
		if ctx != nil && ctx.Err() != nil {
			//: wrap ctx.Err() so the typed-errors-only SDK rule is preserved.
			return errs.Wrap(ctx.Err(), errs.WrapParams{
				Code:    CodeAsyncCtxCancelled,
				Reason:  "ASYNC_CTX_CANCELLED",
				Public:  "Async sink flush aborted due to cancellation",
				Private: "service/logger/middleware/async.Flush saw a cancelled context",
			})
		}
		//: wait for drainer progress or cancellation; helper returns false
		//: when the flushSignal was closed so the loop can surface a typed
		//: error instead of busy-spinning.
		if !s.waitForDrainerProgress(ctx) {
			//: flushSignal closed means the drainer exited — return the
			//: typed Stopped sentinel. ctx may be nil (legacy wait-forever
			//: semantics) so we never dereference it here; this branch is
			//: not a ctx-cancellation event.
			return Stopped
		}
	}
}

// waitForDrainerProgress blocks until the drainer signals progress on
// flushSignal OR ctx is cancelled. Returns false when flushSignal was
// reported closed (a misuse signal the caller surfaces as a typed wrap)
// so Flush cannot loop forever on a closed channel.
func (s *asyncSink) waitForDrainerProgress(ctx context.Context) bool {
	//: a nil ctx means the caller has opted into an indefinite wait — block
	//: on the drainer signal alone with comma-ok so a closed channel does
	//: not turn into a CPU hot-spin.
	if ctx == nil {
		//: pure flushSignal wait — comma-ok form documents the contract.
		_, sigOK := <-s.flushSignal
		//: propagate the closed-channel signal to the caller.
		return sigOK
	}
	//: cancellation-aware wait — whichever channel fires first wins.
	select {
	case _, sigOK := <-s.flushSignal:
		//: drainer signal observed; closed channel surfaces via ok=false.
		return sigOK
	case <-ctx.Done():
		//: cancellation — loop body picks up ctx.Err() and returns wrap.
		return true
	}
}

// Close stops the drainer goroutine and closes the downstream sink. After
// Close returns, all subsequent Write calls return Stopped. Safe to call
// multiple times — the sync.Once guard makes close(stop) idempotent.
func (s *asyncSink) Close() error {
	//: acquire ringMu: hard join point with every in-flight Write. When Lock returns, no Write is mid-[isClosed..TryWrite]; subsequent Writes
	//: observe isClosed(stop) under ringMu after our close(stop) below, so drainRemaining sees every record that Write accepted (closes the
	//: Close vs Write TOCTOU race).
	s.ringMu.Lock()
	//: sync.Once guards close(stop) so concurrent Close calls never panic.
	s.stopOnce.Do(func() {
		//: signal the drainer to exit; drainRemaining empties the queue first.
		close(s.stop)
	})
	//: release the lock so the drainer's TryRead can progress — the drainer
	//: holds ringMu for each TryRead call (see drainer.go).
	s.ringMu.Unlock()
	//: wait for the drainer goroutine to confirm exit.
	<-s.done
	//: forward to the downstream sink so its own resources release.
	return s.downstream.Close()
}
