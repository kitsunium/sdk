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
