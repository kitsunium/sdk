package metrics_test

import (
	"bytes"
	"errors"
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
		{"the prometheus exporter is registered", "prometheus", false},
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

// The Prometheus exporter reaches consumers through two doors, and both need
// pinning: the registry (which a bare import arms, on stderr) and the explicit
// constructor (which a /metrics handler binds to its response writer). Only the
// second is usable for a scrape, so a facade exporting the registry name but
// not the constructor would look complete and serve nothing.
func TestFacadePrometheusExporter(t *testing.T) {
	t.Parallel()
	meter := metrics.NewMeter()
	meter.Counter("requests_total", metrics.Label{Key: "method", Value: "GET"}).Inc()

	var buf bytes.Buffer
	if err := metrics.NewPrometheusExporter("scrape", &buf).Export(meter.Collect()); err != nil {
		t.Fatalf("Export = %v, want nil", err)
	}

	want := "# TYPE requests_total counter\nrequests_total{method=\"GET\"} 1\n"
	if buf.String() != want {
		t.Errorf("Export wrote %q, want %q", buf.String(), want)
	}
}

// A name the exposition format cannot spell is REFUSED, not rewritten, and the
// refusal is typed so a consumer acts on it rather than parsing a string. The
// sentinel has to be reachable from the facade for that to be possible at all.
func TestFacadePrometheusRefusesUnrepresentableNames(t *testing.T) {
	t.Parallel()
	meter := metrics.NewMeter()
	meter.Counter("http.requests").Inc()

	var buf bytes.Buffer
	err := metrics.NewPrometheusExporter("scrape", &buf).Export(meter.Collect())

	if !errors.Is(err, metrics.InvalidMetricName) {
		t.Fatalf("Export = %v, want InvalidMetricName", err)
	}
	//: nothing is written, because a truncated exposition parses as a
	//: complete one and its missing series look like series that stopped.
	if buf.Len() != 0 {
		t.Errorf("Export wrote %q on a refusal, want nothing", buf.String())
	}
}
