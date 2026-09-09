// Package metrics_test — the text exporter as a consumer wires it up.
package metrics_test

import (
	"bytes"
	"errors"
	"testing"
	"time"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcmetrics "github.com/kitsunium/sdk/internal/service/metrics"
)

// textHeader is the three payload-level lines every document opens with:
// Resource, Scope and the window. They are carried ONCE for the whole payload
// rather than repeated on every point, which is the reason the OTel model has
// them at all.
const textHeader = "# resource service.name=\"orders\"\n" +
	"# scope name=\"github.com/acme/orders\" version=\"1.4.0\"\n" +
	"# window start=\"2026-01-02T03:04:05Z\" end=\"2026-01-02T03:04:15Z\"\n"

// failingWriter reports a fixed error, standing in for a sink that has gone
// away — a closed pipe, a full disk.
type failingWriter struct{ err error }

// Write always fails.
func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

// The fixed collection window every golden document below is written against.
// A real snapshot carries the meter's clock; pinning it here is what lets the
// window line be asserted byte for byte.
var (
	textStart = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	textEnd   = time.Date(2026, 1, 2, 3, 4, 15, 0, time.UTC)
)

// identified stamps snap with the fixed Resource, Scope and window the golden
// documents expect.
func identified(snap coremetrics.SnapshotValue) coremetrics.SnapshotValue {
	snap.Resource = coremetrics.ResourceValue{Attrs: []coremetrics.AttrValue{
		coremetrics.String(coremetrics.ServiceNameKey, "orders"),
	}}
	snap.Scope = coremetrics.ScopeValue{Name: "github.com/acme/orders", Version: "1.4.0"}
	snap.StartTime = textStart
	snap.Time = textEnd
	return snap
}

// oneAttr is the single-string-attribute shorthand the tables below use.
func oneAttr(key, value string) []coremetrics.AttrValue {
	return []coremetrics.AttrValue{coremetrics.String(key, value)}
}

// TestNewTextExporter pins the rendering and the ordering.
//
// The sorted order is what makes the output diffable: a test fixture, a golden
// file or an operator's eye all depend on two runs of the same snapshot
// producing byte-identical text, and Go's map iteration order is deliberately
// randomised.
//
// Unlike the Prometheus connector, this exporter is LOSSLESS about the model —
// it is where a caller can actually see the Resource, the Scope, the window,
// each metric's temporality and monotonicity, and each attribute's TYPE.
func TestNewTextExporter(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		snap coremetrics.SnapshotValue
		want string
	}
	tests := []tc{
		{
			name: "an empty snapshot still identifies its producer",
			want: textHeader,
		},
		{
			name: "one counter",
			snap: coremetrics.SnapshotValue{Sums: map[string]coremetrics.SumMetricValue{
				"requests": promCounter(coremetrics.SumValue{Value: 7}),
			}},
			want: textHeader + "# metric requests sum cumulative monotonic\nrequests 7\n",
		},
		{
			//: the field that separates a Counter from an UpDownCounter, and
			//: the whole reason both are one point shape.
			name: "a non-monotonic sum says so",
			snap: coremetrics.SnapshotValue{Sums: map[string]coremetrics.SumMetricValue{
				"in_flight": promUpDown(coremetrics.SumValue{Value: 2}),
			}},
			want: textHeader + "# metric in_flight sum cumulative non_monotonic\nin_flight 2\n",
		},
		{
			//: temporality is the fact a metric's VALUE cannot carry, so the
			//: diagnostic prints it rather than leaving a reader to guess.
			name: "a delta sum says so",
			snap: coremetrics.SnapshotValue{Sums: map[string]coremetrics.SumMetricValue{
				"requests": {
					Temporality: coremetrics.TemporalityDelta,
					Monotonic:   true,
					Points:      []coremetrics.SumValue{{Value: 7}},
				},
			}},
			want: textHeader + "# metric requests sum delta monotonic\nrequests 7\n",
		},
		{
			//: declared out of order; the output must be sorted.
			name: "metrics are sorted by name",
			snap: coremetrics.SnapshotValue{Sums: map[string]coremetrics.SumMetricValue{
				"z": promCounter(coremetrics.SumValue{Value: 1}),
				"a": promCounter(coremetrics.SumValue{Value: 2}),
				"m": promCounter(coremetrics.SumValue{Value: 3}),
			}},
			want: textHeader +
				"# metric a sum cumulative monotonic\na 2\n" +
				"# metric m sum cumulative monotonic\nm 3\n" +
				"# metric z sum cumulative monotonic\nz 1\n",
		},
		{
			//: several series under ONE name, under ONE header — the shape
			//: every per-series wire format wants.
			name: "one name, several series, one header",
			snap: coremetrics.SnapshotValue{Sums: map[string]coremetrics.SumMetricValue{
				"requests": promCounter(
					coremetrics.SumValue{Attrs: oneAttr("method", "GET"), Value: 7},
					coremetrics.SumValue{Attrs: oneAttr("method", "POST"), Value: 2},
				),
			}},
			want: textHeader + "# metric requests sum cumulative monotonic\n" +
				"requests{method=\"GET\"} 7\nrequests{method=\"POST\"} 2\n",
		},
		{
			//: the dimensionless series renders as a bare name — no braces.
			name: "a dimensionless series next to an attributed one",
			snap: coremetrics.SnapshotValue{Sums: map[string]coremetrics.SumMetricValue{
				"requests": promCounter(
					coremetrics.SumValue{Value: 9},
					coremetrics.SumValue{Attrs: oneAttr("method", "GET"), Value: 7},
				),
			}},
			want: textHeader + "# metric requests sum cumulative monotonic\n" +
				"requests 9\nrequests{method=\"GET\"} 7\n",
		},
		{
			//: the typed attribute model, visible: a string is quoted, the
			//: other three kinds are bare. Every wire format flattens this
			//: away; a diagnostic does not have to.
			name: "each attribute kind keeps its type",
			snap: coremetrics.SnapshotValue{Sums: map[string]coremetrics.SumMetricValue{
				"requests": promCounter(coremetrics.SumValue{
					Attrs: []coremetrics.AttrValue{
						coremetrics.Bool("cached", true),
						coremetrics.String("method", "GET"),
						coremetrics.Float64("ratio", 0.5),
						coremetrics.Int64("status", 503),
					},
					Value: 1,
				}),
			}},
			want: textHeader + "# metric requests sum cumulative monotonic\n" +
				"requests{cached=true,method=\"GET\",ratio=0.5,status=503} 1\n",
		},
		{
			//: a gauge has no temporality and no monotonicity, so its header
			//: carries neither — that absence is the OTel model, not an
			//: omission.
			name: "a gauge header carries the kind alone",
			snap: coremetrics.SnapshotValue{Gauges: map[string]coremetrics.GaugeMetricValue{
				"in_flight": promGauge(coremetrics.GaugeValue{Value: 2.5}),
			}},
			want: textHeader + "# metric in_flight gauge\nin_flight 2.5\n",
		},
		{
			name: "a histogram renders its observation count",
			snap: coremetrics.SnapshotValue{
				Histograms: map[string]coremetrics.HistogramMetricValue{
					"latency": promHistogram(coremetrics.HistogramValue{Count: 42}),
				},
			},
			want: textHeader + "# metric latency histogram cumulative\nlatency 42\n",
		},
		{
			//: the three kinds appear in a fixed order: sums, gauges, then
			//: histograms — so the whole document is stable, not just each
			//: section.
			name: "every kind, in a fixed order",
			snap: coremetrics.SnapshotValue{
				Sums:       map[string]coremetrics.SumMetricValue{"c": promCounter(coremetrics.SumValue{Value: 1})},
				Gauges:     map[string]coremetrics.GaugeMetricValue{"g": promGauge(coremetrics.GaugeValue{Value: 2})},
				Histograms: map[string]coremetrics.HistogramMetricValue{"h": promHistogram(coremetrics.HistogramValue{Count: 3})},
			},
			want: textHeader +
				"# metric c sum cumulative monotonic\nc 1\n" +
				"# metric g gauge\ng 2\n" +
				"# metric h histogram cumulative\nh 3\n",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		snap := identified(c.snap)
		var buf bytes.Buffer
		e := svcmetrics.NewTextExporter("test", &buf)

		if got := e.Name(); got != "test" {
			t.Errorf("Name() = %q, want test", got)
		}
		if err := e.Export(snap); err != nil {
			t.Fatalf("Export = %v, want nil", err)
		}
		if buf.String() != c.want {
			t.Errorf("Export wrote\n%q\nwant\n%q", buf.String(), c.want)
		}

		//: exporting the same snapshot twice produces the same bytes again,
		//: which is what "deterministic" has to mean for a diffable format.
		var second bytes.Buffer
		if err := svcmetrics.NewTextExporter("test", &second).Export(snap); err != nil {
			t.Fatalf("the second Export = %v, want nil", err)
		}
		if second.String() != buf.String() {
			t.Errorf("two exports of one snapshot differ:\n%q\n%q", buf.String(), second.String())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestTextExporterOmitsAnAbsentScopeVersion pins the one optional field on the
// scope line. The specification makes Version optional, and an empty one says
// nothing — printing `version=""` would claim a fact the producer never stated.
func TestTextExporterOmitsAnAbsentScopeVersion(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	err := svcmetrics.NewTextExporter("test", &buf).Export(coremetrics.SnapshotValue{
		Resource: coremetrics.ResourceValue{Attrs: []coremetrics.AttrValue{
			coremetrics.String(coremetrics.ServiceNameKey, "orders"),
		}},
		Scope: coremetrics.ScopeValue{Name: "github.com/acme/orders"},
	})
	if err != nil {
		t.Fatalf("Export = %v, want nil", err)
	}
	want := "# resource service.name=\"orders\"\n" +
		"# scope name=\"github.com/acme/orders\"\n" +
		"# window start=\"0001-01-01T00:00:00Z\" end=\"0001-01-01T00:00:00Z\"\n"
	if buf.String() != want {
		t.Errorf("Export wrote\n%q\nwant\n%q", buf.String(), want)
	}
}

// TestTextExporterFromARealMeter walks the path a consumer walks and pins that
// the meter's own defaults reach the document — including the service.name the
// OpenTelemetry specification mandates when a producer supplies none.
func TestTextExporterFromARealMeter(t *testing.T) {
	t.Parallel()
	meter := svcmetrics.NewMeter()
	meter.Counter("requests", coremetrics.Int64("status", 503)).Inc()

	var buf bytes.Buffer
	if err := svcmetrics.NewTextExporter("test", &buf).Export(meter.Collect()); err != nil {
		t.Fatalf("Export = %v, want nil", err)
	}
	got := buf.String()

	//: the mandated default, not a value this SDK invented.
	wantResource := "# resource service.name=\"" + coremetrics.UnknownService + "\"\n"
	if len(got) < len(wantResource) || got[:len(wantResource)] != wantResource {
		t.Errorf("the document opens with %q, want %q", got, wantResource)
	}
	//: and the SDK names itself as the instrumenting library.
	wantScope := "# scope name=\"" + coremetrics.DefaultScopeName + "\"\n"
	if !contains(got, wantScope) {
		t.Errorf("the document does not carry %q:\n%q", wantScope, got)
	}
	//: an unconfigured meter accumulates, so it reports cumulative.
	if !contains(got, "# metric requests sum cumulative monotonic\nrequests{status=503} 1\n") {
		t.Errorf("the metric block is missing or mis-shaped:\n%q", got)
	}
}

// contains is strings.Contains, spelled out so this file keeps one import less
// than the assertions it makes.
func contains(haystack, needle string) bool {
	//: a plain substring search; the documents are small.
	return bytes.Contains([]byte(haystack), []byte(needle))
}

// TestTextExporterWriterFailure pins that a dead sink is reported typed. The
// exporter renders everything into one buffer and writes once, so the write is
// the only error surface — and swallowing it would leave a caller believing
// their metrics were shipped.
func TestTextExporterWriterFailure(t *testing.T) {
	t.Parallel()
	sinkErr := errors.New("the sink is gone")

	type tc struct {
		name string
		snap coremetrics.SnapshotValue
	}
	tests := []tc{
		{"an empty snapshot", coremetrics.SnapshotValue{}},
		{"a populated snapshot", coremetrics.SnapshotValue{
			Sums: map[string]coremetrics.SumMetricValue{"c": promCounter(coremetrics.SumValue{Value: 1})},
		}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		e := svcmetrics.NewTextExporter("test", failingWriter{err: sinkErr})

		err := e.Export(c.snap)

		if !kerrs.HasCode(err, coremetrics.CodeExportFailed) {
			t.Fatalf("Export = %v, want EXPORT_FAILED", err)
		}
		//: the sink's own error stays reachable, or an operator cannot tell a
		//: closed pipe from a full disk.
		if !errors.Is(err, sinkErr) {
			t.Errorf("Export = %v, want it to wrap %v", err, sinkErr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestExporterRegistry pins that importing this package registers its text
// exporter, which is the only way a consumer reaches it by name.
func TestExporterRegistry(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		key  coremetrics.ExporterName
	}
	tests := []tc{{"the text exporter", "text"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, ok := coremetrics.LookupExporter(c.key)
		if !ok {
			t.Fatalf("%q did not self-register on import", c.key)
		}
		if got.Name() != c.key {
			t.Errorf("the exporter under %q reports name %q", c.key, got.Name())
		}
		//: and it must be listed, since that is what an operator reads.
		var listed bool
		for _, name := range coremetrics.AvailableExporters() {
			if name == c.key {
				listed = true
			}
		}
		if !listed {
			t.Errorf("%q is registered but not listed", c.key)
		}
		//: exporting through the registry reaches the same implementation.
		if err := coremetrics.Export(c.key, coremetrics.SnapshotValue{}); err != nil {
			t.Errorf("Export through the registry = %v, want nil", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
