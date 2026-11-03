package metrics_test

import (
	"bytes"
	"runtime"
	"slices"
	"strings"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	svcmetrics "github.com/kitsunium/sdk/internal/service/observe/metrics"
)

// observedSource is an observable histogram's source: the absolute reading
// the next collection reports.
type observedSource struct {
	point coremetrics.HistogramValue
}

// callback reports the source's reading.
func (s *observedSource) callback(observe coremetrics.ObserveHistogram) {
	observe(s.point)
}

// asyncHistograms widens m to the observable-histogram sibling.
func asyncHistograms(t *testing.T, m coremetrics.FullMeter) coremetrics.AsyncHistogramMeter {
	t.Helper()
	am, ok := m.(coremetrics.AsyncHistogramMeter)
	if !ok {
		t.Fatal("the in-memory meter is not an AsyncHistogramMeter")
	}
	return am
}

// TestAnObservableHistogramCarriesTheSourceBuckets (#868): the source's
// bounds are the series' bounds, nothing re-bucketed; a cumulative meter
// reports each reading as it is, a delta meter its growth bucket by bucket,
// and a reading whose counts went down as a restart.
func TestAnObservableHistogramCarriesTheSourceBuckets(t *testing.T) {
	t.Parallel()
	for _, temporality := range []coremetrics.Temporality{coremetrics.TemporalityCumulative, coremetrics.TemporalityDelta} {
		m := svcmetrics.NewMeterWithConfig(svcmetrics.MeterConfig{Temporality: temporality})
		src := &observedSource{point: coremetrics.HistogramValue{
			Attrs: []coreotel.AttrValue{coreotel.String("k", "v")}, Bounds: []float64{1, 2}, Counts: []uint64{1, 2, 3}, Sum: 9,
		}}
		asyncHistograms(t, m).ObservableHistogram("observed", src.callback)
		first := m.Collect().Histograms["observed"].Points[0]
		if !slices.Equal(first.Bounds, []float64{1, 2}) || !slices.Equal(first.Counts, []uint64{1, 2, 3}) || first.Count != 6 || first.Sum != 9 {
			t.Fatalf("%s: first reading = %+v", temporality, first)
		}
		src.point.Counts, src.point.Sum = []uint64{2, 2, 5}, 14
		second := m.Collect().Histograms["observed"].Points[0]
		want, wantSum := []uint64{2, 2, 5}, 14.0
		if temporality == coremetrics.TemporalityDelta {
			want, wantSum = []uint64{1, 0, 2}, 5
		}
		if !slices.Equal(second.Counts, want) || second.Sum != wantSum {
			t.Fatalf("%s: second reading = %+v, want counts %v sum %g", temporality, second, want, wantSum)
		}
		src.point.Counts, src.point.Sum = []uint64{0, 1, 0}, 1
		if restart := m.Collect().Histograms["observed"].Points[0]; !slices.Equal(restart.Counts, []uint64{0, 1, 0}) {
			t.Fatalf("%s: a source that restarted = %+v, want its reading as it is", temporality, restart)
		}
	}
}

// TestAnObservableHistogramRefusesABrokenLayout: a reading the meter would
// refuse at Histogram's creation panics with InvalidBuckets.
func TestAnObservableHistogramRefusesABrokenLayout(t *testing.T) {
	t.Parallel()
	for name, point := range map[string]coremetrics.HistogramValue{
		"counts short":     {Bounds: []float64{1, 2}, Counts: []uint64{1, 2}},
		"bounds unordered": {Bounds: []float64{2, 1}, Counts: []uint64{1, 2, 3}},
		"an infinity":      {Bounds: []float64{1, posInf()}, Counts: []uint64{1, 2, 3}},
	} {
		m := svcmetrics.NewMeter()
		src := &observedSource{point: point}
		asyncHistograms(t, m).ObservableHistogram("broken", src.callback)
		func() {
			defer func() {
				if r := recover(); r == nil || !strings.Contains(r.(string), "INVALID_BUCKETS") {
					t.Fatalf("%s: Collect = %v, want an INVALID_BUCKETS panic", name, r)
				}
			}()
			m.Collect()
		}()
	}
}

// posInf is +Inf, out of line so the literal map reads.
func posInf() float64 {
	var zero float64
	return 1 / zero
}

// TestRuntimeHistogramsCarryTheRuntimeBuckets (#868): on a meter that takes
// an observable histogram, RuntimeInstruments adds go.schedule.duration and
// go.gc.duration in seconds, the runtime's buckets as the explicit bounds
// and no sum; OTLP/JSON writes no sum for them, Prometheus no _sum line,
// and the text exporter their counts.
func TestRuntimeHistogramsCarryTheRuntimeBuckets(t *testing.T) {
	t.Parallel()
	m := svcmetrics.NewMeter()
	svcmetrics.RuntimeInstruments(m, svcmetrics.RuntimeConfig{})
	runtime.GC()
	snap := m.Collect()
	for _, name := range []string{"go.schedule.duration", "go.gc.duration"} {
		metric, ok := snap.Histograms[name]
		if !ok || len(metric.Points) != 1 {
			t.Fatalf("%s: not in the snapshot", name)
		}
		point := metric.Points[0]
		if metric.Unit != "s" || metric.Description == "" || !point.NoSum || len(point.Counts) != len(point.Bounds)+1 || len(point.Bounds) < 2 {
			t.Fatalf("%s: unit %q, NoSum %v, %d bounds, %d counts", name, metric.Unit, point.NoSum, len(point.Bounds), len(point.Counts))
		}
		for i := 1; i < len(point.Bounds); i++ {
			if point.Bounds[i] <= point.Bounds[i-1] {
				t.Fatalf("%s: bounds not increasing at %d", name, i)
			}
		}
	}
	if snap.Histograms["go.gc.duration"].Points[0].Count == 0 {
		t.Error("go.gc.duration counted no pause after runtime.GC")
	}
	doc := mustEncode(t, snap)
	at := strings.Index(doc, `"name":"go.schedule.duration"`)
	if at < 0 {
		t.Fatalf("OTLP document lacks go.schedule.duration")
	}
	entry := doc[at:]
	entry = entry[:strings.Index(entry, `"aggregationTemporality"`)]
	if strings.Contains(entry, `"sum"`) {
		t.Fatalf("OTLP wrote a sum the runtime does not keep: %s", entry)
	}
	underscored := svcmetrics.NewMeter()
	svcmetrics.RuntimeInstruments(underscored, svcmetrics.RuntimeConfig{Underscore: true})
	var prom bytes.Buffer
	if err := svcmetrics.NewPrometheusExporter("p", &prom).Export(underscored.Collect()); err != nil {
		t.Fatalf("Prometheus: %v", err)
	}
	if !strings.Contains(prom.String(), "go_schedule_duration_count ") || strings.Contains(prom.String(), "go_schedule_duration_sum") {
		t.Fatalf("Prometheus exposition of the runtime histogram:\n%s", prom.String())
	}
}
