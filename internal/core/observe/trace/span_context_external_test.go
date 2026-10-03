package trace_test

import (
	"testing"

	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
)

// The W3C specification's own example identifiers (§3.2), used verbatim so a
// reader can compare a test against the document without translating
// anything. The header that carries them is the engine's to parse
// (internal/service/observe/trace); this package's tests build the same span
// context from its parts.
const (
	specTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	specSpanID  = "00f067aa0ba902b7"
	specHeader  = "00-" + specTraceID + "-" + specSpanID + "-01"
)

// specContext is the span context the specification's example header names:
// its two identifiers, the sampled flag, and Remote — a traceparent always
// names a span that ran somewhere else.
func specContext(tb testing.TB) coretrace.SpanContextValue {
	tb.Helper()
	traceID, err := coretrace.ParseTraceID(specTraceID)
	if err != nil {
		tb.Fatalf("ParseTraceID(%q): %v", specTraceID, err)
	}
	spanID, err := coretrace.ParseSpanID(specSpanID)
	if err != nil {
		tb.Fatalf("ParseSpanID(%q): %v", specSpanID, err)
	}
	return coretrace.SpanContextValue{TraceID: traceID, SpanID: spanID, Flags: coretrace.FlagSampled, Remote: true}
}

// stateOf builds a tracestate list from key, value pairs, leftmost first, the
// way the engine's parser does — through a StateBuilder.
func stateOf(tb testing.TB, pairs ...string) coretrace.StateValue {
	tb.Helper()
	builder := coretrace.NewStateBuilder(len(pairs) / 2)
	for i := 0; i+1 < len(pairs); i += 2 {
		if !builder.Add(pairs[i], pairs[i+1]) {
			tb.Fatalf("StateBuilder.Add(%q, %q) refused a seed member", pairs[i], pairs[i+1])
		}
	}
	return builder.State()
}
