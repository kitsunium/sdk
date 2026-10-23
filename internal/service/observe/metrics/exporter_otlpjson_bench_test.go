// Package metrics_test — what one OTLP/JSON document costs to encode from a
// snapshot: the tree built from the model, then rendered. The snapshot is built
// once, outside the measurement, the way a Meter hands one to an exporter.
package metrics_test

import (
	"math"
	"strconv"
	"testing"
	"time"

	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	svcmetrics "github.com/kitsunium/sdk/internal/service/observe/metrics"
)

// The benchmark's shape: ten names of a hundred series each.
const (
	benchOTLPNames        int = 10
	benchOTLPSeriesByName int = 100
)

// benchOTLPDoc keeps the compiler from proving a document dead.
var benchOTLPDoc []byte

// benchOTLPAttrs is the three-dimension set every series carries, the third
// one varying per series so no two points are alike.
func benchOTLPAttrs(series int) []coreotel.AttrValue {
	return []coreotel.AttrValue{
		coreotel.String("http.request.method", "GET"),
		coreotel.String("http.route", "/v1/orders/{id}"),
		coreotel.Int64("http.response.status_code", int64(200+series%5)),
	}
}

// benchOTLPSums is a snapshot of benchOTLPNames counters, each with
// benchOTLPSeriesByName attributed series.
func benchOTLPSums() coremetrics.SnapshotValue {
	sums := make(map[string]coremetrics.SumMetricValue, benchOTLPNames)
	for name := range benchOTLPNames {
		points := make([]coremetrics.SumValue, benchOTLPSeriesByName)
		for series := range points {
			points[series] = coremetrics.SumValue{Attrs: benchOTLPAttrs(series), Value: int64(series) * 1_000_003}
		}
		sums["http.server.requests."+strconv.Itoa(name)] = coremetrics.SumMetricValue{
			Temporality: coremetrics.TemporalityCumulative,
			Monotonic:   true,
			Points:      points,
		}
	}
	return benchOTLPSnapshot(coremetrics.SnapshotValue{Sums: sums})
}

// benchOTLPHistograms is a snapshot of benchOTLPNames histograms on the
// default ladder, each with benchOTLPSeriesByName attributed series.
func benchOTLPHistograms() coremetrics.SnapshotValue {
	bounds := []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10}
	histograms := make(map[string]coremetrics.HistogramMetricValue, benchOTLPNames)
	for name := range benchOTLPNames {
		points := make([]coremetrics.HistogramValue, benchOTLPSeriesByName)
		for series := range points {
			counts := make([]uint64, len(bounds)+1)
			for bucket := range counts {
				counts[bucket] = uint64(series * bucket)
			}
			points[series] = coremetrics.HistogramValue{
				Attrs:  benchOTLPAttrs(series),
				Bounds: bounds,
				Counts: counts,
				Sum:    float64(series) * math.Pi,
				Count:  uint64(series * len(counts)),
			}
		}
		histograms["http.server.duration."+strconv.Itoa(name)] = coremetrics.HistogramMetricValue{
			Temporality: coremetrics.TemporalityDelta,
			Points:      points,
		}
	}
	return benchOTLPSnapshot(coremetrics.SnapshotValue{Histograms: histograms})
}

// benchOTLPSnapshot stamps the resource, the scope and the window.
func benchOTLPSnapshot(snap coremetrics.SnapshotValue) coremetrics.SnapshotValue {
	snap.Resource = coreotel.ResourceValue{Attrs: []coreotel.AttrValue{
		coreotel.String(coreotel.ServiceNameKey, "checkout"),
	}}
	snap.Scope = coreotel.ScopeValue{Name: "github.com/acme/orders", Version: "1.4.0"}
	snap.StartTime = time.Unix(1_700_000_000, 0)
	snap.Time = time.Unix(1_700_000_010, 0)
	return snap
}

// runEncodeOTLPJSON measures EncodeOTLPJSON on a snapshot built once.
func runEncodeOTLPJSON(b *testing.B, snap coremetrics.SnapshotValue) {
	b.ReportAllocs()
	for b.Loop() {
		doc, err := svcmetrics.EncodeOTLPJSON(snap)
		if err != nil {
			b.Fatal(err)
		}
		benchOTLPDoc = doc
	}
}

// BenchmarkEncodeOTLPJSON_1000Sums prices a busy counter export: 1 000 points,
// three attributes each.
func BenchmarkEncodeOTLPJSON_1000Sums(b *testing.B) { runEncodeOTLPJSON(b, benchOTLPSums()) }

// BenchmarkEncodeOTLPJSON_1000Histograms prices a busy histogram export: 1 000
// points on a fifteen-bucket ladder, three attributes each.
func BenchmarkEncodeOTLPJSON_1000Histograms(b *testing.B) {
	runEncodeOTLPJSON(b, benchOTLPHistograms())
}
