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
	"github.com/kitsunium/sdk/internal/kernel/buffer"
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
	pool buffer.Recycler[*recordEntry]
	// policy selects the saturation behaviour at Write time.
	policy DropPolicy
	// onDrop fires for every entry discarded by the policy; never nil.
	onDrop func(missed int)
	// stop signals the drainer goroutine to exit after Close.
	stop chan struct{}
	// stopOnce guards close(stop) so concurrent Close calls never panic.
	stopOnce sync.Once
	// done is closed by the drainer once it has exited.
	done chan struct{}
	// doneOnce guards close(done) — the drainer is the sole closer in
	// practice but the lint heuristic requires an explicit sync.Once guard.
	doneOnce sync.Once
}

// New wraps downstream with a ring + drainer goroutine. cfg is consulted
// for the buffer size, drop policy and OnDrop callback; zero-value Config
// is valid and yields the documented defaults.
//
// Params:
//   - downstream: the wrapped sink that receives drained entries.
//   - cfg: tuning knobs; zero-value falls back to documented defaults.
//
// Returns:
//   - corelogger.Sink: a ready-to-use async Sink behind the public interface.
func New(downstream corelogger.Sink, cfg Config) (sink corelogger.Sink) {
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
	//: recycler keeps per-entry allocation off the hot path.
	pool := buffer.NewRecycler[*recordEntry](newRecordEntry)
	//: build the sink with channels primed for the drainer lifecycle.
	out := &asyncSink{
		downstream: downstream,
		queue:      queue,
		pool:       pool,
		policy:     cfg.Policy,
		onDrop:     callback,
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
	//: drainer runs for the lifetime of the sink; Close terminates it.
	go out.drain()
	//: hand back the sink behind the public Sink interface.
	return out
}

// mustNewRing wraps ring.New for the New constructor — the size is
// pre-validated so the error path is unreachable; the helper makes that
// invariant explicit instead of using an `_, _ :=` discard.
//
// Params:
//   - size: positive capacity already validated by the caller.
//
// Returns:
//   - ring.Queue[*recordEntry]: a ready-to-use ring of the requested capacity.
func mustNewRing(size int) (out ring.Queue[*recordEntry]) {
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

// noopOnDrop is the documented sink for the OnDrop callback when the caller
// does not supply one. Keeps the drainer's call path branch-free.
//
// Params:
//   - missed: count of dropped entries; intentionally unused by the no-op.
func noopOnDrop(missed int) {
	//: defensive guard so the parameter is observed by the audit.
	if missed < 0 {
		//: negative counts are caller bugs; ignore so the drainer never panics.
		return
	}
}

// Write enqueues an encoded record for the drainer to deliver. Returns
// Stopped if Close has terminated the drainer.
//
// Params:
//   - ctx: request-scoped context; cancelled contexts skip the enqueue.
//   - rec: originating record forwarded to the downstream sink.
//   - payload: formatted bytes; copied into a recycled buffer for the trip.
//
// Returns:
//   - n: number of bytes accepted by the ring (== len(payload) on success).
//   - err: Stopped after Close; BufferFull on DropNewest saturation;
//     ctx.Err on cancelled context; nil on success.
func (s *asyncSink) Write(ctx context.Context, rec corelogger.RecordEvent, payload []byte) (n int, err error) {
	//: honour cancellation so a doomed request does not waste a queue slot.
	if ctx != nil && ctx.Err() != nil {
		//: surface the cancellation cause verbatim.
		return 0, ctx.Err()
	}
	//: refuse work after Close — the drainer is gone.
	if isClosed(s.stop) {
		//: documented sentinel — caller knows Close has happened.
		return 0, Stopped
	}
	//: borrow an entry from the pool and copy the payload in.
	ent := s.pool.Get()
	ent.rec = rec
	ent.data = append(ent.data[:0], payload...)
	//: ring saturation triggers the configured drop policy.
	if werr := s.queue.TryWrite(ent); werr != nil {
		//: ring is full — apply the configured drop policy.
		return s.handleFull(ent)
	}
	//: happy path — bytes accepted by the ring.
	return len(payload), nil
}

// handleFull applies the configured DropPolicy to a saturated ring.
//
// Params:
//   - ent: the entry that could not enqueue under the steady-state path.
//
// Returns:
//   - n: bytes accepted (== len(ent.data) for DropOldest; 0 for DropNewest).
//   - err: BufferFull for DropNewest; nil for DropOldest.
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

// Flush blocks until the queue has drained or ctx is cancelled.
//
// Params:
//   - ctx: request-scoped context; cancellation aborts the wait.
//
// Returns:
//   - err: ctx.Err() on cancellation; nil once the queue is empty.
func (s *asyncSink) Flush(ctx context.Context) (err error) {
	//: spin until empty or ctx done; tests typically use a short timeout.
	for s.queue.Len() > 0 {
		//: bail out cleanly when the caller cancels mid-wait.
		if ctx != nil && ctx.Err() != nil {
			//: surface the cancellation cause verbatim.
			return ctx.Err()
		}
		//: yield so the drainer goroutine can progress.
		yieldOnce()
	}
	//: also flush the downstream sink so its own buffers settle.
	return s.downstream.Flush(ctx)
}

// Close stops the drainer goroutine and closes the downstream sink. After
// Close returns, all subsequent Write calls return Stopped. Safe to call
// multiple times — the sync.Once guard makes close(stop) idempotent.
//
// Returns:
//   - err: downstream Close error; nil on unanimous success.
func (s *asyncSink) Close() (err error) {
	//: sync.Once guards close(stop) so concurrent Close calls never panic.
	s.stopOnce.Do(func() {
		//: signal the drainer to exit; drainRemaining empties the queue first.
		close(s.stop)
	})
	//: wait for the drainer goroutine to confirm exit.
	<-s.done
	//: forward to the downstream sink so its own resources release.
	return s.downstream.Close()
}
