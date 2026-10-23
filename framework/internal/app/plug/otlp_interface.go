package plug

import (
	"context"
	"time"
)

// The OTLP export subsystem's export (issue #483), which
// framework/internal/app/otlpengine implements over the SDK's app/forward.

// OTLPExporter is an app's export, open: where the tracer hands its spans,
// what the framework records of each unit of work, the loop that sends, and
// what it counts.
type OTLPExporter interface {
	// SpanSink is the sink the app's tracer ends its spans into.
	SpanSink() OTLPSinkValue
	// Meter is the product's own meter, collected at each interval beside
	// the framework's, into the same metrics queue, under the same resource
	// and a scope named after the product.
	Meter() OTLPMeterValue
	// Record counts one unit of work the framework observed on node: its
	// operation, how long it took, and whether it failed.
	Record(node, op string, took time.Duration, failed bool)
	// Run sends the queued batches and collects the metrics until ctx ends;
	// then it flushes what it holds in memory into the queues, lets the
	// batch being sent finish within the drain, and closes the queues.
	Run(ctx context.Context) error
	// Stats is what each signal's queue holds, has sent and has lost.
	Stats() []OTLPStatsValue
}

// OTLPQueue is one signal's queue of the export as the product's CLI opens
// it (issue #536): its dead letters read, replayed into the live queue, or
// dropped.
type OTLPQueue interface {
	// DeadLetters are at most max of the batches the queue gave up on,
	// oldest first.
	DeadLetters(ctx context.Context, max int) ([]OTLPDeadLetterValue, error)
	// Replay moves the dead letter id back into the live queue, its
	// attempts counted afresh: the export sends it again. found is false
	// when the queue holds no dead letter id.
	Replay(ctx context.Context, id string) (found bool, err error)
	// Drop deletes the dead letter id for good. found is false when the
	// queue holds no dead letter id.
	Drop(ctx context.Context, id string) (found bool, err error)
	// Close releases what the queue holds.
	Close() error
}
