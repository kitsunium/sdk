// Package metrics_test — labels, series identity and the cardinality bound as
// a consumer meets them.
package metrics_test

import (
	"strconv"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
	svcmetrics "github.com/kitsunium/sdk/internal/service/metrics"
)

// TestSeriesIdentity pins what makes two fetches the same instrument.
//
// Getting this wrong is not a cosmetic bug: a name+labels pair that resolves to
// two instruments splits one metric's total across two series, and each one
// reports a fraction of the truth to a dashboard that has no way to notice.
func TestSeriesIdentity(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: the two label sets being compared.
		first, second []coremetrics.LabelValue
		wantSame      bool
	}
	tests := []tc{
		{name: "no labels twice", wantSame: true},
		{
			name:     "the same label twice",
			first:    []coremetrics.LabelValue{{Key: "a", Value: "1"}},
			second:   []coremetrics.LabelValue{{Key: "a", Value: "1"}},
			wantSame: true,
		},
		{
			//: a label set is a SET — declaration order is not identity.
			name:     "the same labels in a different order",
			first:    []coremetrics.LabelValue{{Key: "a", Value: "1"}, {Key: "b", Value: "2"}},
			second:   []coremetrics.LabelValue{{Key: "b", Value: "2"}, {Key: "a", Value: "1"}},
			wantSame: true,
		},
		{
			name:   "a different value",
			first:  []coremetrics.LabelValue{{Key: "a", Value: "1"}},
			second: []coremetrics.LabelValue{{Key: "a", Value: "2"}},
		},
		{
			name:   "a different key",
			first:  []coremetrics.LabelValue{{Key: "a", Value: "1"}},
			second: []coremetrics.LabelValue{{Key: "b", Value: "1"}},
		},
		{
			//: the dimensionless series is its own series, not a wildcard.
			name:   "labelled versus dimensionless",
			second: []coremetrics.LabelValue{{Key: "a", Value: "1"}},
		},
		{
			name:   "an extra label",
			first:  []coremetrics.LabelValue{{Key: "a", Value: "1"}},
			second: []coremetrics.LabelValue{{Key: "a", Value: "1"}, {Key: "b", Value: "2"}},
		},
		{
			//: the adversarial pair: one value carrying what a delimiter
			//: encoding would read as the boundary between two labels.
			name:   "a value that tries to forge another label",
			first:  []coremetrics.LabelValue{{Key: "a", Value: "x\x00b\x00y"}},
			second: []coremetrics.LabelValue{{Key: "a", Value: "x"}, {Key: "b", Value: "y"}},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m := svcmetrics.NewMeter()
		first := m.Counter("requests", c.first...)
		second := m.Counter("requests", c.second...)

		if (first == second) != c.wantSame {
			t.Fatalf("the two fetches resolved to the same instrument = %v, want %v",
				first == second, c.wantSame)
		}
		//: and the snapshot agrees about how many series exist.
		first.Add(3)
		second.Add(4)
		series := m.Collect().Counters["requests"]
		wantSeries, wantFirst := 2, int64(3)
		if c.wantSame {
			wantSeries, wantFirst = 1, 7
		}
		if len(series) != wantSeries {
			t.Fatalf("%d series under the name, want %d", len(series), wantSeries)
		}
		if total := series[0].Value; c.wantSame && total != wantFirst {
			t.Errorf("the shared series totals %d, want %d", total, wantFirst)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestInvalidLabelPanics pins the refusal of a label set that cannot name a
// series, from the outside.
func TestInvalidLabelPanics(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		fetch func(m coremetrics.Meter)
	}
	tests := []tc{
		{
			"a counter with an empty key",
			func(m coremetrics.Meter) { m.Counter("x", coremetrics.LabelValue{Value: "1"}) },
		},
		{
			"a gauge with a repeated key",
			func(m coremetrics.Meter) {
				m.Gauge("x",
					coremetrics.LabelValue{Key: "a", Value: "1"},
					coremetrics.LabelValue{Key: "a", Value: "2"})
			},
		},
		{
			"a histogram with an empty key",
			func(m coremetrics.Meter) {
				m.Histogram("x", nil, coremetrics.LabelValue{Value: "1"})
			},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m := svcmetrics.NewMeter()
		defer func() {
			if recover() == nil {
				t.Error("an unusable label set did not panic")
			}
		}()
		c.fetch(m)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestCardinalityBound pins what happens when an instrument's labels blow up.
//
// This is the failure this whole feature exists to contain: an unbounded label
// value — a request id, a raw path, a user agent — turns a metric into a leak
// that grows for as long as the process lives. The meter admits the first
// MaxSeriesPerInstrument label sets and folds every later one into ONE
// aggregated series, so memory stops growing while the total stays correct.
func TestCardinalityBound(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: the configured bound, verbatim — including the non-positive values
		//: that must clamp rather than mean "unbounded".
		configured int
		//: how many distinct label sets the caller pushes through.
		pushed int
		//: series expected in the snapshot, overflow included.
		wantSeries int
		//: whether an overflow series must be present.
		wantOverflow bool
	}
	tests := []tc{
		{name: "under the bound", configured: 10, pushed: 4, wantSeries: 4},
		{name: "exactly at the bound", configured: 4, pushed: 4, wantSeries: 4},
		{
			//: one past: three admitted plus the aggregated series.
			name: "one past the bound", configured: 3, pushed: 4,
			wantSeries: 4, wantOverflow: true,
		},
		{
			//: far past: the series count stops growing, which is the point.
			name: "far past the bound", configured: 3, pushed: 500,
			wantSeries: 4, wantOverflow: true,
		},
		{
			//: a bound of one admits a single label set — legal, and the
			//: caller's explicit intent, so it is not clamped up.
			name: "a bound of one", configured: 1, pushed: 5,
			wantSeries: 2, wantOverflow: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m := svcmetrics.NewMeterWithConfig(svcmetrics.MeterConfig{
			MaxSeriesPerInstrument: c.configured,
		})
		for i := range c.pushed {
			m.Counter("requests", coremetrics.LabelValue{
				Key: "id", Value: strconv.Itoa(i),
			}).Inc()
		}

		series := m.Collect().Counters["requests"]
		if len(series) != c.wantSeries {
			t.Fatalf("%d series survived, want %d", len(series), c.wantSeries)
		}
		//: nothing is dropped: every increment landed in some series.
		var total int64
		var sawOverflow bool
		for _, s := range series {
			total += s.Value
			for _, l := range s.Labels {
				if l.Key == coremetrics.OverflowLabelKey {
					sawOverflow = true
					if l.Value != coremetrics.OverflowLabelValue {
						t.Errorf("the overflow label reads %q, want %q",
							l.Value, coremetrics.OverflowLabelValue)
					}
				}
			}
		}
		if total != int64(c.pushed) {
			t.Errorf("the series total %d, want %d — an observation was dropped",
				total, c.pushed)
		}
		if sawOverflow != c.wantOverflow {
			t.Errorf("an overflow series is present = %v, want %v", sawOverflow, c.wantOverflow)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestZeroBoundIsNotUnbounded pins ADR 0031 on this knob: a non-positive
// MaxSeriesPerInstrument clamps to the working default, and there is no
// setting at all that means "unbounded".
//
// The assertion is on the OBSERVABLE outcome — a meter built with each of
// these still folds past its bound — not on the clamped field, so it survives
// a change of mechanism.
func TestZeroBoundIsNotUnbounded(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		configured int
	}
	tests := []tc{
		{"the zero value", 0},
		{"a negative bound", -1},
		{"a very negative bound", -1 << 20},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m := svcmetrics.NewMeterWithConfig(svcmetrics.MeterConfig{
			MaxSeriesPerInstrument: c.configured,
		})
		//: push one label set past the default bound.
		for i := range svcmetrics.DefaultMaxSeriesPerInstrument + 1 {
			m.Counter("requests", coremetrics.LabelValue{
				Key: "id", Value: strconv.Itoa(i),
			}).Inc()
		}

		series := m.Collect().Counters["requests"]
		//: the default admitted its quota, then the one extra folded — so the
		//: count is the bound plus the single aggregated series, never the
		//: bound plus one more real series.
		want := svcmetrics.DefaultMaxSeriesPerInstrument + 1
		if len(series) != want {
			t.Fatalf("%d series survived a %d bound, want %d — a non-positive bound was read as unbounded",
				len(series), c.configured, want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestBoundIsPerInstrumentName pins that one exploding metric cannot starve
// another. The bound is per NAME, so a runaway label on one instrument leaves
// every other instrument with its full quota.
func TestBoundIsPerInstrumentName(t *testing.T) {
	t.Parallel()
	m := svcmetrics.NewMeterWithConfig(svcmetrics.MeterConfig{MaxSeriesPerInstrument: 2})
	//: blow up one name.
	for i := range 50 {
		m.Counter("exploding", coremetrics.LabelValue{Key: "id", Value: strconv.Itoa(i)}).Inc()
	}
	//: the other name still gets its own quota.
	for i := range 2 {
		m.Counter("healthy", coremetrics.LabelValue{Key: "id", Value: strconv.Itoa(i)}).Inc()
	}

	snap := m.Collect().Counters
	if got := len(snap["exploding"]); got != 3 {
		t.Errorf("the exploding name holds %d series, want 3 (2 admitted + overflow)", got)
	}
	if got := len(snap["healthy"]); got != 2 {
		t.Errorf("the healthy name holds %d series, want 2 — its quota was spent elsewhere", got)
	}
	for _, s := range snap["healthy"] {
		for _, l := range s.Labels {
			if l.Key == coremetrics.OverflowLabelKey {
				t.Error("the healthy name overflowed because another name did")
			}
		}
	}
}

// TestSnapshotSeriesAreDeterministic pins the ordering an exporter depends on.
//
// Go randomises map iteration deliberately, so without a sort every collection
// of the same meter emits its series in a different order — and a rendered
// snapshot stops being diffable, which is the property that makes exporter
// output testable at all.
func TestSnapshotSeriesAreDeterministic(t *testing.T) {
	t.Parallel()
	m := svcmetrics.NewMeter()
	for _, method := range []string{"PUT", "GET", "POST", "DELETE"} {
		for _, status := range []string{"500", "200"} {
			m.Counter("requests",
				coremetrics.LabelValue{Key: "status", Value: status},
				coremetrics.LabelValue{Key: "method", Value: method}).Inc()
		}
	}

	first := m.Collect().Counters["requests"]
	if len(first) != 8 {
		t.Fatalf("%d series, want 8", len(first))
	}
	//: sorted by label set: method ascending, then status.
	wantMethods := []string{"DELETE", "DELETE", "GET", "GET", "POST", "POST", "PUT", "PUT"}
	for i, s := range first {
		if len(s.Labels) != 2 {
			t.Fatalf("series %d carries %d labels, want 2", i, len(s.Labels))
		}
		//: the label set itself is ordered by key, so method precedes status.
		if s.Labels[0].Key != "method" || s.Labels[1].Key != "status" {
			t.Fatalf("series %d labels are %v, want method then status", i, s.Labels)
		}
		if s.Labels[0].Value != wantMethods[i] {
			t.Errorf("series %d is method %q, want %q", i, s.Labels[0].Value, wantMethods[i])
		}
	}

	//: and a second collection agrees, which map iteration alone would not.
	for i, s := range m.Collect().Counters["requests"] {
		if s.Labels[0].Value != first[i].Labels[0].Value ||
			s.Labels[1].Value != first[i].Labels[1].Value {
			t.Fatalf("a second Collect ordered series %d differently", i)
		}
	}
}
