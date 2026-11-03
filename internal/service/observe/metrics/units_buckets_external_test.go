package metrics_test

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	"github.com/kitsunium/sdk/internal/service/observe/metrics"
)

// render runs one exporter over snap and returns what it wrote.
func render(t *testing.T, build func(coremetrics.ExporterName, *bytes.Buffer) coremetrics.Exporter, snap coremetrics.SnapshotValue) string {
	t.Helper()
	var buf bytes.Buffer
	if err := build("t", &buf).Export(snap); err != nil {
		t.Fatalf("Export: %v", err)
	}
	return buf.String()
}

func prom(n coremetrics.ExporterName, b *bytes.Buffer) coremetrics.Exporter {
	return metrics.NewPrometheusExporter(n, b)
}

func text(n coremetrics.ExporterName, b *bytes.Buffer) coremetrics.Exporter {
	return metrics.NewTextExporter(n, b)
}

func otlpJSON(n coremetrics.ExporterName, b *bytes.Buffer) coremetrics.Exporter {
	return metrics.NewOTLPJSONExporter(n, b)
}

// TestAHistogramWithNoBucketsGetsTheDefaultLadder (#1135): Histogram(name,
// nil) records into DefaultBuckets, and each exporter renders the full ladder.
func TestAHistogramWithNoBucketsGetsTheDefaultLadder(t *testing.T) {
	t.Parallel()
	m := metrics.NewMeter()
	m.Histogram("h", nil).Record(0.3)
	snap := m.Collect()
	point := snap.Histograms["h"].Points[0]
	if !slices.Equal(point.Bounds, coremetrics.DefaultBuckets()) {
		t.Fatalf("bounds = %v, want DefaultBuckets", point.Bounds)
	}
	if got := render(t, prom, snap); !strings.Contains(got, `h_bucket{le="10000"} 1`) || strings.Count(got, "h_bucket{") != 16 {
		t.Fatalf("prometheus ladder:\n%s", got)
	}
	if got := render(t, otlpJSON, snap); !strings.Contains(got, `"explicitBounds":[0,5,10,25,50,75,100,250,500,750,1000,2500,5000,7500,10000]`) {
		t.Fatalf("otlp ladder:\n%s", got)
	}
	//: the text exporter renders a histogram's count, never its buckets, for
	//: any layout: the ladder is the wire formats' to carry.
	if got := render(t, text, snap); !strings.Contains(got, "# metric h histogram cumulative\nh 1\n") {
		t.Fatalf("text rendering:\n%s", got)
	}
	if b := coremetrics.DefaultBuckets(); &b[0] == &coremetrics.DefaultBuckets()[0] {
		t.Fatal("DefaultBuckets hands out one shared slice")
	}
}

// panicCode runs f and returns the panic's message.
func panicCode(f func()) (msg string) {
	defer func() {
		rec := recover()
		msg, _ = rec.(string)
	}()
	f()
	return ""
}

// TestUnitReachesEveryWireThatCarriesOne (#1021): Unit("latency", "s") lands
// in the snapshot, the OTLP document ("unit":"s", after the description) and
// the text header (unit="s" after description=…); the Prometheus exposition
// is byte-identical with and without the unit — the named loss.
func TestUnitReachesEveryWireThatCarriesOne(t *testing.T) {
	t.Parallel()
	with, without := metrics.NewMeter(), metrics.NewMeter()
	for _, m := range []coremetrics.FullMeter{with, without} {
		m.(coremetrics.Describer).Describe("latency", "request latency")
		m.Histogram("latency", []float64{0.1, 1}).Record(0.2)
	}
	ud, ok := with.(coremetrics.UnitDescriber)
	if !ok {
		t.Fatal("the meter is not a UnitDescriber")
	}
	ud.Unit("latency", "s")
	ud.Unit("latency", "s") // idempotent
	snapWith, snapWithout := with.Collect(), without.Collect()
	snapWithout.Time, snapWithout.StartTime = snapWith.Time, snapWith.StartTime
	if got := snapWith.Histograms["latency"].Unit; got != "s" {
		t.Fatalf("snapshot unit %q", got)
	}
	if got := render(t, otlpJSON, snapWith); !strings.Contains(got, `"name":"latency","description":"request latency","unit":"s","histogram"`) {
		t.Fatalf("otlp document:\n%s", got)
	}
	if got := render(t, otlpJSON, snapWithout); strings.Contains(got, `"unit"`) {
		t.Fatalf("an undeclared unit reached the document:\n%s", got)
	}
	if got := render(t, text, snapWith); !strings.Contains(got, `# metric latency histogram cumulative description="request latency" unit="s"`) {
		t.Fatalf("text header:\n%s", got)
	}
	if a, b := render(t, prom, snapWith), render(t, prom, snapWithout); a != b {
		t.Fatalf("the Prometheus exposition changed with a unit:\n%s\nvs\n%s", a, b)
	}
}

// TestUnitRefusesAConflictAndABadUnit (#1021): a different unit for a bound
// name panics UNIT_CONFLICT; an empty, over-long or non-printable one panics
// INVALID_UNIT.
func TestUnitRefusesAConflictAndABadUnit(t *testing.T) {
	t.Parallel()
	ud := metrics.NewMeter().(coremetrics.UnitDescriber)
	ud.Unit("x", "ms")
	if msg := panicCode(func() { ud.Unit("x", "s") }); !strings.Contains(msg, "UNIT_CONFLICT") {
		t.Fatalf("conflicting unit: %q", msg)
	}
	for _, bad := range []string{"", "a b", "s\x00", "é", strings.Repeat("s", 64)} {
		if msg := panicCode(func() { ud.Unit("y", bad) }); !strings.Contains(msg, "INVALID_UNIT") {
			t.Fatalf("Unit(%q): %q, want INVALID_UNIT", bad, msg)
		}
	}
	ud.Unit("z", "{request}")
}
