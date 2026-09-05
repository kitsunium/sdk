package metrics_test

import (
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/metrics"
)

// The facade re-exports the meter and the exporter registry. What needs
// pinning is that a metric recorded through the public names survives Collect
// — an exporter that received an empty snapshot would report success while
// saying nothing.
func TestFacade(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		record  func(m metrics.Meter)
		wantAny bool
	}
	tests := []tc{
		{"a counter", func(m metrics.Meter) { m.Counter("ops").Inc() }, true},
		{"a gauge", func(m metrics.Meter) { m.Gauge("queue").Set(3) }, true},
		{"a histogram", func(m metrics.Meter) { m.Histogram("latency", []float64{1, 5}).Record(1.5) }, true},
		{"nothing recorded", func(metrics.Meter) {}, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m := metrics.NewMeter()
		c.record(m)
		snapshot := m.Collect()
		total := len(snapshot.Counters) + len(snapshot.Gauges) + len(snapshot.Histograms)
		if got := total > 0; got != c.wantAny {
			t.Errorf("collected %d instruments, want any = %v", total, c.wantAny)
		}
		//: the default text exporter must accept whatever Collect produced,
		//: including an empty snapshot — an exporter that refused one would
		//: make a quiet service look like a broken one.
		if err := metrics.Export("text", snapshot); err != nil {
			t.Errorf("Export: %v", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// The registry is what makes Export("text", …) resolve at all, so an empty one
// would fail every call with an unknown-exporter error rather than silently.
func TestFacadeExporterRegistry(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		export  metrics.ExporterName
		wantErr bool
	}
	tests := []tc{
		{"the default text exporter is registered", "text", false},
		{"an unregistered name is refused", "nonesuch", true},
		{"an empty name is refused", "", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if len(metrics.AvailableExporters()) == 0 {
			t.Fatal("no exporters registered; Export could never resolve")
		}
		err := metrics.Export(c.export, metrics.NewMeter().Collect())
		if (err != nil) != c.wantErr {
			t.Errorf("Export(%q) error = %v, want error = %v", c.export, err, c.wantErr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
