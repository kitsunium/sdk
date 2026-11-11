//go:build !race

// The fixture of metrics' performance contract (design/sdk.yaml, budgets): it
// sets one call up and returns it, and the gen_perf_test.go kit gen writes
// beside this file counts its allocations against its budget. Race off.
package metrics_test

import (
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	svcmetrics "github.com/kitsunium/sdk/internal/service/observe/metrics"
)

// perfOTLPSeries is how many series the fixture's one sum name carries: few,
// because the window runs 60 000 encodings with the collector held off.
const perfOTLPSeries int = 8

// perfEncodeErr keeps each call's result alive.
var perfEncodeErr error

// perfEncodeOTLPJSON is EncodeOTLPJSON's fixture (#813): one sum name of
// perfOTLPSeries series, three attributes each, the bench's shape, encoded
// whole — the tree built from one arena, not a slice and a pointer per point,
// and rendered into one buffer. The count does not grow with the series.
func perfEncodeOTLPJSON(testing.TB) func() {
	points := make([]coremetrics.SumValue, perfOTLPSeries)
	for series := range points {
		points[series] = coremetrics.SumValue{Attrs: benchOTLPAttrs(series), Value: int64(series)}
	}
	snap := benchOTLPSnapshot(coremetrics.SnapshotValue{Sums: map[string]coremetrics.SumMetricValue{
		"http.server.requests": {Temporality: coremetrics.TemporalityCumulative, Monotonic: true, Points: points},
	}})
	return func() {
		benchOTLPDoc, perfEncodeErr = svcmetrics.EncodeOTLPJSON(snap)
	}
}
