package metrics_test

import (
	"bytes"
	"strings"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	"github.com/kitsunium/sdk/internal/service/observe/metrics"
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
	metrics.RuntimeInstruments(m, metrics.RuntimeConfig{})
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
	metrics.RuntimeInstruments(m, metrics.RuntimeConfig{Underscore: true})
	var buf bytes.Buffer
	if err := metrics.NewPrometheusExporter("t", &buf).Export(m.Collect()); err != nil {
		t.Fatalf("Prometheus: %v", err)
	}
	if !strings.Contains(buf.String(), "go_goroutine_count") {
		t.Fatalf("exposition:\n%s", buf.String())
	}
	dotted := metrics.NewMeter()
	metrics.RuntimeInstruments(dotted, metrics.RuntimeConfig{})
	if err := metrics.NewPrometheusExporter("t", &bytes.Buffer{}).Export(dotted.Collect()); err == nil {
		t.Fatal("the dotted names passed the Prometheus grammar")
	}
	_ = coremetrics.CodeInvalidMetricName
}

// BenchmarkCollect_Runtime prices a collection of a meter holding only the
// runtime instruments: one runtime/metrics.Read for all of them.
func BenchmarkCollect_Runtime(b *testing.B) {
	m := metrics.NewMeter()
	metrics.RuntimeInstruments(m, metrics.RuntimeConfig{})
	m.Collect()
	b.ReportAllocs()
	for b.Loop() {
		m.Collect()
	}
}
