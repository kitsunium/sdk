package trace_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/observe/trace"
)

// sinks so no measured call can be proven dead and elided.
var (
	strSink    string
	okSink     bool
	stateSink  trace.StateValue
	traceIDSnk trace.TraceID
	spanIDSnk  trace.SpanID
	errSink    error
)

// BenchmarkParseTraceID / ParseSpanID isolate the hex decode the engine's
// traceparent parser (internal/service/observe/trace) spends its time in, so a
// profile can tell "the parser is slow" from "hex is slow".
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

// BenchmarkStateValue_Insert is the mutation every hop performs: a vendor
// prepends its own entry. Copy-on-write means the cost is the list length.
func BenchmarkStateValue_Insert(b *testing.B) {
	base := stateOf(b, "vendor1", "value1", "vendor2", "value2", "vendor3", "value3")
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
	base := stateOf(b, "vendor1", "value1", "vendor2", "value2", "vendor3", "value3", "vendor4", "value4")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink, okSink = base.Get("vendor4")
	}
}

// BenchmarkStateValue_String renders the header back for propagation, once per
// outbound call.
func BenchmarkStateValue_String(b *testing.B) {
	base := stateOf(b, "vendor1", "value1", "vendor2", "value2", "vendor3", "value3")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink = base.String()
	}
}
