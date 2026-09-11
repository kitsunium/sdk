package logger_test

import (
	"context"
	"strings"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
)

// sampleTraceID and sampleSpanID are the W3C Trace Context specification's own
// traceparent example, so the expected hex below can be read straight off the
// document rather than off this implementation.
var (
	sampleTraceID = [corelogger.TraceIDLen]byte{
		0x4b, 0xf9, 0x2f, 0x35, 0x77, 0xb3, 0x4d, 0xa6,
		0xa3, 0xce, 0x92, 0x9d, 0x0e, 0x0e, 0x47, 0x36,
	}
	sampleSpanID = [corelogger.SpanIDLen]byte{0x00, 0xf0, 0x67, 0xaa, 0x0b, 0xa9, 0x02, 0xb7}
)

// TestTraceContextValueIsValid pins the "absent span emits nothing" decision at
// its source: the only gate the encoders consult. An all-zero identifier is
// invalid under W3C Trace Context §3.2.2.3 (trace-id) and §3.2.2.4 (span-id),
// and a half-populated pair is refused too — the data model asks that a SpanId
// never travel without its TraceId, and a trace id with no span id names no
// span a backend can join.
func TestTraceContextValueIsValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		tc   corelogger.TraceContextValue
		want bool
	}{
		{name: "zero value is invalid", tc: corelogger.TraceContextValue{}, want: false},
		{
			name: "trace id without span id is invalid",
			tc:   corelogger.TraceContextValue{TraceID: sampleTraceID},
			want: false,
		},
		{
			name: "span id without trace id is invalid",
			tc:   corelogger.TraceContextValue{SpanID: sampleSpanID},
			want: false,
		},
		{
			name: "both identifiers present is valid",
			tc:   corelogger.TraceContextValue{TraceID: sampleTraceID, SpanID: sampleSpanID},
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.tc.IsValid(); got != tc.want {
				t.Errorf("IsValid() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestTraceContextValueAppendHex pins the spelling the specification fixes:
// lowercase hex, 32 digits of trace id and 16 of span id, appended onto the
// caller's buffer rather than returned as a fresh string.
func TestTraceContextValueAppendHex(t *testing.T) {
	t.Parallel()
	tc := corelogger.TraceContextValue{TraceID: sampleTraceID, SpanID: sampleSpanID}
	traceHex := string(tc.AppendTraceIDHex(nil))
	spanHex := string(tc.AppendSpanIDHex(nil))
	if traceHex != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("AppendTraceIDHex = %q, want the specification's example id", traceHex)
	}
	if spanHex != "00f067aa0ba902b7" {
		t.Errorf("AppendSpanIDHex = %q, want the specification's example id", spanHex)
	}
	if len(traceHex) != corelogger.TraceIDHexLen || len(spanHex) != corelogger.SpanIDHexLen {
		t.Errorf("hex lengths = %d/%d, want %d/%d", len(traceHex), len(spanHex), corelogger.TraceIDHexLen, corelogger.SpanIDHexLen)
	}
	if traceHex != strings.ToLower(traceHex) || spanHex != strings.ToLower(spanHex) {
		t.Errorf("hex must be lowercase, got %q / %q", traceHex, spanHex)
	}
	//: the leading zero byte of the span id must survive as "00" — a
	//: big-integer rendering would drop it and produce a 14-digit id.
	if !strings.HasPrefix(spanHex, "00") {
		t.Errorf("AppendSpanIDHex = %q, want the leading zero byte preserved", spanHex)
	}
}

// TestTraceContextValueAppendHexPreservesPrefix proves the two helpers APPEND
// rather than overwrite, which is what lets an encoder render them straight
// into the buffer it borrowed from the pool.
func TestTraceContextValueAppendHexPreservesPrefix(t *testing.T) {
	t.Parallel()
	tc := corelogger.TraceContextValue{TraceID: sampleTraceID, SpanID: sampleSpanID}
	got := string(tc.AppendSpanIDHex(tc.AppendTraceIDHex([]byte("prefix:"))))
	want := "prefix:4bf92f3577b34da6a3ce929d0e0e473600f067aa0ba902b7"
	if got != want {
		t.Errorf("append chain = %q, want %q", got, want)
	}
}

// TestTraceContextSourceZeroValueIsNoTrace documents the port's contract: a
// source that finds nothing returns the zero value rather than an error, and a
// RecordEvent left untouched therefore carries "no trace here".
func TestTraceContextSourceZeroValueIsNoTrace(t *testing.T) {
	t.Parallel()
	src := corelogger.TraceContextSource(func(_ context.Context) corelogger.TraceContextValue {
		return corelogger.TraceContextValue{}
	})
	if src(context.Background()).IsValid() {
		t.Error("a source returning the zero value must report an invalid trace context")
	}
	if (corelogger.RecordEvent{}).TraceContext.IsValid() {
		t.Error("a RecordEvent nobody stamped must report an invalid trace context")
	}
}
