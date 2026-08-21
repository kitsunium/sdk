package metrics_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
	svcmetrics "github.com/kitsunium/sdk/internal/service/metrics"
)

// TestInstruments records into all three kinds and verifies the Collect snapshot.
func TestInstruments(t *testing.T) {
	t.Parallel()
	m := svcmetrics.NewMeter()
	m.Counter("hits").Add(3)
	m.Counter("hits").Inc() // idempotent fetch + Inc → 4
	m.Gauge("temp").Set(20)
	m.Gauge("temp").Add(1.5) // 21.5
	h := m.Histogram("lat", []float64{1, 10, 100})
	h.Record(5)
	h.RecordDuration(50 * time.Millisecond) // 0.05s → first bucket
	snap := m.Collect()
	//: counter accumulated Add(3)+Inc.
	if snap.Counters["hits"] != 4 {
		t.Errorf("counter hits=%d, want 4", snap.Counters["hits"])
	}
	//: gauge reflects Set then Add.
	if snap.Gauges["temp"] != 21.5 {
		t.Errorf("gauge temp=%v, want 21.5", snap.Gauges["temp"])
	}
	//: histogram saw two observations.
	if hv := snap.Histograms["lat"]; hv.Count != 2 {
		t.Errorf("histogram lat count=%d, want 2", hv.Count)
	}
}

// TestKindConflictPanics confirms reusing a name across kinds panics.
func TestKindConflictPanics(t *testing.T) {
	t.Parallel()
	m := svcmetrics.NewMeter()
	m.Counter("x")
	defer func() {
		//: fetching a counter name as a gauge is a programmer error → panic.
		if recover() == nil {
			t.Error("kind conflict did not panic")
		}
	}()
	//: "x" is a counter; fetching it as a gauge must panic.
	m.Gauge("x")
}

// TestTextExporter renders a snapshot to a buffer via a custom exporter.
func TestTextExporter(t *testing.T) {
	t.Parallel()
	m := svcmetrics.NewMeter()
	m.Counter("requests").Add(7)
	var buf bytes.Buffer
	exp := svcmetrics.NewTextExporter("buf", &buf)
	//: export writes the counter line.
	if err := exp.Export(m.Collect()); err != nil {
		t.Fatalf("Export: %v", err)
	}
	//: the rendered text contains the counter.
	if !strings.Contains(buf.String(), "counter requests 7") {
		t.Errorf("text output missing counter line: %q", buf.String())
	}
}

// TestExporterRegistry covers the default text exporter + Export dispatch.
func TestExporterRegistry(t *testing.T) {
	t.Parallel()
	//: the default "text" exporter self-registered on import.
	found := false
	for _, n := range coremetrics.AvailableExporters() {
		if n == "text" {
			found = true
		}
	}
	if !found {
		t.Errorf("default text exporter not registered: %v", coremetrics.AvailableExporters())
	}
	//: an unknown exporter name surfaces UnknownExporter.
	if err := coremetrics.Export("nope", coremetrics.SnapshotValue{}); err == nil {
		t.Error("Export(nope) err=nil, want UnknownExporter")
	}
}
