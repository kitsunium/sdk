package trace_test

import (
	"context"
	"testing"

	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
)

// stubSpan is the smallest Span: an identity, and nothing recorded.
type stubSpan struct{ context coretrace.SpanContextValue }

func (s *stubSpan) SpanContext() coretrace.SpanContextValue { return s.context }
func (s *stubSpan) SetAttrs(...coreotel.AttrValue)          {}
func (s *stubSpan) AddEvent(string, ...coreotel.AttrValue)  {}
func (s *stubSpan) SetStatus(coretrace.StatusCode, string)  {}
func (s *stubSpan) End()                                    {}

// TestContextWithSpanReadsBackTheSpansContext pins ContextWithSpan's promise:
// SpanContextFromContext reads back exactly what ContextWithSpanContext would
// have stored for span.SpanContext(); the two writers shadow each other in
// either order — one slot, two spellings of what goes in it; and a nil span is
// the deliberate absence of a trace, read back as the invalid zero value and
// shadowing an outer context as ContextWithSpanContext's invalid one does.
func TestContextWithSpanReadsBackTheSpansContext(t *testing.T) {
	outer := specContext(t)
	inner := coretrace.SpanContextValue{TraceID: outer.TraceID, SpanID: coretrace.SpanID{9, 9, 9, 9, 9, 9, 9, 9}, Flags: outer.Flags}
	background := context.Background()
	type tc struct {
		name string
		ctx  context.Context
		want coretrace.SpanContextValue
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := coretrace.SpanContextFromContext(c.ctx)
		if got.TraceID != c.want.TraceID || got.SpanID != c.want.SpanID || got.Flags != c.want.Flags || got.IsValid() != c.want.IsValid() {
			t.Errorf("%s: SpanContextFromContext = %+v, want %+v", c.name, got, c.want)
		}
	}
	cases := []tc{
		{
			name: "a span, read back through it",
			ctx:  coretrace.ContextWithSpan(background, &stubSpan{context: outer}),
			want: outer,
		},
		{
			name: "an invalid value written over a span shadows it",
			ctx:  coretrace.ContextWithSpanContext(coretrace.ContextWithSpan(background, &stubSpan{context: outer}), coretrace.SpanContextValue{}),
			want: coretrace.SpanContextValue{},
		},
		{
			name: "a span written over a value shadows it",
			ctx:  coretrace.ContextWithSpan(coretrace.ContextWithSpanContext(background, outer), &stubSpan{context: inner}),
			want: inner,
		},
		{
			name: "a nil span shadows the outer context",
			ctx:  coretrace.ContextWithSpan(coretrace.ContextWithSpanContext(background, outer), nil),
			want: coretrace.SpanContextValue{},
		},
	}
	for _, c := range cases {
		runCase(t, c)
	}
}
