// Package metrics_test — the ADR 0039 guards around the Describer sibling, and
// the description ADR 0067 puts on the three metric envelopes.
package metrics_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/metrics"
)

// sevenMethodDouble is a hand-written implementation carrying EXACTLY the
// methods Meter, UpDownMeter and AsyncMeter declared before ADR 0067 — three
// synchronous instruments, Collect, the non-monotonic sum and the three
// observables. It has no Describe, on purpose.
type sevenMethodDouble struct{}

func (sevenMethodDouble) Counter(string, ...metrics.AttrValue) metrics.Counter { return nil }

func (sevenMethodDouble) Gauge(string, ...metrics.AttrValue) metrics.Gauge { return nil }

func (sevenMethodDouble) Histogram(string, []float64, ...metrics.AttrValue) metrics.Histogram {
	return nil
}

func (sevenMethodDouble) Collect() metrics.SnapshotValue { return metrics.SnapshotValue{} }

func (sevenMethodDouble) UpDownCounter(string, ...metrics.AttrValue) metrics.UpDownCounter {
	return nil
}

func (sevenMethodDouble) ObservableCounter(string, metrics.Int64Callback) {}

func (sevenMethodDouble) ObservableUpDownCounter(string, metrics.Int64Callback) {}

func (sevenMethodDouble) ObservableGauge(string, metrics.Float64Callback) {}

// describerDouble carries exactly the one method Describer declares.
type describerDouble struct{ calls int }

func (d *describerDouble) Describe(string, string) { d.calls++ }

// TestPreDescriberDoubleStillSatisfiesFullMeter is the ADR 0039 guard, and it
// is why Describer is a sibling instead of a fourth method somewhere.
//
// Meter is frozen and so is FullMeter — a union is still an interface, and Go
// interfaces are structural, so folding Describe into either would break every
// downstream double at COMPILE time with no deprecation window. This double is
// that downstream double: it predates ADR 0067 and knows nothing about
// descriptions. The day someone adds Describe to Meter, UpDownMeter, AsyncMeter
// or FullMeter, this file stops compiling — which is the whole point of writing
// the methods out by hand rather than embedding the interfaces.
func TestPreDescriberDoubleStillSatisfiesFullMeter(t *testing.T) {
	t.Parallel()
	meter := metrics.FullMeter(sevenMethodDouble{})
	//: it still answers, and it is still not a Describer.
	if snap := meter.Collect(); len(snap.Sums) != 0 {
		t.Fatalf("the double reported %d sums, want 0", len(snap.Sums))
	}
	//: the ABSENCE of the sibling is the answer a caller reads.
	if _, ok := meter.(metrics.Describer); ok {
		t.Error("a Meter with no Describe satisfies Describer; the sibling was folded into the union")
	}
}

// TestDescriberIsOneMethod pins the sibling's own width. A one-method port is
// what makes a downstream Describer cheap to write; growing it later is the
// same ADR 0039 break one layer down.
func TestDescriberIsOneMethod(t *testing.T) {
	t.Parallel()
	double := &describerDouble{}
	describer := metrics.Describer(double)
	describer.Describe("requests_total", "Requests served")
	if double.calls != 1 {
		t.Fatalf("Describe reached the double %d times, want 1", double.calls)
	}
}

// TestMetricEnvelopesCarryADescription pins the field ADR 0067 added to the
// three PUBLISHED metric shapes — the change the ADR 0040 v0 licence covers.
//
// Every literal here is written with FIELD NAMES, which is also the reason the
// addition broke nothing: an unkeyed composite literal of any of these three
// would have failed to compile the moment the field landed, and this repository
// has none.
func TestMetricEnvelopesCarryADescription(t *testing.T) {
	t.Parallel()
	const help string = "Requests served, by route and status"
	snap := metrics.SnapshotValue{
		Sums: map[string]metrics.SumMetricValue{
			"requests_total": {
				Temporality: metrics.TemporalityCumulative,
				Monotonic:   true,
				Description: help,
			},
		},
		Gauges: map[string]metrics.GaugeMetricValue{
			"queue_depth": {Description: help},
		},
		Histograms: map[string]metrics.HistogramMetricValue{
			"latency_seconds": {
				Temporality: metrics.TemporalityCumulative,
				Description: help,
			},
		},
	}
	//: one field, three envelopes, one spelling.
	if got := snap.Sums["requests_total"].Description; got != help {
		t.Errorf("SumMetricValue.Description = %q, want %q", got, help)
	}
	if got := snap.Gauges["queue_depth"].Description; got != help {
		t.Errorf("GaugeMetricValue.Description = %q, want %q", got, help)
	}
	if got := snap.Histograms["latency_seconds"].Description; got != help {
		t.Errorf("HistogramMetricValue.Description = %q, want %q", got, help)
	}
	//: an undescribed metric carries the zero value, not a placeholder.
	if got := (metrics.SumMetricValue{}).Description; got != "" {
		t.Errorf("an undescribed SumMetricValue reads %q, want the empty string", got)
	}
}

// TestDescriptionSentinelsAreDistinct pins that the two refusals ADR 0067
// declares are two facts with two fixes, not one sentinel wearing two hats.
func TestDescriptionSentinelsAreDistinct(t *testing.T) {
	t.Parallel()
	if metrics.InvalidDescription.Error() == metrics.DescriptionConflict.Error() {
		t.Fatal("the empty-description and conflicting-description sentinels render identically")
	}
	//: an empty description is a different repair from a conflicting one.
	if metrics.CodeInvalidDescription == metrics.CodeDescriptionConflict {
		t.Fatal("both refusals share one code")
	}
}
