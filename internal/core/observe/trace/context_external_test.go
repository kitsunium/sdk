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
// have stored for span.SpanContext(), and the two writers shadow each other in
// either order — one slot, two spellings of what goes in it.
func TestContextWithSpanReadsBackTheSpansContext(t *testing.T) {
	outer := specContext(t)
	span := &stubSpan{context: outer}
	ctx := coretrace.ContextWithSpan(context.Background(), span)
	if got := coretrace.SpanContextFromContext(ctx); got.TraceID != outer.TraceID || got.SpanID != outer.SpanID || got.Flags != outer.Flags {
		t.Fatalf("SpanContextFromContext = %+v, want the span's %+v", got, outer)
	}
	//: a value written over a span shadows it.
	shadowed := coretrace.ContextWithSpanContext(ctx, coretrace.SpanContextValue{})
	if coretrace.SpanContextFromContext(shadowed).IsValid() {
		t.Error("an invalid context written over a span must shadow it")
	}
	//: a span written over a value shadows it.
	inner := &stubSpan{context: coretrace.SpanContextValue{TraceID: outer.TraceID, SpanID: coretrace.SpanID{9, 9, 9, 9, 9, 9, 9, 9}, Flags: outer.Flags}}
	overValue := coretrace.ContextWithSpan(coretrace.ContextWithSpanContext(context.Background(), outer), inner)
	if got := coretrace.SpanContextFromContext(overValue); got.SpanID != inner.context.SpanID {
		t.Errorf("SpanContextFromContext = %v, want the inner span's %v", got.SpanID, inner.context.SpanID)
	}
}

// TestContextWithANilSpanShadowsTheOuterContext pins the nil case: no span is
// the deliberate absence of a trace, read back as the invalid zero value and
// shadowing an outer context, as ContextWithSpanContext's invalid one does.
func TestContextWithANilSpanShadowsTheOuterContext(t *testing.T) {
	ctx := coretrace.ContextWithSpanContext(context.Background(), specContext(t))
	if coretrace.SpanContextFromContext(coretrace.ContextWithSpan(ctx, nil)).IsValid() {
		t.Error("a nil span must read back as the invalid zero context, shadowing the outer one")
	}
}
