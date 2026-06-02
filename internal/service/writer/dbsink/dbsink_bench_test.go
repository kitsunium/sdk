package dbsink_test

import (
	"context"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/service/writer/dbsink"
)

// benchSink is a package-level sink that defeats dead-code elimination so the
// composed Sink built per benchmark is not optimised away.
var benchSink corelogger.Sink

// benchN parks the per-Write byte count so the compiler cannot prove the Write
// is side-effect-free and elide it.
var benchN int

// benchDrops parks the count of back-pressure drops so the compiler cannot elide
// the error read; drops are an EXPECTED outcome under sustained load (the async
// ring saturates faster than the no-op seam drains), not a benchmark failure.
var benchDrops int

// benchConfig is the drain-friendly config the Write benchmarks share: a small
// row cap so the batcher flushes the no-op seam continuously (keeping the async
// ring drained) and a large ring so the steady state measures Write cost rather
// than back-pressure churn.
var benchConfig = dbsink.Config{MaxRows: 64, BufferSize: 1 << 16}

// benchWriteLoop drives Write for the whole benchmark, parking the byte count and
// tolerating expected back-pressure drops. Factored out so both benchmarks share
// the identical hot loop.
func benchWriteLoop(b *testing.B, sink corelogger.Sink, rec corelogger.RecordEvent, p []byte) {
	b.Helper()
	ctx := b.Context()
	b.ReportAllocs()
	//: drive Write only; Flush/Close lifecycle is out of the hot-path scope.
	for b.Loop() {
		//: park n + any drop so DCE cannot elide the call; a saturated-ring
		//: BufferFull is expected back-pressure under load, not a failure.
		n, werr := sink.Write(ctx, rec, p)
		benchN += n
		if werr != nil {
			benchDrops++
		}
	}
}

// noopExec is a deliver seam that discards the batch: the benchmarks measure the
// Write-side cost (level check + ring enqueue + batch append), not the database
// round-trip, so the seam does nothing.
func noopExec(_ context.Context, _ []corelogger.RecordEvent) error {
	//: discard the batch — the producer-side cost is what these benches isolate.
	return nil
}

// BenchmarkWrite_NoAttrs measures Write for a record with no structured Attrs.
// It documents the honest cost of the shell's deferring design — this sink makes
// NO zero-alloc claim on Write (the zero-alloc invariant lives on the producer's
// Build().Send() hot path, ADR 0014); a batching sink that defers delivery into
// an async ring carries an amortised per-record buffering cost, not zero.
func BenchmarkWrite_NoAttrs(b *testing.B) {
	sink := dbsink.Compose(noopExec, benchConfig)
	benchSink = sink
	rec := corelogger.RecordEvent{Message: "bench"}
	benchWriteLoop(b, sink, rec, []byte("bench payload"))
}

// BenchmarkWrite_WithAttrs measures Write for a record carrying Attrs. The
// record value (including its Attrs slice header) is appended to the batch by
// value; no deep Attrs copy is made (RecordEvent is immutable by contract), so
// this quantifies the buffering cost of a realistic attributed record.
func BenchmarkWrite_WithAttrs(b *testing.B) {
	sink := dbsink.Compose(noopExec, benchConfig)
	benchSink = sink
	rec := corelogger.RecordEvent{
		Message: "bench",
		Attrs: []corelogger.AttrValue{
			{Key: "k1"}, {Key: "k2"}, {Key: "k3"},
		},
	}
	benchWriteLoop(b, sink, rec, []byte("bench payload"))
}
