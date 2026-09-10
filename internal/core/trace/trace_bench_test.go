package trace_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/trace"
)

// a well-formed W3C traceparent, the exact shape an inbound request carries.
const goodParent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

// the two malformed shapes a public endpoint actually receives: a wrong-length
// identifier, and an all-zero trace id §3.2.2.3 obliges a receiver to reject.
const (
	shortParent = "00-4bf92f3577b34da6a3ce929d0e0e47-00f067aa0ba902b7-01"
	zeroParent  = "00-00000000000000000000000000000000-00f067aa0ba902b7-01"
)

// sinks so no measured call can be proven dead and elided.
var (
	ctxSink    trace.SpanContextValue
	strSink    string
	okSink     bool
	stateSink  trace.StateValue
	traceIDSnk trace.TraceID
	spanIDSnk  trace.SpanID
	errSink    error
)

// BenchmarkParseTraceParent_Valid is the hottest function in the domain: every
// inbound request on a traced service runs it exactly once, before any of the
// work the request came to do. A per-request parser that allocates is a
// per-request garbage generator, so the alloc column matters more than the ns.
func BenchmarkParseTraceParent_Valid(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		ctxSink, errSink = trace.ParseTraceParent(goodParent)
	}
}

// BenchmarkParseTraceParent_Malformed and _AllZero are the REFUSAL paths, and
// they are benchmarked because a public endpoint is the one place a caller
// cannot choose its input. A refusal that costs far more than an acceptance is
// an amplification an attacker picks for free.
func BenchmarkParseTraceParent_Malformed(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		ctxSink, errSink = trace.ParseTraceParent(shortParent)
	}
}

func BenchmarkParseTraceParent_AllZero(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		ctxSink, errSink = trace.ParseTraceParent(zeroParent)
	}
}

// BenchmarkFormatTraceParent is the outbound half: once per outbound call on a
// traced service, so it runs as often as the parser on a service that fans out.
func BenchmarkFormatTraceParent(b *testing.B) {
	sc, err := trace.ParseTraceParent(goodParent)
	if err != nil {
		b.Fatalf("seed: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink, okSink = trace.FormatTraceParent(sc)
	}
}

// BenchmarkParseTraceID / ParseSpanID isolate the hex decode the two parsers
// above share, so a profile can tell "the parser is slow" from "hex is slow".
func BenchmarkParseTraceID(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		traceIDSnk, errSink = trace.ParseTraceID("4bf92f3577b34da6a3ce929d0e0e4736")
	}
}

func BenchmarkParseSpanID(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		spanIDSnk, errSink = trace.ParseSpanID("00f067aa0ba902b7")
	}
}

// BenchmarkTraceID_String is the rendering every log line and every exporter
// payload pays per span. It returns a string, so one allocation is structural
// unless the caller appends into a buffer instead.
func BenchmarkTraceID_String(b *testing.B) {
	id, err := trace.ParseTraceID("4bf92f3577b34da6a3ce929d0e0e4736")
	if err != nil {
		b.Fatalf("seed: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink = id.String()
	}
}

// BenchmarkParseTraceState_* walk the second header. tracestate is a LIST, so
// its cost scales with how many vendors are already in it — which is the fact a
// caller needs, because the list grows by one at every hop.
func BenchmarkParseTraceState_1(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		stateSink, errSink = trace.ParseTraceState("vendor1=value1")
	}
}

func BenchmarkParseTraceState_4(b *testing.B) {
	const header = "vendor1=value1,vendor2=value2,vendor3=value3,vendor4=value4"
	b.ReportAllocs()
	for b.Loop() {
		stateSink, errSink = trace.ParseTraceState(header)
	}
}

// BenchmarkStateValue_Insert is the mutation every hop performs: a vendor
// prepends its own entry. Copy-on-write means the cost is the list length.
func BenchmarkStateValue_Insert(b *testing.B) {
	base, err := trace.ParseTraceState("vendor1=value1,vendor2=value2,vendor3=value3")
	if err != nil {
		b.Fatalf("seed: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		stateSink, errSink = base.Insert("mine", "v")
	}
}

// BenchmarkStateValue_Get is the read a sampler or a vendor does to find its
// own entry — a linear walk, which is correct for a list this short and is
// worth pinning as such.
func BenchmarkStateValue_Get(b *testing.B) {
	base, err := trace.ParseTraceState("vendor1=value1,vendor2=value2,vendor3=value3,vendor4=value4")
	if err != nil {
		b.Fatalf("seed: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink, okSink = base.Get("vendor4")
	}
}

// BenchmarkStateValue_String renders the header back for propagation, once per
// outbound call.
func BenchmarkStateValue_String(b *testing.B) {
	base, err := trace.ParseTraceState("vendor1=value1,vendor2=value2,vendor3=value3")
	if err != nil {
		b.Fatalf("seed: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink = base.String()
	}
}

// mapCarrier is the minimum Carrier: two map operations, so the numbers below
// measure Inject/Extract and not an HTTP header implementation.
type mapCarrier map[string]string

func (m mapCarrier) Get(key string) string { return m[key] }
func (m mapCarrier) Set(key, value string) { m[key] = value }

// BenchmarkExtract / BenchmarkInject are the two calls a middleware makes, and
// they are what a caller actually budgets — the parse and format above are
// their inner cost.
func BenchmarkExtract(b *testing.B) {
	carrier := mapCarrier{"traceparent": goodParent}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		ctxSink = trace.Extract(carrier)
	}
}

func BenchmarkExtract_WithState(b *testing.B) {
	carrier := mapCarrier{
		"traceparent": goodParent,
		"tracestate":  "vendor1=value1,vendor2=value2",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		ctxSink = trace.Extract(carrier)
	}
}

// BenchmarkExtract_Absent is the untraced request — the common case on any
// public entry point — and it must be the cheap one.
func BenchmarkExtract_Absent(b *testing.B) {
	carrier := mapCarrier{}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		ctxSink = trace.Extract(carrier)
	}
}

func BenchmarkInject(b *testing.B) {
	sc, err := trace.ParseTraceParent(goodParent)
	if err != nil {
		b.Fatalf("seed: %v", err)
	}
	carrier := mapCarrier{}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		trace.Inject(sc, carrier)
	}
}

// BenchmarkInject_Invalid pins the documented shortcut: an invalid context
// writes NOTHING, so it must not pay for formatting a header nobody will read.
func BenchmarkInject_Invalid(b *testing.B) {
	carrier := mapCarrier{}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		trace.Inject(trace.SpanContextValue{}, carrier)
	}
}
