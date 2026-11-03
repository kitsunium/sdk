package metrics_test

import (
	"bytes"
	"math"
	"math/rand/v2"
	"strings"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	svcmetrics "github.com/kitsunium/sdk/internal/service/observe/metrics"
)

// exponentialOf mints an exponential histogram on m.
func exponentialOf(t *testing.T, m coremetrics.FullMeter, name string, maxSize int, attrs ...coreotel.AttrValue) coremetrics.Histogram {
	t.Helper()
	em, ok := m.(coremetrics.ExponentialMeter)
	if !ok {
		t.Fatal("the in-memory meter is not an ExponentialMeter")
	}
	return em.ExponentialHistogram(name, maxSize, attrs...)
}

// bucketOf is the count of the bucket holding magnitude at the point's scale.
func bucketOf(point coremetrics.ExponentialHistogramValue, value float64) uint64 {
	run := point.Positive
	if value < 0 {
		run = point.Negative
	}
	slot := int(coremetrics.ExponentialIndex(math.Abs(value), point.Scale) - run.Offset)
	if slot < 0 || slot >= len(run.Counts) {
		return 0
	}
	return run.Counts[slot]
}

// TestTheDownscaleKeepsEveryValueInItsBucket (#869 acceptance): values over
// many orders of magnitude, both signs, into eight buckets per range: the
// scale falls only as far as the range needs, no range exceeds maxSize,
// every recorded value lies in a counted bucket at the final scale, and the
// buckets, the zero count, the count and the sum agree.
func TestTheDownscaleKeepsEveryValueInItsBucket(t *testing.T) {
	t.Parallel()
	const maxSize = 8
	m := svcmetrics.NewMeter()
	h := exponentialOf(t, m, "latency", maxSize)
	rng := rand.New(rand.NewPCG(7, 9))
	values := []float64{0, 0}
	for range 500 {
		v := math.Exp(rng.Float64()*40 - 20)
		if rng.IntN(4) == 0 {
			v = -v
		}
		values = append(values, v)
	}
	var sum float64
	for _, v := range values {
		h.Record(v)
		sum += v
	}
	h.Record(math.NaN())
	h.Record(math.Inf(1))
	point := m.Collect().ExponentialHistograms["latency"].Points[0]
	if point.Count != uint64(len(values)) || point.ZeroCount != 2 || math.Abs(point.Sum-sum) > 1e-9*math.Abs(sum) {
		t.Fatalf("count %d zero %d sum %g; want %d, 2, %g (NaN and +Inf dropped)", point.Count, point.ZeroCount, point.Sum, len(values), sum)
	}
	var buckets uint64
	for _, run := range []coremetrics.ExponentialBucketsValue{point.Positive, point.Negative} {
		if len(run.Counts) > maxSize {
			t.Fatalf("a range holds %d buckets, more than %d", len(run.Counts), maxSize)
		}
		for _, c := range run.Counts {
			buckets += c
		}
	}
	if buckets+point.ZeroCount != point.Count {
		t.Fatalf("buckets %d + zero %d != count %d", buckets, point.ZeroCount, point.Count)
	}
	for _, v := range values[2:] {
		if bucketOf(point, v) == 0 {
			t.Fatalf("%g lies in no counted bucket at scale %d", v, point.Scale)
		}
	}
	//: the scale is the highest that fits: one step finer, a range overflows.
	finer, overflows := point.Scale+1, false
	for _, run := range [][]float64{positives(values), negatives(values)} {
		if len(run) == 0 {
			continue
		}
		lo, hi := coremetrics.ExponentialIndex(slicesMin(run), finer), coremetrics.ExponentialIndex(slicesMax(run), finer)
		overflows = overflows || int(hi-lo)+1 > maxSize
	}
	if !overflows {
		t.Fatalf("scale %d fell further than the range needs: %d fits too", point.Scale, finer)
	}
	if point.Scale >= coremetrics.ExponentialMaxScale || point.Scale < coremetrics.ExponentialMinScale {
		t.Fatalf("scale %d after a 40-octave range in %d buckets", point.Scale, maxSize)
	}
}

// positives and negatives split values by sign, as magnitudes, zero aside.
func positives(values []float64) (out []float64) {
	for _, v := range values {
		if v > 0 {
			out = append(out, v)
		}
	}
	return out
}

func negatives(values []float64) (out []float64) {
	for _, v := range values {
		if v < 0 {
			out = append(out, -v)
		}
	}
	return out
}

func slicesMin(v []float64) float64 {
	m := v[0]
	for _, x := range v {
		m = min(m, x)
	}
	return m
}

func slicesMax(v []float64) float64 {
	m := v[0]
	for _, x := range v {
		m = max(m, x)
	}
	return m
}

// TestAnExponentialHistogramStartsAtTheTopScale: one value is one bucket at
// ExponentialMaxScale; a maxSize of zero is the default, one is refused; a
// delta collection hands the window over and starts again at the top scale.
func TestAnExponentialHistogramStartsAtTheTopScale(t *testing.T) {
	t.Parallel()
	m := svcmetrics.NewMeterWithConfig(svcmetrics.MeterConfig{Temporality: coremetrics.TemporalityDelta})
	h := exponentialOf(t, m, "one", 0)
	h.Record(3)
	h.Record(1e-3)
	h.Record(1e3)
	point := m.Collect().ExponentialHistograms["one"].Points[0]
	if point.Count != 3 || len(point.Positive.Counts) > coremetrics.DefaultExponentialMaxSize {
		t.Fatalf("point = %+v", point)
	}
	h.Record(3)
	point = m.Collect().ExponentialHistograms["one"].Points[0]
	want := coremetrics.ExponentialIndex(3, coremetrics.ExponentialMaxScale)
	if point.Scale != coremetrics.ExponentialMaxScale || point.Count != 1 || point.Positive.Offset != want || len(point.Positive.Counts) != 1 {
		t.Fatalf("after a delta collection point = %+v, want one bucket at index %d of the top scale", point, want)
	}
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "INVALID_BUCKETS") {
			t.Fatalf("maxSize 1 = %v, want an INVALID_BUCKETS panic", r)
		}
	}()
	exponentialOf(t, m, "two", 1)
}

// otlpExponential is the hand-written ExportMetricsServiceRequest of one
// cumulative exponential histogram: Metric.exponential_histogram (10), the
// point's count (4, fixed64 string), sum (5), scale (6, sint32 number),
// zero_count (7), positive (8) and negative (9) as {offset, bucketCounts},
// zero_threshold (14) omitted at 0, then the attributes.
const otlpExponential = `{"resourceMetrics":[{"resource":{},"scopeMetrics":[{"scope":{},"metrics":[` +
	`{"name":"latency","unit":"s","exponentialHistogram":{"dataPoints":[{` + otlpWindow + `,` +
	`"count":"6","sum":7.5,"scale":2,"zeroCount":"1",` +
	`"positive":{"offset":-3,"bucketCounts":["2","0","1"]},` +
	`"negative":{"offset":4,"bucketCounts":["2"]},` +
	`"attributes":[{"key":"route","value":{"stringValue":"/a"}}]` +
	`}],"aggregationTemporality":2}}` +
	`]}]}]}`

// TestExponentialHistogramOnEveryExporter (#869 acceptance): OTLP/JSON writes
// the point as the hand-written document; Prometheus keeps the le="+Inf"
// bucket, _sum and _count; the text exporter prints the count.
func TestExponentialHistogramOnEveryExporter(t *testing.T) {
	t.Parallel()
	snap := coremetrics.SnapshotValue{StartTime: otlpStart, Time: otlpEnd, ExponentialHistograms: map[string]coremetrics.ExponentialHistogramMetricValue{
		"latency": {Temporality: coremetrics.TemporalityCumulative, Unit: "s", Points: []coremetrics.ExponentialHistogramValue{{
			Attrs:     []coreotel.AttrValue{coreotel.String("route", "/a")},
			Scale:     2,
			ZeroCount: 1,
			Positive:  coremetrics.ExponentialBucketsValue{Offset: -3, Counts: []uint64{2, 0, 1}},
			Negative:  coremetrics.ExponentialBucketsValue{Offset: 4, Counts: []uint64{2}},
			Sum:       7.5,
			Count:     6,
		}}},
	}}
	if got := mustEncode(t, snap); got != otlpExponential {
		t.Fatalf("OTLP/JSON:\n got %s\nwant %s", got, otlpExponential)
	}
	var prom bytes.Buffer
	if err := svcmetrics.NewPrometheusExporter("p", &prom).Export(snap); err != nil {
		t.Fatalf("Prometheus: %v", err)
	}
	wantProm := "# TYPE latency histogram\n" +
		`latency_bucket{route="/a",le="+Inf"} 6` + "\n" +
		`latency_sum{route="/a"} 7.5` + "\n" +
		`latency_count{route="/a"} 6` + "\n"
	if prom.String() != wantProm {
		t.Fatalf("Prometheus:\n got %q\nwant %q", prom.String(), wantProm)
	}
	var text bytes.Buffer
	if err := svcmetrics.NewTextExporter("t", &text).Export(snap); err != nil {
		t.Fatalf("text: %v", err)
	}
	if !strings.Contains(text.String(), "# metric latency exponential_histogram cumulative unit=\"s\"\nlatency{route=\"/a\"} 6\n") {
		t.Fatalf("text:\n%s", text.String())
	}
}

// TestAnExponentialHistogramIsANameOfItsOwnKind: a name bound to an
// explicit histogram cannot be fetched as an exponential one.
func TestAnExponentialHistogramIsANameOfItsOwnKind(t *testing.T) {
	t.Parallel()
	m := svcmetrics.NewMeter()
	m.Histogram("x", nil)
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("an explicit name was reused as an exponential one")
		}
	}()
	exponentialOf(t, m, "x", 0)
}
