package metrics_test

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/metrics"
)

// TestFacadeLabels pins the labelled surface through the public names only —
// the alias set is what a consumer actually compiles against, and a missing
// re-export is invisible to every test written inside the SDK.
func TestFacadeLabels(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: the two label sets recorded, then compared.
		first, second []metrics.Label
		wantSeries    int
	}
	tests := []tc{
		{name: "no labels", wantSeries: 1},
		{
			name:       "the same set twice",
			first:      []metrics.Label{{Key: "method", Value: "GET"}},
			second:     []metrics.Label{{Key: "method", Value: "GET"}},
			wantSeries: 1,
		},
		{
			//: order is not identity — a label set is a set.
			name:       "the same set, reordered",
			first:      []metrics.Label{{Key: "a", Value: "1"}, {Key: "b", Value: "2"}},
			second:     []metrics.Label{{Key: "b", Value: "2"}, {Key: "a", Value: "1"}},
			wantSeries: 1,
		},
		{
			name:       "two values of one dimension",
			first:      []metrics.Label{{Key: "method", Value: "GET"}},
			second:     []metrics.Label{{Key: "method", Value: "POST"}},
			wantSeries: 2,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m := metrics.NewMeter()
		m.Counter("requests", c.first...).Inc()
		m.Counter("requests", c.second...).Inc()

		series := m.Collect().Counters["requests"]
		if len(series) != c.wantSeries {
			t.Fatalf("%d series, want %d", len(series), c.wantSeries)
		}
		//: nothing is lost whichever way the sets resolved.
		var total int64
		for _, s := range series {
			total += s.Value
		}
		if total != 2 {
			t.Errorf("the series total %d, want 2", total)
		}
		//: the snapshot must render through the registered text exporter, or
		//: the labelled shape is unusable by the thing it exists to feed.
		var buf bytes.Buffer
		if err := metrics.NewTextExporter("test", &buf).Export(m.Collect()); err != nil {
			t.Fatalf("Export: %v", err)
		}
		if !strings.HasPrefix(buf.String(), "counter requests") {
			t.Errorf("the exporter rendered %q", buf.String())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestFacadeCardinality pins the bound and its zero value through the public
// surface: a caller who never thinks about cardinality is still bounded, and
// one who writes 0 is bounded too (ADR 0031).
func TestFacadeCardinality(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		bound      int
		pushed     int
		wantSeries int
	}
	tests := []tc{
		{name: "a small explicit bound", bound: 3, pushed: 100, wantSeries: 4},
		{name: "under an explicit bound", bound: 100, pushed: 3, wantSeries: 3},
		{
			//: zero is the unset knob, not a request for infinity.
			name: "the zero value clamps to the default", bound: 0, pushed: 10,
			wantSeries: 10,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m := metrics.NewMeterWithConfig(metrics.MeterConfig{MaxSeriesPerInstrument: c.bound})
		for i := range c.pushed {
			m.Counter("requests", metrics.Label{Key: "id", Value: strconv.Itoa(i)}).Inc()
		}

		series := m.Collect().Counters["requests"]
		if len(series) != c.wantSeries {
			t.Fatalf("%d series, want %d", len(series), c.wantSeries)
		}
		//: every increment landed somewhere, folded or not.
		var total int64
		for _, s := range series {
			total += s.Value
		}
		if total != int64(c.pushed) {
			t.Errorf("the series total %d, want %d", total, c.pushed)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestFacadeOverflowLabelIsReExported pins that a consumer can RECOGNISE the
// overflow condition without importing anything internal. Detecting it is the
// whole reason the fold is visible rather than silent, so the constants have to
// be reachable from the same package the meter came from.
func TestFacadeOverflowLabelIsReExported(t *testing.T) {
	t.Parallel()
	m := metrics.NewMeterWithConfig(metrics.MeterConfig{MaxSeriesPerInstrument: 1})
	for i := range 10 {
		m.Counter("requests", metrics.Label{Key: "id", Value: strconv.Itoa(i)}).Inc()
	}

	var overflow *metrics.CounterValue
	for _, s := range m.Collect().Counters["requests"] {
		for _, l := range s.Labels {
			if l.Key == metrics.OverflowLabelKey && l.Value == metrics.OverflowLabelValue {
				overflow = &s
			}
		}
	}
	if overflow == nil {
		t.Fatal("no overflow series is visible after exceeding the bound")
	}
	//: the aggregated series carries every folded observation, so the metric's
	//: grand total is still right even though its breakdown is gone.
	if overflow.Value != 9 {
		t.Errorf("the overflow series totals %d, want 9", overflow.Value)
	}
	if metrics.DefaultMaxSeriesPerInstrument <= 0 {
		t.Errorf("DefaultMaxSeriesPerInstrument = %d, want a positive bound",
			metrics.DefaultMaxSeriesPerInstrument)
	}
}
