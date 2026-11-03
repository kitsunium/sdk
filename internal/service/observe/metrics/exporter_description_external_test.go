// Package metrics_test — what the Prometheus grammar alone does with an
// instrument description (ADR 0067): HELP before TYPE, once per name. What
// every exporter does with one — rendered, omitted, escaped — is the
// conformance table's (exporter_conformance_external_test.go).
package metrics_test

import (
	"strings"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
)

// describedSnapshot is one metric of each kind, each carrying help: a sum, a
// gauge, an explicit and an exponential histogram.
func describedSnapshot(help string) coremetrics.SnapshotValue {
	return coremetrics.SnapshotValue{
		Sums: map[string]coremetrics.SumMetricValue{
			"requests_total": {
				Temporality: coremetrics.TemporalityCumulative,
				Monotonic:   true,
				Description: help,
				Points:      []coremetrics.SumValue{{Value: 1}},
			},
		},
		Gauges: map[string]coremetrics.GaugeMetricValue{
			"queue_depth": {
				Description: help,
				Points:      []coremetrics.GaugeValue{{Value: 2}},
			},
		},
		Histograms: map[string]coremetrics.HistogramMetricValue{
			"latency_seconds": {
				Temporality: coremetrics.TemporalityCumulative,
				Description: help,
				//: a ladder OTLP accepts: one count more than the bounds.
				Points: []coremetrics.HistogramValue{{
					Bounds: []float64{1},
					Counts: []uint64{2, 1},
					Sum:    4,
					Count:  3,
				}},
			},
		},
		ExponentialHistograms: map[string]coremetrics.ExponentialHistogramMetricValue{
			"request_size": {
				Temporality: coremetrics.TemporalityCumulative,
				Description: help,
				Points:      []coremetrics.ExponentialHistogramValue{exponentialPoint},
			},
		},
	}
}

// exponentialPoint is one exponential histogram series: a zero, three
// positive and two negative observations at scale 2.
var exponentialPoint = coremetrics.ExponentialHistogramValue{
	Scale:     2,
	ZeroCount: 1,
	Positive:  coremetrics.ExponentialBucketsValue{Offset: -3, Counts: []uint64{2, 0, 1}},
	Negative:  coremetrics.ExponentialBucketsValue{Offset: 4, Counts: []uint64{2}},
	Sum:       7.5,
	Count:     6,
}

// TestPrometheusHelpPrecedesTypeForEveryFamily pins the line the exporter's own
// comment promised for the day the Meter grew a description.
//
// The order is not decorative: the exposition format's published examples put
// HELP above TYPE, a parser reads both as metadata for the name that follows,
// and a HELP line emitted after the first sample would be metadata for a family
// that has already started.
func TestPrometheusHelpPrecedesTypeForEveryFamily(t *testing.T) {
	t.Parallel()
	got := exportPrometheus(t, describedSnapshot("Requests served, by route"))
	type tc struct {
		name string
		want string
	}
	tests := []tc{
		{
			name: "a counter family",
			want: "# HELP requests_total Requests served, by route\n" +
				"# TYPE requests_total counter\n",
		},
		{
			name: "a gauge family",
			want: "# HELP queue_depth Requests served, by route\n" +
				"# TYPE queue_depth gauge\n",
		},
		{
			name: "a histogram family",
			want: "# HELP latency_seconds Requests served, by route\n" +
				"# TYPE latency_seconds histogram\n",
		},
		{
			name: "an exponential histogram family",
			want: "# HELP request_size Requests served, by route\n" +
				"# TYPE request_size histogram\n",
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if !strings.Contains(got, c.want) {
				t.Errorf("the document does not carry\n%q\ngot:\n%s", c.want, got)
			}
		})
	}
}

// TestPrometheusHelpAppearsOncePerName pins the other half of the format's own
// rule — "only one HELP line may exist for any given metric name" — against the
// natural bug, which is emitting the header inside the per-series loop.
func TestPrometheusHelpAppearsOncePerName(t *testing.T) {
	t.Parallel()
	series := make([]coremetrics.SumValue, 0, 64)
	for i := range 64 {
		series = append(series, coremetrics.SumValue{
			Attrs: labelsOf("shard", string(rune('a'+i%26))),
			Value: int64(i),
		})
	}
	metric := promCounter(series...)
	metric.Description = "Requests served"
	got := exportPrometheus(t, coremetrics.SnapshotValue{
		Sums: map[string]coremetrics.SumMetricValue{"requests_total": metric},
	})

	if n := strings.Count(got, "# HELP requests_total"); n != 1 {
		t.Errorf("the HELP header appears %d times across 64 series, want exactly 1", n)
	}
	//: and it opens the family, ahead of TYPE and every sample.
	if !strings.HasPrefix(got, "# HELP requests_total Requests served\n# TYPE requests_total counter\n") {
		t.Errorf("HELP does not open the family:\n%q", got[:min(len(got), 160)])
	}
}
