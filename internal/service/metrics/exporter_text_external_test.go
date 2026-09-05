// Package metrics_test — the text exporter as a consumer wires it up.
package metrics_test

import (
	"bytes"
	"errors"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcmetrics "github.com/kitsunium/sdk/internal/service/metrics"
)

// failingWriter reports a fixed error, standing in for a sink that has gone
// away — a closed pipe, a full disk.
type failingWriter struct{ err error }

// Write always fails.
func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

// TestNewTextExporter pins the rendering and the ordering.
//
// The sorted order is what makes the output diffable: a test fixture, a golden
// file or an operator's eye all depend on two runs of the same snapshot
// producing byte-identical text, and Go's map iteration order is deliberately
// randomised.
func TestNewTextExporter(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		snap coremetrics.SnapshotValue
		want string
	}
	tests := []tc{
		{name: "an empty snapshot", want: ""},
		{
			name: "one counter",
			snap: coremetrics.SnapshotValue{Counters: map[string]int64{"requests": 7}},
			want: "counter requests 7\n",
		},
		{
			//: declared out of order; the output must be sorted.
			name: "counters are sorted by name",
			snap: coremetrics.SnapshotValue{Counters: map[string]int64{"z": 1, "a": 2, "m": 3}},
			want: "counter a 2\ncounter m 3\ncounter z 1\n",
		},
		{
			name: "a gauge renders its float",
			snap: coremetrics.SnapshotValue{Gauges: map[string]float64{"in_flight": 2.5}},
			want: "gauge in_flight 2.5\n",
		},
		{
			name: "a histogram renders its observation count",
			snap: coremetrics.SnapshotValue{
				Histograms: map[string]coremetrics.HistogramValue{"latency": {Count: 42}},
			},
			want: "histogram_count latency 42\n",
		},
		{
			//: the three kinds appear in a fixed order: counters, gauges, then
			//: histograms — so the whole document is stable, not just each
			//: section.
			name: "every kind, in a fixed order",
			snap: coremetrics.SnapshotValue{
				Counters:   map[string]int64{"c": 1},
				Gauges:     map[string]float64{"g": 2},
				Histograms: map[string]coremetrics.HistogramValue{"h": {Count: 3}},
			},
			want: "counter c 1\ngauge g 2\nhistogram_count h 3\n",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var buf bytes.Buffer
		e := svcmetrics.NewTextExporter("test", &buf)

		if got := e.Name(); got != "test" {
			t.Errorf("Name() = %q, want test", got)
		}
		if err := e.Export(c.snap); err != nil {
			t.Fatalf("Export = %v, want nil", err)
		}
		if buf.String() != c.want {
			t.Errorf("Export wrote %q, want %q", buf.String(), c.want)
		}

		//: exporting the same snapshot twice produces the same bytes again,
		//: which is what "deterministic" has to mean for a diffable format.
		var second bytes.Buffer
		if err := svcmetrics.NewTextExporter("test", &second).Export(c.snap); err != nil {
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
		{"a populated snapshot", coremetrics.SnapshotValue{Counters: map[string]int64{"c": 1}}},
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
