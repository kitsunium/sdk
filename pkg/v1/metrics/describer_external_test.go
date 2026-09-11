package metrics_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/metrics"
)

// A description reaches consumers through a SIBLING port, not through Meter,
// and the facade has to publish three things for that to be usable at all: the
// Describer alias, a meter value that satisfies it, and the two sentinels its
// refusals carry. Pinning the assertion itself is the point — the FullMeter
// NewMeter returns deliberately does NOT name Describe, so a consumer who
// cannot write this line has no way in at all (ADR 0067).
func TestFacadeDescriberReachesTheWire(t *testing.T) {
	t.Parallel()
	meter := metrics.NewMeter()
	describer, ok := meter.(metrics.Describer)
	if !ok {
		t.Fatal("the meter NewMeter returns does not satisfy the published Describer")
	}
	describer.Describe("requests_total", "Requests served, by method")
	meter.Counter("requests_total", metrics.String("method", "GET")).Inc()

	var buf bytes.Buffer
	if err := metrics.NewPrometheusExporter("scrape", &buf).Export(meter.Collect()); err != nil {
		t.Fatalf("Export = %v, want nil", err)
	}

	want := "# HELP requests_total Requests served, by method\n" +
		"# TYPE requests_total counter\nrequests_total{method=\"GET\"} 1\n"
	if buf.String() != want {
		t.Errorf("Export wrote %q, want %q", buf.String(), want)
	}
}

// The two refusals are sentinels a consumer can NAME, which is what makes a
// recovered panic actionable instead of a string to grep. They are separate
// because the repairs are separate: write a description, or reconcile two
// wiring sites that disagree.
func TestFacadeDescribeSentinelsAreReachable(t *testing.T) {
	t.Parallel()
	if metrics.InvalidDescription == nil || metrics.DescriptionConflict == nil {
		t.Fatal("the facade does not publish the Describe sentinels")
	}
	if errors.Is(metrics.InvalidDescription, metrics.DescriptionConflict) {
		t.Error("the two Describe sentinels are indistinguishable through errors.Is")
	}
}
