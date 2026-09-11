// Package metrics_test — typed attributes, series identity and the cardinality
// bound as a consumer meets them.
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
		first, second []coremetrics.AttrValue
		wantSame      bool
	}
	tests := []tc{
		{name: "no labels twice", wantSame: true},
		{
			name:     "the same label twice",
			first:    []coremetrics.AttrValue{coremetrics.String("a", "1")},
			second:   []coremetrics.AttrValue{coremetrics.String("a", "1")},
			wantSame: true,
		},
		{
			//: a label set is a SET — declaration order is not identity.
			name:     "the same labels in a different order",
			first:    []coremetrics.AttrValue{coremetrics.String("a", "1"), coremetrics.String("b", "2")},
			second:   []coremetrics.AttrValue{coremetrics.String("b", "2"), coremetrics.String("a", "1")},
			wantSame: true,
		},
		{
			name:   "a different value",
			first:  []coremetrics.AttrValue{coremetrics.String("a", "1")},
			second: []coremetrics.AttrValue{coremetrics.String("a", "2")},
		},
		{
			name:   "a different key",
			first:  []coremetrics.AttrValue{coremetrics.String("a", "1")},
			second: []coremetrics.AttrValue{coremetrics.String("b", "1")},
		},
		{
			//: the dimensionless series is its own series, not a wildcard.
			name:   "labelled versus dimensionless",
			second: []coremetrics.AttrValue{coremetrics.String("a", "1")},
		},
		{
			name:   "an extra label",
			first:  []coremetrics.AttrValue{coremetrics.String("a", "1")},
			second: []coremetrics.AttrValue{coremetrics.String("a", "1"), coremetrics.String("b", "2")},
		},
		{
			//: the adversarial pair: one value carrying what a delimiter
			//: encoding would read as the boundary between two labels.
			name:   "a value that tries to forge another label",
			first:  []coremetrics.AttrValue{coremetrics.String("a", "x\x00b\x00y")},
			second: []coremetrics.AttrValue{coremetrics.String("a", "x"), coremetrics.String("b", "y")},
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
		series := m.Collect().Sums["requests"].Points
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

// TestInvalidAttributePanics pins the refusal of an attribute set that cannot
// name a series, from the outside.
func TestInvalidAttributePanics(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		fetch func(m coremetrics.Meter)
	}
	tests := []tc{
		{
			"a counter with an empty key",
			func(m coremetrics.Meter) { m.Counter("x", coremetrics.String("", "1")) },
		},
		{
			//: a struct literal sets no value at all — the kind is
			//: AttrKindInvalid, which is not "the empty string".
			"a counter with a value no constructor set",
			func(m coremetrics.Meter) { m.Counter("x", coremetrics.AttrValue{Key: "a"}) },
		},
		{
			"a gauge with a repeated key",
			func(m coremetrics.Meter) {
				m.Gauge("x",
					coremetrics.String("a", "1"),
					coremetrics.String("a", "2"))
			},
		},
		{
			"a histogram with an empty key",
			func(m coremetrics.Meter) {
				m.Histogram("x", nil, coremetrics.String("", "1"))
			},
		},
		{
			//: only the KIND differs, which is exactly the pair the identity
			//: encoding keeps apart — so it is still a repeated key.
			"a counter with one key under two kinds",
			func(m coremetrics.Meter) {
				m.Counter("x", coremetrics.String("a", "1"), coremetrics.Int64("a", 1))
			},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m := svcmetrics.NewMeter()
		defer func() {
			if recover() == nil {
				t.Error("an unusable attribute set did not panic")
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
			m.Counter("requests", coremetrics.String("id", strconv.Itoa(i))).Inc()
		}

		series := m.Collect().Sums["requests"].Points
		if len(series) != c.wantSeries {
			t.Fatalf("%d series survived, want %d", len(series), c.wantSeries)
		}
		//: nothing is dropped: every increment landed in some series.
		var total int64
		var sawOverflow bool
		for _, s := range series {
			total += s.Value
			for _, a := range s.Attrs {
				if a.Key == coremetrics.OverflowAttrKey {
					sawOverflow = true
					//: the marker is a BOOL, not the string "true" — typed
					//: attributes let it be the thing it always meant.
					if a.Kind() != coremetrics.AttrKindBool || !a.Bool() {
						t.Errorf("the overflow attribute is kind %d value %v, want a true bool",
							a.Kind(), a.Bool())
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
			m.Counter("requests", coremetrics.String("id", strconv.Itoa(i))).Inc()
		}

		series := m.Collect().Sums["requests"].Points
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
		m.Counter("exploding", coremetrics.String("id", strconv.Itoa(i))).Inc()
	}
	//: the other name still gets its own quota.
	for i := range 2 {
		m.Counter("healthy", coremetrics.String("id", strconv.Itoa(i))).Inc()
	}

	snap := m.Collect().Sums
	if got := len(snap["exploding"].Points); got != 3 {
		t.Errorf("the exploding name holds %d series, want 3 (2 admitted + overflow)", got)
	}
	if got := len(snap["healthy"].Points); got != 2 {
		t.Errorf("the healthy name holds %d series, want 2 — its quota was spent elsewhere", got)
	}
	for _, s := range snap["healthy"].Points {
		for _, l := range s.Attrs {
			if l.Key == coremetrics.OverflowAttrKey {
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
				coremetrics.String("status", status),
				coremetrics.String("method", method)).Inc()
		}
	}

	first := m.Collect().Sums["requests"].Points
	if len(first) != 8 {
		t.Fatalf("%d series, want 8", len(first))
	}
	//: sorted by label set: method ascending, then status.
	wantMethods := []string{"DELETE", "DELETE", "GET", "GET", "POST", "POST", "PUT", "PUT"}
	for i, s := range first {
		if len(s.Attrs) != 2 {
			t.Fatalf("series %d carries %d attributes, want 2", i, len(s.Attrs))
		}
		//: the set itself is ordered by key, so method precedes status.
		if s.Attrs[0].Key != "method" || s.Attrs[1].Key != "status" {
			t.Fatalf("series %d attributes are %v, want method then status", i, s.Attrs)
		}
		if s.Attrs[0].Str() != wantMethods[i] {
			t.Errorf("series %d is method %q, want %q", i, s.Attrs[0].Str(), wantMethods[i])
		}
	}

	//: and a second collection agrees, which map iteration alone would not.
	for i, s := range m.Collect().Sums["requests"].Points {
		if s.Attrs[0].Str() != first[i].Attrs[0].Str() ||
			s.Attrs[1].Str() != first[i].Attrs[1].Str() {
			t.Fatalf("a second Collect ordered series %d differently", i)
		}
	}
}
