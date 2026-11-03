package metrics_test

import (
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
