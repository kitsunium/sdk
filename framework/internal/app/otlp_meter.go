package app

import (
	"context"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/observe/metrics"
)

// The product's own metrics on the OTLP export (issue #538): Meter hands
// the meter the running export collects beside the framework's, and a meter
// that keeps nothing everywhere else, so the code that counts runs the same
// with and without an export. The meter itself is the export subsystem's
// (otlpengine): a product that does not import framework/app/otlp links
// none of the SDK's meter, only discardMeter.

// Meter is the product's own meter, for the app ctx runs inside: the
// counters, gauges and histograms it makes land in the OTLP export's
// metrics stream, under the app's resource and an instrumentation scope
// named after the app, collected every otlp-interval beside the framework's
// own. Without a running export — none declared, the export off, a CLI
// command, or outside an app — it is a meter that keeps nothing. An
// instrument is found again by its name and attributes, so a handler may
// ask for it at each call; its Collect is the export's.
func Meter(ctx context.Context) metrics.FullMeter {
	if a := appOf(ctx); a != nil {
		if r := a.otlpRun.Load(); r != nil && r.meter != nil {
			return r.meter
		}
	}
	return discardMeter{}
}

// discardMeter is the meter of a product no export collects: every
// instrument it makes keeps nothing, and a collection is empty.
type discardMeter struct{}

// discardCount is the counter and the up-down counter a discardMeter makes.
type discardCount struct{}

// discardGauge is the gauge a discardMeter makes.
type discardGauge struct{}

// discardHistogram is the histogram a discardMeter makes.
type discardHistogram struct{}

// Counter is an instrument that keeps nothing.
func (discardMeter) Counter(_ string, _ ...metrics.Attr) metrics.Counter { return discardCount{} }

// Gauge is an instrument that keeps nothing.
func (discardMeter) Gauge(_ string, _ ...metrics.Attr) metrics.Gauge { return discardGauge{} }

// Histogram is an instrument that keeps nothing.
func (discardMeter) Histogram(_ string, _ []float64, _ ...metrics.Attr) metrics.Histogram {
	return discardHistogram{}
}

// UpDownCounter is an instrument that keeps nothing.
func (discardMeter) UpDownCounter(_ string, _ ...metrics.Attr) metrics.UpDownCounter {
	return discardCount{}
}

// ObservableCounter registers nothing: no collection calls it.
func (discardMeter) ObservableCounter(_ string, _ metrics.Int64Callback) {}

// ObservableUpDownCounter registers nothing: no collection calls it.
func (discardMeter) ObservableUpDownCounter(_ string, _ metrics.Int64Callback) {}

// ObservableGauge registers nothing: no collection calls it.
func (discardMeter) ObservableGauge(_ string, _ metrics.Float64Callback) {}

// Collect is an empty snapshot.
func (discardMeter) Collect() metrics.Snapshot { return metrics.Snapshot{} }

// Add keeps nothing.
func (discardCount) Add(_ int64) {}

// Inc keeps nothing.
func (discardCount) Inc() {}

// Dec keeps nothing.
func (discardCount) Dec() {}

// Set keeps nothing.
func (discardGauge) Set(_ float64) {}

// Add keeps nothing.
func (discardGauge) Add(_ float64) {}

// Record keeps nothing.
func (discardHistogram) Record(_ float64) {}

// RecordDuration keeps nothing.
func (discardHistogram) RecordDuration(_ time.Duration) {}
