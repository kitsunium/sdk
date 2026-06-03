// Package dbsink is the driver-AGNOSTIC database sink shell (ADR 0015, gated).
// It is the DB analogue of the S3 batching sink (third-party/aws/writer/s3):
// records are coalesced into batches by the generic kernel batcher and handed
// to a single deliver seam — execBatch — when the item-count cap is reached, on
// the optional flush ticker, or on Flush / Close. The seam is the only thing a
// concrete driver supplies; this package imports NO database driver, NO vendor
// SDK, and NO new core sibling, so it stays stdlib-only and dep-light. The real
// driver adapters (third-party/db/writer/{mysql,clickhouse,redis}) wrap New
// with their own execBatch closure and self-register as writer factories.
//
// Composition: a concrete driver wires levelgate(async(dbSink)) — the gate
// drops below-floor records before the ring, async gives a non-blocking ring +
// OnDrop back-pressure, and the dbSink batches the rest. Compose builds that
// chain for them so the ordering lives in one place (mirroring the s3 factory).
//
// Ownership / allocation: dbSink.Write runs on the async drainer goroutine (the
// producer call already returned at async.Write), and RecordEvent is an
// immutable snapshot by contract, so no defensive per-record clone is needed —
// the batcher's appended value safely outlives async's recycled entry. This
// package therefore makes NO zero-alloc claim on Write: the only Write-side cost
// is the amortised growth of the batcher's pending slice, and the zero-alloc
// invariant belongs to the producer's Build().Send() hot path (ADR 0014), not to
// a deferring sink. CPU/RAM stay minimal by coalescing many records into one
// execBatch round-trip — the DB analogue of the s3 sink's batched object upload.
package dbsink

import (
	"context"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/batcher"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// defaultMaxRows bounds the in-memory batch when Config leaves MaxRows at its
// zero value. Row-count is the natural cap for a DB insert: most engines bound
// a multi-row INSERT / pipeline by statement count, not by byte size.
const defaultMaxRows int = 256

// execBatch is the deliver seam over a database: it persists one coalesced
// batch of records, returning a non-nil error to fail the batch. A func type
// (not an interface) keeps the real driver call an anonymous closure in each
// concrete adapter — confining the driver/SDK there — while tests inject a
// recording closure, so the batching logic needs no database and the seam
// carries no untestable named method. It is the DB analogue of s3's uploadFunc.
type execBatch func(ctx context.Context, batch []corelogger.RecordEvent) error

// dbSink coalesces formatted records into batched database writes. The
// coalesce / flush / ticker lifecycle is delegated to a kernel batcher whose
// items are the cloned RecordEvents; the deliver closure forwards the batch to
// execBatch. The concrete type stays unexported (IFACE-PLUGIN): New returns it
// behind the core/logger.Sink interface.
type dbSink struct {
	// exec is the driver-supplied deliver seam, captured at construction.
	exec execBatch
	// onError surfaces cap-triggered delivery failures seen on the Write path;
	// never nil after newDBSink (a nil hook degrades to a no-op).
	onError func(err error)
	// clk sources the timestamp when RecordEvent.Time is the zero value; never
	// nil after newDBSink (a nil Config.Clock defaults to clock.System).
	clk clock.Clock
	// batch is the generic coalescing buffer; each item is one cloned record.
	batch *batcher.Batcher[corelogger.RecordEvent]
}

// newDBSink builds a batching sink over exec. When flushEvery > 0 the underlying
// batcher spawns a ticker goroutine that flushes on the interval; the goroutine
// is joined by Close. A non-positive maxRows substitutes defaultMaxRows so the
// batch can never grow unbounded.
func newDBSink(exec execBatch, maxRows int, cfg Config) *dbSink {
	//: substitute the default cap when the caller left MaxRows at zero.
	if maxRows <= 0 {
		//: 256 rows comfortably absorbs a burst without unbounded growth.
		maxRows = defaultMaxRows
	}
	onError := cfg.OnError
	//: degrade a nil error hook to a no-op so the Write path stays branch-free.
	if onError == nil {
		//: discard cap-flush errors when the caller wired no observer.
		onError = func(error) {}
	}
	clk := cfg.Clock
	//: fall back to the real wall clock so the Write-path timestamp fill is
	//: always callable; tests inject a fake Clock for deterministic Time.
	if clk == nil {
		//: default to the stdlib-backed system clock when none is injected.
		clk = clock.System
	}
	s := &dbSink{exec: exec, onError: onError, clk: clk}
	//: count-based batching: a DB insert is bounded by row count, not bytes, so
	//: WeightOf is left nil (every record weighs one slot) and MaxItems caps it.
	s.batch = batcher.NewBatcher(s.deliver, batcher.Config[corelogger.RecordEvent]{
		MaxItems:   maxRows,
		FlushEvery: cfg.FlushEvery,
		OnError:    onError,
	})
	//: hand back the constructed batching sink.
	return s
}

// Write appends the originating record to the current batch; the batcher flushes
// eagerly once the row cap is reached. It never blocks on the database — the
// producer is decoupled by the async middleware wrapping this sink — and a
// cap-triggered batch failure is routed to onError, not returned, so a slow /
// failing DB never propagates to the logging call site. A zero RecordEvent.Time
// is the documented "fill at handle time" sentinel; since the DB path persists
// the structured record (the payload p is ignored, re-serialised per wire
// protocol in execBatch) it must do the fill itself — the format-side encoder's
// fill is invisible here — so Write stamps clk.Now() before batching to keep the
// drivers from persisting a year-0001 timestamp.
func (s *dbSink) Write(ctx context.Context, r corelogger.RecordEvent, p []byte) (n int, err error) {
	//: fill the handle-time timestamp on this local value copy before batching so
	//: every driver persists a real instant; r is passed by value, so the stamp
	//: never escapes back to the caller's record.
	if r.Time.IsZero() {
		//: injected clock keeps the fill deterministic under test.
		r.Time = s.clk.Now()
	}
	//: append the record value to the pending batch. r is an immutable snapshot
	//: by contract (core/logger: RecordEvent is read-only after construction),
	//: and dbSink.Write already runs on the async drainer goroutine — the
	//: producer's call returned at async.Write — so no defensive clone is needed
	//: to cross the goroutine boundary: the batcher's appended value outlives
	//: the recycled async entry (async overwrites ent.rec wholesale per Write,
	//: never the Attrs array this value references). The sole Write-side
	//: allocation is therefore the amortised batch-slice growth, not a per-record
	//: copy — this sink makes NO zero-alloc claim on Write (the zero-alloc
	//: invariant lives on the producer's Build().Send() hot path, ADR 0014).
	if aerr := s.batch.Add(ctx, r); aerr != nil {
		//: an eager cap-flush failed; route it to the observer, never the caller.
		s.onError(aerr)
	}
	//: report the payload as accepted regardless of the delivery outcome.
	return len(p), nil
}

// Flush delivers the pending batch synchronously, returning any execBatch error
// to the caller (the caller-facing path propagates rather than routing to
// OnError).
func (s *dbSink) Flush(ctx context.Context) error {
	//: caller-facing flush propagates the error rather than routing to OnError.
	return s.batch.Flush(ctx)
}

// Close stops the ticker (if any), delivers the final batch, and returns the
// flush error. Close has no caller ctx, so it uses a background context; the
// batcher joins the ticker goroutine before the final flush.
func (s *dbSink) Close() error {
	//: background context — Close has no caller ctx; the batcher joins the ticker.
	return s.batch.Close(context.Background())
}

// deliver forwards a coalesced batch of records to the execBatch seam. It runs
// outside the batcher's lock, so a slow database round-trip never blocks
// producers. The batcher already wraps a non-nil result as its typed
// DeliverFailed sentinel (the driver's own cause stays reachable via
// errors.Is), so deliver only needs to relay the seam's verdict.
func (s *dbSink) deliver(ctx context.Context, items []corelogger.RecordEvent) error {
	//: relay the batch to the driver-supplied seam; a nil exec is rejected at
	//: construction (Compose guards it), so exec is always callable here.
	return s.exec(ctx, items)
}
