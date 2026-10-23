// Package trace — the OTLP/JSON appender against the reflection marshal it
// replaced: for every tree the corpus builds, appendJSON must write exactly
// the bytes otlp.Marshal writes. encoding/json is the oracle.
package trace

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
	Code:    errs.Pack(0, 3, 0xFE, 12),
	Reason:  "APPEND_ORACLE_FAILED",
	Public:  "The append oracle failed",
	Private: "trace: never reached",
}

// The corpus the generator draws from: strings for every escape class,
// integers and doubles at the limits the specification allows.
var (
	diffStrings = []string{"", "a", "4bf92f3577b34da6a3ce929d0e0e4736", `q"\`, "\x00\x1f\b\f\n\r\t", "<&>", "\u2028\u2029", "é🙂", "\xff\xc0\xaf"}
	diffInts    = []int64{0, 1, -1, math.MinInt64, math.MaxInt64, 1<<53 + 1}
	diffUints   = []uint64{0, 1, math.MaxUint64, 1<<53 + 1, 1_700_000_000_000_000_000}
	diffDoubles = []float64{
		0, math.Copysign(0, -1), 0.25, -1.5, 1e21, 1e-7, math.MaxFloat64,
		math.SmallestNonzeroFloat64, math.NaN(), math.Inf(1), math.Inf(-1),
	}
	diffInt32s  = []int32{0, 1, 5, -1, math.MinInt32, math.MaxInt32}
	diffUint32s = []uint32{0, 0x101, 0x301, math.MaxUint32}
)

// diffGen draws a random tree, every optional member present or not, every
// list nil, empty or populated.
type diffGen struct{ rng *rand.Rand }

func (g diffGen) str() string      { return diffStrings[g.rng.IntN(len(diffStrings))] }
func (g diffGen) u64() otlp.Uint64 { return otlp.Uint64(diffUints[g.rng.IntN(len(diffUints))]) }
func (g diffGen) i32() int32       { return diffInt32s[g.rng.IntN(len(diffInt32s))] }
func (g diffGen) u32() uint32      { return diffUint32s[g.rng.IntN(len(diffUint32s))] }

// count is 0..3, with nil distinct from empty at 0.
func (g diffGen) count() (n int, nilList bool) { return g.rng.IntN(4), g.rng.IntN(2) == 0 }

func (g diffGen) attrs() []otlp.KeyValue {
	n, nilList := g.count()
	if n == 0 && nilList {
		return nil
	}
	kvs := make([]otlp.KeyValue, n)
	for i := range kvs {
		var attr coreotel.AttrValue
		switch g.rng.IntN(5) {
		case 0:
			attr = coreotel.String(g.str(), g.str())
		case 1:
			attr = coreotel.Bool(g.str(), g.rng.IntN(2) == 0)
		case 2:
			attr = coreotel.Int64(g.str(), diffInts[g.rng.IntN(len(diffInts))])
		case 3:
			attr = coreotel.Float64(g.str(), diffDoubles[g.rng.IntN(len(diffDoubles))])
		default:
			attr = coreotel.AttrValue{Key: g.str()}
		}
		kvs[i] = otlp.KeyValueOf(attr)
	}
	return kvs
}

func (g diffGen) span() otlpSpan {
	span := otlpSpan{
		TraceID: g.str(), SpanID: g.str(), TraceState: g.str(), ParentSpanID: g.str(), Name: g.str(),
		Kind: g.i32(), StartTimeUnixNano: g.u64(), EndTimeUnixNano: g.u64(), Attributes: g.attrs(), Flags: g.u32(),
	}
	if events, nilEvents := g.count(); events > 0 || !nilEvents {
		span.Events = make([]otlpEvent, events)
		for i := range span.Events {
			span.Events[i] = otlpEvent{TimeUnixNano: g.u64(), Name: g.str(), Attributes: g.attrs()}
		}
	}
	if links, nilLinks := g.count(); links > 0 || !nilLinks {
		span.Links = make([]otlpLink, links)
		for i := range span.Links {
			span.Links[i] = otlpLink{TraceID: g.str(), SpanID: g.str(), TraceState: g.str(), Attributes: g.attrs(), Flags: g.u32()}
		}
	}
	if g.rng.IntN(2) == 0 {
		span.Status = &otlpStatus{Message: g.str(), Code: g.i32()}
	}
	return span
}

func (g diffGen) request() otlpRequest {
	n, nilList := g.count()
	if n == 0 && nilList {
		return otlpRequest{}
	}
	resources := make([]otlpResourceSpans, n)
	for i := range resources {
		resource := otlpResourceSpans{Resource: otlp.ResourceMessage{Attributes: g.attrs()}}
		if scopes, nilScopes := g.count(); scopes > 0 || !nilScopes {
			resource.ScopeSpans = make([]otlpScopeSpans, scopes)
			for j := range scopes {
				scope := otlpScopeSpans{Scope: otlp.ScopeMessage{Name: g.str(), Version: g.str()}}
				spans, _ := g.count()
				for range spans {
					scope.Spans = append(scope.Spans, g.span())
				}
				resource.ScopeSpans[j] = scope
			}
		}
		resources[i] = resource
	}
	return otlpRequest{ResourceSpans: resources}
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
		{name: "seed 51/2026", seed1: 51, seed2: 2026, trees: 1000},
		{name: "seed 9/3", seed1: 9, seed2: 3, trees: 1000},
	}
	for _, c := range cases {
		runCase(t, c)
	}
}

// TestOTLPDocCapacityGrowsWithTheBatch keeps the estimate honest: the
// envelope, plus one estimate per span.
func TestOTLPDocCapacityGrowsWithTheBatch(t *testing.T) {
	type tc struct {
		name  string
		spans []otlpSpan
		want  int
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := otlpDocCapacity(c.spans); got != c.want {
			t.Errorf("%s: otlpDocCapacity = %d, want %d", c.name, got, c.want)
		}
	}
	cases := []tc{
		{name: "no span", spans: nil, want: otlpDocEnvelopeBytes},
		{name: "two spans", spans: make([]otlpSpan, 2), want: otlpDocEnvelopeBytes + 2*otlpDocSpanBytes},
	}
	for _, c := range cases {
		runCase(t, c)
	}
}
