// Package metrics — the OTLP/JSON appender against the reflection marshal it
// replaced: for every tree the corpus builds, appendJSON must write exactly
// the bytes otlp.Marshal writes. encoding/json is the oracle.
package metrics

import (
	"bytes"
	"math"
	"math/rand/v2"
	"testing"

	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/observe/internal/otlp"
)

// appendOracleFailure is the wrap the oracle would fail under; it never does.
var appendOracleFailure = errs.WrapParams{
	Code:    errs.Pack(0, 3, 0xFE, 11),
	Reason:  "APPEND_ORACLE_FAILED",
	Public:  "The append oracle failed",
	Private: "metrics: never reached",
}

// The corpus the generator draws from: strings for every escape class,
// integers and doubles at the limits the specification allows.
var (
	diffStrings = []string{"", "a", "http.server.requests", `q"\`, "\x00\x1f\b\f\n\r\t", "<&>", "\u2028\u2029", "é🙂", "\xff\xc0\xaf"}
	diffInts    = []int64{0, 1, -1, math.MinInt64, math.MaxInt64, 1<<53 + 1}
	diffUints   = []uint64{0, 1, math.MaxUint64, 1<<53 + 1, 1_767_323_045_000_000_000}
	diffDoubles = []float64{
		0, math.Copysign(0, -1), 0.25, -1.5, 1e21, 1e-7, math.MaxFloat64,
		math.SmallestNonzeroFloat64, math.NaN(), math.Inf(1), math.Inf(-1),
	}
)

// diffGen draws a random tree, every optional member present or not, every
// list nil, empty or populated.
type diffGen struct{ rng *rand.Rand }

func (g diffGen) str() string      { return diffStrings[g.rng.IntN(len(diffStrings))] }
func (g diffGen) u64() otlp.Uint64 { return otlp.Uint64(diffUints[g.rng.IntN(len(diffUints))]) }
func (g diffGen) dbl() otlp.Double { return otlp.Double(diffDoubles[g.rng.IntN(len(diffDoubles))]) }

// count is 0..3, with nil distinct from empty at 0.
func (g diffGen) count() (n int, nilList bool) { return g.rng.IntN(4), g.rng.IntN(2) == 0 }

func (g diffGen) attrs() []otlp.KeyValue {
	n, nilList := g.count()
	if n == 0 && nilList {
		return nil
	}
	set := make([]coreotel.AttrValue, n)
	for i := range set {
		switch g.rng.IntN(5) {
		case 0:
			set[i] = coreotel.String(g.str(), g.str())
		case 1:
			set[i] = coreotel.Bool(g.str(), g.rng.IntN(2) == 0)
		case 2:
			set[i] = coreotel.Int64(g.str(), diffInts[g.rng.IntN(len(diffInts))])
		case 3:
			set[i] = coreotel.Float64(g.str(), float64(g.dbl()))
		default:
			set[i] = coreotel.AttrValue{Key: g.str()}
		}
	}
	kvs := make([]otlp.KeyValue, n)
	for i := range set {
		kvs[i] = otlp.KeyValueOf(set[i])
	}
	return kvs
}

func (g diffGen) numberPoints() []otlpNumberDataPoint {
	n, nilList := g.count()
	if n == 0 && nilList {
		return nil
	}
	points := make([]otlpNumberDataPoint, n)
	for i := range points {
		points[i] = otlpNumberDataPoint{StartTimeUnixNano: g.u64(), TimeUnixNano: g.u64(), Attributes: g.attrs()}
		switch g.rng.IntN(3) {
		case 0:
			points[i].AsInt = new(otlp.Int64(diffInts[g.rng.IntN(len(diffInts))]))
		case 1:
			points[i].AsDouble = new(g.dbl())
		}
	}
	return points
}

func (g diffGen) histogramPoints() []otlpHistogramDataPoint {
	n, nilList := g.count()
	if n == 0 && nilList {
		return nil
	}
	points := make([]otlpHistogramDataPoint, n)
	for i := range points {
		point := otlpHistogramDataPoint{StartTimeUnixNano: g.u64(), TimeUnixNano: g.u64(), Count: g.u64(), Attributes: g.attrs()}
		if g.rng.IntN(3) > 0 {
			point.Sum = new(g.dbl())
		}
		if buckets, nilBuckets := g.count(); buckets > 0 || !nilBuckets {
			point.BucketCounts = make([]otlp.Uint64, buckets)
			point.ExplicitBounds = make([]otlp.Double, buckets)
			for j := range buckets {
				point.BucketCounts[j], point.ExplicitBounds[j] = g.u64(), g.dbl()
			}
		}
		points[i] = point
	}
	return points
}

func (g diffGen) metric() otlpMetric {
	m := otlpMetric{Name: g.str()}
	if g.rng.IntN(2) == 0 {
		m.Description = g.str()
	}
	switch g.rng.IntN(4) {
	case 0:
		m.Gauge = &otlpGauge{DataPoints: g.numberPoints()}
	case 1:
		m.Sum = &otlpSum{DataPoints: g.numberPoints(), AggregationTemporality: g.rng.IntN(3), IsMonotonic: g.rng.IntN(2) == 0}
	case 2:
		m.Histogram = &otlpHistogram{DataPoints: g.histogramPoints(), AggregationTemporality: g.rng.IntN(3)}
	}
	return m
}

func (g diffGen) request() otlpRequest {
	n, nilList := g.count()
	if n == 0 && nilList {
		return otlpRequest{}
	}
	resources := make([]otlpResourceMetrics, n)
	for i := range resources {
		resource := otlpResourceMetrics{Resource: otlp.ResourceMessage{Attributes: g.attrs()}}
		if scopes, nilScopes := g.count(); scopes > 0 || !nilScopes {
			resource.ScopeMetrics = make([]otlpScopeMetrics, scopes)
			for j := range scopes {
				scope := otlpScopeMetrics{Scope: otlp.ScopeMessage{Name: g.str(), Version: g.str()}}
				metrics, _ := g.count()
				for range metrics {
					scope.Metrics = append(scope.Metrics, g.metric())
				}
				resource.ScopeMetrics[j] = scope
			}
		}
		resources[i] = resource
	}
	return otlpRequest{ResourceMetrics: resources}
}

// TestOTLPAppendJSONIsMarshalsRendering compares the appender with the
// reflection marshal on two thousand random trees, drawn from two seeds.
func TestOTLPAppendJSONIsMarshalsRendering(t *testing.T) {
	type tc struct {
		name         string
		seed1, seed2 uint64
		trees        int
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		g := diffGen{rng: rand.New(rand.NewPCG(c.seed1, c.seed2))}
		for i := range c.trees {
			request := g.request()
			want, err := otlp.Marshal(request, &appendOracleFailure)
			if err != nil {
				t.Fatalf("%s, tree %d: Marshal: %v", c.name, i, err)
			}
			if got := request.appendJSON(nil); !bytes.Equal(got, want) {
				t.Fatalf("%s, tree %d:\n got %s\nwant %s", c.name, i, got, want)
			}
		}
	}
	cases := []tc{
		{name: "seed 48/2026", seed1: 48, seed2: 2026, trees: 1000},
		{name: "seed 7/1", seed1: 7, seed2: 1, trees: 1000},
	}
	for _, c := range cases {
		runCase(t, c)
	}
}

// TestOTLPDocCapacityCoversTheEnvelope keeps the estimate honest: never below
// the envelope, and exactly the envelope for an empty list.
func TestOTLPDocCapacityCoversTheEnvelope(t *testing.T) {
	g := diffGen{rng: rand.New(rand.NewPCG(1, 2))}
	type tc struct {
		name  string
		list  []otlpMetric
		exact bool
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := otlpDocCapacity(c.list)
		if c.exact && got != otlpDocEnvelopeBytes {
			t.Errorf("%s: otlpDocCapacity = %d, want %d", c.name, got, otlpDocEnvelopeBytes)
		}
		if got < otlpDocEnvelopeBytes {
			t.Errorf("%s: otlpDocCapacity = %d, below the envelope %d", c.name, got, otlpDocEnvelopeBytes)
		}
	}
	cases := []tc{
		{name: "no metric", list: nil, exact: true},
		{name: "three random metrics", list: []otlpMetric{g.metric(), g.metric(), g.metric()}},
	}
	for _, c := range cases {
		runCase(t, c)
	}
}
