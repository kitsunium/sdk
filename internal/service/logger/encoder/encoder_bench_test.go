package encoder_test

import (
	"errors"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/service/logger/encoder"
)

const (
	// benchScratchCap is the scratch buffer capacity the encoder benchmarks
	// start from. The production path borrows its destination from
	// kernel/buffer, which is pooled and warm, so re-using one buffer per
	// benchmark models the real call and keeps the allocator out of the
	// measurement.
	benchScratchCap int = 4096

	// benchPlainValue needs no escaping at all.
	benchPlainValue string = "a-value-of-moderate-length"

	// benchQuotedValue is the same length but every other byte needs an
	// escape, so the two lines differ in escaping work and in nothing else.
	benchQuotedValue string = "a\"value\"of\"moderate\"length"

	// benchControlValue carries the control bytes that force the six-byte
	// \u00XX form in JSON and the four-byte \xNN form in strconv.AppendQuote.
	benchControlValue string = "a\x01value\x02of\x03moderate\x04len"
)

// benchEncoded is the package-level observation point for every encoder
// benchmark below. An encoder whose output nothing reads is a pure function
// with a discarded result, and the compiler is entitled to delete the call —
// which would publish a fictitious sub-nanosecond line. Assigning the returned
// slice here makes the work observable.
var benchEncoded []byte

// benchTime is the fixed instant every benchmarked record carries. Stamping it
// in setup keeps clock.Now out of the timed loop and makes the rendered width
// of the timestamp identical across every line of the report.
var benchTime = time.Date(2026, 9, 10, 12, 34, 56, 789_000_000, time.UTC)

// benchTraceContext is a valid W3C trace context used by the correlation
// benchmarks. Built once so the hex rendering, not the parse, is what the
// timed loop sees.
var benchTraceContext = mustTraceContext()

// errBenchCause is the fixed error carried by the KindAny benchmarks.
var errBenchCause = errors.New("connection refused by upstream")

// benchGroup is a nested attribute group of four members.
var benchGroup = corelogger.GroupValue(stringAttrs(4)...)

// benchGroups is a two-deep active group prefix stack.
var benchGroups = []string{"http", "request"}

// mustTraceContext builds the fixed trace identity used by the correlation
// benchmarks, panicking on the unreachable invalid-input path.
func mustTraceContext() corelogger.TraceContextValue {
	tc := corelogger.TraceContextValue{
		TraceID: [corelogger.TraceIDLen]byte{
			0x4b, 0xf9, 0x2f, 0x35, 0x77, 0xb3, 0x4d, 0xa6,
			0xa3, 0xce, 0x92, 0x9d, 0x0e, 0x0e, 0x47, 0x36,
		},
		SpanID: [corelogger.SpanIDLen]byte{0x00, 0xf0, 0x67, 0xaa, 0x0b, 0xa9, 0x02, 0xb7},
	}
	//: the identifiers above are non-zero, so the value is valid by
	//: construction; the guard exists so a future refactor cannot silently
	//: turn every correlation benchmark into the absent-trace path.
	if !tc.IsValid() {
		panic("encoder bench: fixed trace context is not valid")
	}
	return tc
}

// benchRecord builds a record carrying the supplied attributes.
func benchRecord(attrs []corelogger.AttrValue) corelogger.RecordEvent {
	return corelogger.RecordEvent{
		Time:    benchTime,
		Level:   level.Info,
		Message: "user login accepted",
		Attrs:   attrs,
	}
}

// stringAttrs builds n string attributes with fixed-width keys and values so
// the attribute-count sweep varies the count and nothing else.
func stringAttrs(n int) []corelogger.AttrValue {
	out := make([]corelogger.AttrValue, 0, n)
	keys := [...]string{"user", "tenant", "region", "route"}
	for i := range n {
		out = append(out, corelogger.AttrValue{
			Key:   keys[i%len(keys)],
			Value: corelogger.StringValue("a-value-of-moderate-length"),
		})
	}
	return out
}

// runEncode is the shared timed loop: it hands the encoder a re-used scratch
// buffer and parks the result where the compiler must keep it.
func runEncode(b *testing.B, enc encoder.Encoder, groups []string, rec corelogger.RecordEvent) {
	b.Helper()
	dst := make([]byte, 0, benchScratchCap)
	//: encode once outside the timed region so any lazily-initialised
	//: strconv/time state is warm and the first iteration is not an outlier.
	benchEncoded = enc.Append(dst[:0], groups, rec)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		benchEncoded = enc.Append(dst[:0], groups, rec)
	}
}

// -----------------------------------------------------------------------
// 1. text vs json, swept over the attribute count a caller actually varies.
// -----------------------------------------------------------------------

// BenchmarkText_Attrs0 encodes a bare record — header only, no attributes.
func BenchmarkText_Attrs0(b *testing.B) {
	runEncode(b, encoder.NewText(clock.System), nil, benchRecord(nil))
}

// BenchmarkJSON_Attrs0 is BenchmarkText_Attrs0's JSON control.
func BenchmarkJSON_Attrs0(b *testing.B) {
	runEncode(b, encoder.NewJSON(clock.System), nil, benchRecord(nil))
}

// BenchmarkText_Attrs4 encodes four string attributes.
func BenchmarkText_Attrs4(b *testing.B) {
	runEncode(b, encoder.NewText(clock.System), nil, benchRecord(stringAttrs(4)))
}

// BenchmarkJSON_Attrs4 is BenchmarkText_Attrs4's JSON control.
func BenchmarkJSON_Attrs4(b *testing.B) {
	runEncode(b, encoder.NewJSON(clock.System), nil, benchRecord(stringAttrs(4)))
}

// BenchmarkText_Attrs16 encodes sixteen string attributes.
func BenchmarkText_Attrs16(b *testing.B) {
	runEncode(b, encoder.NewText(clock.System), nil, benchRecord(stringAttrs(16)))
}

// BenchmarkJSON_Attrs16 is BenchmarkText_Attrs16's JSON control.
func BenchmarkJSON_Attrs16(b *testing.B) {
	runEncode(b, encoder.NewJSON(clock.System), nil, benchRecord(stringAttrs(16)))
}

// -----------------------------------------------------------------------
// 2. attribute TYPE, four of each, so the delta against Attrs4 is the type.
// -----------------------------------------------------------------------

// typedAttrs builds four attributes all carrying value v.
func typedAttrs(v corelogger.Value) []corelogger.AttrValue {
	out := make([]corelogger.AttrValue, 0, 4)
	keys := [...]string{"a", "b", "c", "d"}
	for _, k := range keys {
		out = append(out, corelogger.AttrValue{Key: k, Value: v})
	}
	return out
}

// BenchmarkText_TypeInt encodes four int64 attributes.
func BenchmarkText_TypeInt(b *testing.B) {
	runEncode(b, encoder.NewText(clock.System), nil, benchRecord(typedAttrs(corelogger.Int64Value(1234567))))
}

// BenchmarkJSON_TypeInt is BenchmarkText_TypeInt's JSON control.
func BenchmarkJSON_TypeInt(b *testing.B) {
	runEncode(b, encoder.NewJSON(clock.System), nil, benchRecord(typedAttrs(corelogger.Int64Value(1234567))))
}

// BenchmarkText_TypeFloat encodes four float64 attributes.
func BenchmarkText_TypeFloat(b *testing.B) {
	runEncode(b, encoder.NewText(clock.System), nil, benchRecord(typedAttrs(corelogger.Float64Value(1234.5678))))
}

// BenchmarkJSON_TypeFloat is BenchmarkText_TypeFloat's JSON control.
func BenchmarkJSON_TypeFloat(b *testing.B) {
	runEncode(b, encoder.NewJSON(clock.System), nil, benchRecord(typedAttrs(corelogger.Float64Value(1234.5678))))
}

// BenchmarkText_TypeTime encodes four time.Time attributes.
func BenchmarkText_TypeTime(b *testing.B) {
	runEncode(b, encoder.NewText(clock.System), nil, benchRecord(typedAttrs(corelogger.TimeValue(benchTime))))
}

// BenchmarkJSON_TypeTime is BenchmarkText_TypeTime's JSON control.
func BenchmarkJSON_TypeTime(b *testing.B) {
	runEncode(b, encoder.NewJSON(clock.System), nil, benchRecord(typedAttrs(corelogger.TimeValue(benchTime))))
}

// BenchmarkText_TypeDuration encodes four time.Duration attributes.
func BenchmarkText_TypeDuration(b *testing.B) {
	runEncode(b, encoder.NewText(clock.System), nil, benchRecord(typedAttrs(corelogger.DurationValue(1500*time.Millisecond))))
}

// BenchmarkJSON_TypeDuration is BenchmarkText_TypeDuration's JSON control.
func BenchmarkJSON_TypeDuration(b *testing.B) {
	runEncode(b, encoder.NewJSON(clock.System), nil, benchRecord(typedAttrs(corelogger.DurationValue(1500*time.Millisecond))))
}

// BenchmarkText_TypeError encodes four error attributes. Both encoders
// degrade KindAny to "?" — this line measures what that degradation costs.
func BenchmarkText_TypeError(b *testing.B) {
	runEncode(b, encoder.NewText(clock.System), nil, benchRecord(typedAttrs(corelogger.AnyValue(errBenchCause))))
}

// BenchmarkJSON_TypeError is BenchmarkText_TypeError's JSON control.
func BenchmarkJSON_TypeError(b *testing.B) {
	runEncode(b, encoder.NewJSON(clock.System), nil, benchRecord(typedAttrs(corelogger.AnyValue(errBenchCause))))
}

// BenchmarkText_TypeGroup encodes four nested-group attributes.
func BenchmarkText_TypeGroup(b *testing.B) {
	runEncode(b, encoder.NewText(clock.System), nil, benchRecord(typedAttrs(benchGroup)))
}

// BenchmarkJSON_TypeGroup is BenchmarkText_TypeGroup's JSON control.
func BenchmarkJSON_TypeGroup(b *testing.B) {
	runEncode(b, encoder.NewJSON(clock.System), nil, benchRecord(typedAttrs(benchGroup)))
}

// -----------------------------------------------------------------------
// 3. escaping — the trap an encoder springs the first time a caller logs a
//    quoted string.
// -----------------------------------------------------------------------

// escapingAttrs builds four string attributes all carrying v.
func escapingAttrs(v string) []corelogger.AttrValue {
	return typedAttrs(corelogger.StringValue(v))
}

// BenchmarkText_EscapePlain encodes four strings needing no escaping.
func BenchmarkText_EscapePlain(b *testing.B) {
	runEncode(b, encoder.NewText(clock.System), nil, benchRecord(escapingAttrs(benchPlainValue)))
}

// BenchmarkJSON_EscapePlain is BenchmarkText_EscapePlain's JSON control.
func BenchmarkJSON_EscapePlain(b *testing.B) {
	runEncode(b, encoder.NewJSON(clock.System), nil, benchRecord(escapingAttrs(benchPlainValue)))
}

// BenchmarkText_EscapeQuoted encodes four strings full of double quotes.
func BenchmarkText_EscapeQuoted(b *testing.B) {
	runEncode(b, encoder.NewText(clock.System), nil, benchRecord(escapingAttrs(benchQuotedValue)))
}

// BenchmarkJSON_EscapeQuoted is BenchmarkText_EscapeQuoted's JSON control.
func BenchmarkJSON_EscapeQuoted(b *testing.B) {
	runEncode(b, encoder.NewJSON(clock.System), nil, benchRecord(escapingAttrs(benchQuotedValue)))
}

// BenchmarkText_EscapeControl encodes four strings full of control bytes.
func BenchmarkText_EscapeControl(b *testing.B) {
	runEncode(b, encoder.NewText(clock.System), nil, benchRecord(escapingAttrs(benchControlValue)))
}

// BenchmarkJSON_EscapeControl is BenchmarkText_EscapeControl's JSON control.
func BenchmarkJSON_EscapeControl(b *testing.B) {
	runEncode(b, encoder.NewJSON(clock.System), nil, benchRecord(escapingAttrs(benchControlValue)))
}

// -----------------------------------------------------------------------
// 4. the group prefix stack and the trace-context rendering path.
// -----------------------------------------------------------------------

// BenchmarkText_GroupPrefix encodes four attributes under a two-deep prefix.
func BenchmarkText_GroupPrefix(b *testing.B) {
	runEncode(b, encoder.NewText(clock.System), benchGroups, benchRecord(stringAttrs(4)))
}

// BenchmarkJSON_GroupPrefix is BenchmarkText_GroupPrefix's JSON control.
func BenchmarkJSON_GroupPrefix(b *testing.B) {
	runEncode(b, encoder.NewJSON(clock.System), benchGroups, benchRecord(stringAttrs(4)))
}

// traceRecord returns the four-attribute record carrying a valid trace
// identity, so the delta against Attrs4 is exactly the correlation fields.
func traceRecord() corelogger.RecordEvent {
	rec := benchRecord(stringAttrs(4))
	rec.TraceContext = benchTraceContext
	return rec
}

// BenchmarkText_TraceContext encodes a record inside a span.
func BenchmarkText_TraceContext(b *testing.B) {
	runEncode(b, encoder.NewText(clock.System), nil, traceRecord())
}

// BenchmarkJSON_TraceContext is BenchmarkText_TraceContext's JSON control.
func BenchmarkJSON_TraceContext(b *testing.B) {
	runEncode(b, encoder.NewJSON(clock.System), nil, traceRecord())
}
