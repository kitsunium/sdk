package metrics_test

import (
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/metrics"
)

// TestFacade smoke-tests the public meter + default text export.
func TestFacade(t *testing.T) {
	t.Parallel()
	m := metrics.NewMeter()
	m.Counter("ops").Inc()
	//: Collect + Export through the default "text" exporter (to stderr) succeeds.
	if err := metrics.Export("text", m.Collect()); err != nil {
		t.Fatalf("Export: %v", err)
	}
	//: the default exporter is listed.
	if len(metrics.AvailableExporters()) == 0 {
		t.Error("no exporters registered")
	}
}
