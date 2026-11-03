package goruntime_test

import (
	"bytes"
	"runtime"
	"strings"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	"github.com/kitsunium/sdk/internal/service/observe/metrics"
	"github.com/kitsunium/sdk/internal/service/observe/metrics/goruntime"
)

// runtimeConventions is the OpenTelemetry Go runtime set: name, kind (sum
// monotonic or not), unit.
var runtimeConventions = []struct {
	name      string
	monotonic bool
	unit      string
}{
	{"go.memory.used", false, "By"},
	{"go.memory.allocated", true, "By"},
	{"go.memory.allocations", true, "{allocation}"},
	{"go.memory.gc.goal", false, "By"},
	{"go.goroutine.count", false, "{goroutine}"},
	{"go.processor.limit", false, "{thread}"},
	{"go.config.gogc", false, "%"},
}

// TestRuntimeInstrumentsFollowTheConventions (#868): every instrument is
// registered under its OpenTelemetry name, kind and unit, observed at Collect
// with a plausible value, and the OTLP/JSON document carries them.
func TestRuntimeInstrumentsFollowTheConventions(t *testing.T) {
	t.Parallel()
	m := metrics.NewMeter()
	goruntime.RuntimeInstruments(m, goruntime.RuntimeConfig{})
	snap := m.Collect()
	for _, c := range runtimeConventions {
		sum, ok := snap.Sums[c.name]
		if !ok || len(sum.Points) != 1 {
			t.Fatalf("%s: not in the snapshot (%v)", c.name, ok)
		}
		if sum.Monotonic != c.monotonic || sum.Unit != c.unit || sum.Description == "" {
			t.Errorf("%s: monotonic %v unit %q description %q", c.name, sum.Monotonic, sum.Unit, sum.Description)
		}
		if sum.Points[0].Value <= 0 {
			t.Errorf("%s = %d, want a positive reading", c.name, sum.Points[0].Value)
		}
	}
	var doc bytes.Buffer
	if err := metrics.NewOTLPJSONExporter("t", &doc).Export(snap); err != nil {
		t.Fatalf("OTLP: %v", err)
	}
	if !strings.Contains(doc.String(), `"name":"go.goroutine.count","description":"Count of live goroutines.","unit":"{goroutine}"`) {
		t.Fatalf("OTLP document lacks the goroutine count:\n%s", doc.String())
	}
	var text bytes.Buffer
	if err := metrics.NewTextExporter("t", &text).Export(snap); err != nil || !strings.Contains(text.String(), "go.memory.used") {
		t.Fatalf("text: %v\n%s", err, text.String())
	}
}

// TestRuntimeInstrumentsUnderscoreForPrometheus: the underscored names pass
// the Prometheus grammar the dotted ones fail.
func TestRuntimeInstrumentsUnderscoreForPrometheus(t *testing.T) {
	t.Parallel()
	m := metrics.NewMeter()
	goruntime.RuntimeInstruments(m, goruntime.RuntimeConfig{Underscore: true})
	var buf bytes.Buffer
	if err := metrics.NewPrometheusExporter("t", &buf).Export(m.Collect()); err != nil {
		t.Fatalf("Prometheus: %v", err)
	}
	if !strings.Contains(buf.String(), "go_goroutine_count") {
		t.Fatalf("exposition:\n%s", buf.String())
	}
	dotted := metrics.NewMeter()
	goruntime.RuntimeInstruments(dotted, goruntime.RuntimeConfig{})
	if err := metrics.NewPrometheusExporter("t", &bytes.Buffer{}).Export(dotted.Collect()); err == nil {
		t.Fatal("the dotted names passed the Prometheus grammar")
	}
	_ = coremetrics.CodeInvalidMetricName
}

// BenchmarkCollect_Runtime prices a collection of a meter holding only the
// runtime instruments: one runtime/metrics.Read for all of them.
func BenchmarkCollect_Runtime(b *testing.B) {
	m := metrics.NewMeter()
	goruntime.RuntimeInstruments(m, goruntime.RuntimeConfig{})
	m.Collect()
	b.ReportAllocs()
	for b.Loop() {
		m.Collect()
	}
}

// TestRuntimeHistogramsCarryTheRuntimeBuckets (#868): on a meter that takes
// an observable histogram, RuntimeInstruments adds go.schedule.duration and
// go.gc.duration in seconds, the runtime's buckets as the explicit bounds
// and no sum; OTLP/JSON writes no sum for them, Prometheus no _sum line,
// and the text exporter their counts.
func TestRuntimeHistogramsCarryTheRuntimeBuckets(t *testing.T) {
	t.Parallel()
	m := metrics.NewMeter()
	goruntime.RuntimeInstruments(m, goruntime.RuntimeConfig{})
	runtime.GC()
	snap := m.Collect()
	for _, name := range []string{"go.schedule.duration", "go.gc.duration"} {
		metric, ok := snap.Histograms[name]
		if !ok || len(metric.Points) != 1 {
			t.Fatalf("%s: not in the snapshot", name)
		}
		point := metric.Points[0]
		if metric.Unit != "s" || metric.Description == "" || !point.NoSum || len(point.Counts) != len(point.Bounds)+1 || len(point.Bounds) < 2 {
			t.Fatalf("%s: unit %q, NoSum %v, %d bounds, %d counts", name, metric.Unit, point.NoSum, len(point.Bounds), len(point.Counts))
		}
		for i := 1; i < len(point.Bounds); i++ {
			if point.Bounds[i] <= point.Bounds[i-1] {
				t.Fatalf("%s: bounds not increasing at %d", name, i)
			}
		}
	}
	if snap.Histograms["go.gc.duration"].Points[0].Count == 0 {
		t.Error("go.gc.duration counted no pause after runtime.GC")
	}
	raw, err := metrics.EncodeOTLPJSON(snap)
	if err != nil {
		t.Fatalf("EncodeOTLPJSON: %v", err)
	}
	doc := string(raw)
	at := strings.Index(doc, `"name":"go.schedule.duration"`)
	if at < 0 {
		t.Fatalf("OTLP document lacks go.schedule.duration")
	}
	entry := doc[at:]
	entry = entry[:strings.Index(entry, `"aggregationTemporality"`)]
	if strings.Contains(entry, `"sum"`) {
		t.Fatalf("OTLP wrote a sum the runtime does not keep: %s", entry)
	}
	underscored := metrics.NewMeter()
	goruntime.RuntimeInstruments(underscored, goruntime.RuntimeConfig{Underscore: true})
	var prom bytes.Buffer
	if err := metrics.NewPrometheusExporter("p", &prom).Export(underscored.Collect()); err != nil {
		t.Fatalf("Prometheus: %v", err)
	}
	if !strings.Contains(prom.String(), "go_schedule_duration_count ") || strings.Contains(prom.String(), "go_schedule_duration_sum") {
		t.Fatalf("Prometheus exposition of the runtime histogram:\n%s", prom.String())
	}
}
