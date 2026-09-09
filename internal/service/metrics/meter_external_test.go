// Package metrics_test — the Meter as a consumer uses it.
package metrics_test

import (
	"math"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
	svcmetrics "github.com/kitsunium/sdk/internal/service/metrics"
)

// firstOr returns the first series of a group, or fallback when the group is
// absent. A snapshot now maps a name to its SERIES, and these cases record a
// single dimensionless one — so "the value under that name" is series zero.
func firstOr[V any](series []V, fallback V) V {
	if len(series) == 0 {
		return fallback
	}
	return series[0]
}

// TestNewMeter pins that a fresh meter is immediately usable and that Collect
// hands back empty maps rather than nils — a caller ranges over all three
// without a guard, so a nil would be a panic in the reporting path, which is
// the last place anyone wants one.
func TestNewMeter(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: what the caller records before collecting.
		record func(m coremetrics.Meter)
		//: the expected counts per kind.
		counters, gauges, histograms int
	}
	tests := []tc{
		{name: "nothing recorded", record: func(coremetrics.Meter) {}},
		{
			name:     "a counter",
			record:   func(m coremetrics.Meter) { m.Counter("requests").Inc() },
			counters: 1,
		},
		{
			name:   "a gauge",
			record: func(m coremetrics.Meter) { m.Gauge("in_flight").Set(3) },
			gauges: 1,
		},
		{
			name:       "a histogram",
			record:     func(m coremetrics.Meter) { m.Histogram("latency", []float64{1}).Record(0.5) },
			histograms: 1,
		},
		{
			name: "one of each",
			record: func(m coremetrics.Meter) {
				m.Counter("requests").Inc()
				m.Gauge("in_flight").Set(3)
				m.Histogram("latency", []float64{1}).Record(0.5)
			},
			counters: 1, gauges: 1, histograms: 1,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m := svcmetrics.NewMeter()
		if m == nil {
			t.Fatal("NewMeter returned no meter")
		}
		c.record(m)

		snap := m.Collect()
		//: empty, not nil.
		if snap.Counters == nil || snap.Gauges == nil || snap.Histograms == nil {
			t.Fatalf("Collect returned nil maps: %+v", snap)
		}
		if len(snap.Counters) != c.counters {
			t.Errorf("%d counters, want %d", len(snap.Counters), c.counters)
		}
		if len(snap.Gauges) != c.gauges {
			t.Errorf("%d gauges, want %d", len(snap.Gauges), c.gauges)
		}
		if len(snap.Histograms) != c.histograms {
			t.Errorf("%d histograms, want %d", len(snap.Histograms), c.histograms)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestInstruments pins the three instruments' arithmetic through the public
// surface, including the monotonicity rule that separates a counter from a
// gauge: a counter refuses to go down, and a gauge is expected to.
func TestInstruments(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: what the caller does, and what Collect must then report.
		record     func(m coremetrics.Meter)
		wantCount  int64
		wantGauge  float64
		wantBucket uint64
	}
	tests := []tc{
		{
			name:      "a counter accumulates",
			record:    func(m coremetrics.Meter) { m.Counter("c").Add(5); m.Counter("c").Inc() },
			wantCount: 6,
		},
		{
			//: monotonic: the decrement is ignored rather than applied.
			name:      "a counter refuses to decrease",
			record:    func(m coremetrics.Meter) { m.Counter("c").Add(5); m.Counter("c").Add(-2) },
			wantCount: 5,
		},
		{
			name:      "a gauge replaces",
			record:    func(m coremetrics.Meter) { m.Gauge("g").Set(3); m.Gauge("g").Set(7) },
			wantGauge: 7,
		},
		{
			//: a gauge is expected to go down; that is the difference.
			name:      "a gauge decreases",
			record:    func(m coremetrics.Meter) { m.Gauge("g").Set(3); m.Gauge("g").Add(-5) },
			wantGauge: -2,
		},
		{
			name: "a histogram buckets an observation",
			record: func(m coremetrics.Meter) {
				m.Histogram("h", []float64{1, 5}).Record(0.5)
			},
			wantBucket: 1,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m := svcmetrics.NewMeter()
		c.record(m)
		snap := m.Collect()

		if got := firstOr(snap.Counters["c"], coremetrics.CounterValue{}).Value; got != c.wantCount {
			t.Errorf("the counter reads %d, want %d", got, c.wantCount)
		}
		if got := firstOr(snap.Gauges["g"], coremetrics.GaugeValue{}).Value; math.Abs(got-c.wantGauge) > 1e-9 {
			t.Errorf("the gauge reads %v, want %v", got, c.wantGauge)
		}
		if c.wantBucket > 0 {
			hist := firstOr(snap.Histograms["h"], coremetrics.HistogramValue{})
			if len(hist.Counts) == 0 || hist.Counts[0] != c.wantBucket {
				t.Errorf("the histogram's first bucket holds %v, want %d", hist.Counts, c.wantBucket)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestKindConflictPanics pins the guard from the outside: reusing a metric name
// across instrument kinds panics rather than silently handing back the wrong
// instrument, because every value recorded through the wrong one is landing in
// a place nothing will ever look.
func TestKindConflictPanics(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		bind  func(m coremetrics.Meter)
		clash func(m coremetrics.Meter)
	}
	tests := []tc{
		{
			"a counter fetched as a gauge",
			func(m coremetrics.Meter) { m.Counter("x") },
			func(m coremetrics.Meter) { m.Gauge("x") },
		},
		{
			"a gauge fetched as a histogram",
			func(m coremetrics.Meter) { m.Gauge("x") },
			func(m coremetrics.Meter) { m.Histogram("x", nil) },
		},
		{
			"a histogram fetched as a counter",
			func(m coremetrics.Meter) { m.Histogram("x", nil) },
			func(m coremetrics.Meter) { m.Counter("x") },
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m := svcmetrics.NewMeter()
		c.bind(m)

		defer func() {
			if recover() == nil {
				t.Error("a cross-kind name reuse did not panic")
			}
		}()
		c.clash(m)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
