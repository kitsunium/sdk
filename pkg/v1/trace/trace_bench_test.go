package trace_test

import (
	"context"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/metrics"
	"github.com/kitsunium/sdk/pkg/v1/trace"
)

// sinks so no span or context can be proven unused and elided.
var (
	ctxSink  context.Context
	spanSink trace.Span
	scSink   trace.SpanContext
)

// benchAttrs is the attribute set a realistic server span carries. Built once,
// outside every timed loop.
var benchAttrs = []metrics.Attr{
	metrics.String("http.request.method", "GET"),
	metrics.String("http.route", "/v1/orders/{id}"),
	metrics.Int64("http.response.status_code", 200),
}

// benchTracer builds a tracer whose sink discards, so every number below is the
// tracer's own cost with the export held at zero — which is what a caller needs
// in order to know what instrumenting a call site costs before any exporter is
// wired.
func benchTracer(b *testing.B, sampler trace.Sampler) trace.Tracer {
	b.Helper()
	return trace.NewTracer(trace.TracerConfig{
		Sampler: sampler,
		Sink:    func(trace.SpanData) {},
	})
}

// BenchmarkStartEnd_Sampled is the number that decides how freely a codebase
// can instrument: one span, started and ended, on a service that is recording.
func BenchmarkStartEnd_Sampled(b *testing.B) {
	tracer := benchTracer(b, trace.AlwaysSample)
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		child, span := tracer.Start(ctx, "GET /v1/orders/{id}", trace.SpanParams{})
		span.End()
		ctxSink, spanSink = child, span
	}
}

// BenchmarkStartEnd_NotSampled is the same call on a service that is NOT
// recording, which is the overwhelmingly common state under any realistic
// sampling ratio. The gap against the sampled row is what a sampling decision
// buys, and it is the number that justifies instrumenting a hot path at all.
func BenchmarkStartEnd_NotSampled(b *testing.B) {
	tracer := benchTracer(b, trace.NeverSample)
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		child, span := tracer.Start(ctx, "GET /v1/orders/{id}", trace.SpanParams{})
		span.End()
		ctxSink, spanSink = child, span
	}
}

// BenchmarkStartEnd_SampledWithAttrs adds the three attributes a server span
// really carries, so the delta says what describing a span costs on top of
// having one.
func BenchmarkStartEnd_SampledWithAttrs(b *testing.B) {
	tracer := benchTracer(b, trace.AlwaysSample)
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		child, span := tracer.Start(ctx, "GET /v1/orders/{id}",
			trace.SpanParams{Kind: trace.KindServer, Attrs: benchAttrs})
		span.End()
		ctxSink, spanSink = child, span
	}
}

// BenchmarkStartEnd_Nested is three levels deep, which is what one HTTP request
// through a handler, a service and a repository actually produces.
func BenchmarkStartEnd_Nested(b *testing.B) {
	tracer := benchTracer(b, trace.AlwaysSample)
	root := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		c1, s1 := tracer.Start(root, "handler", trace.SpanParams{})
		c2, s2 := tracer.Start(c1, "service", trace.SpanParams{})
		c3, s3 := tracer.Start(c2, "repository", trace.SpanParams{})
		s3.End()
		s2.End()
		s1.End()
		ctxSink, spanSink = c3, s1
	}
}

// BenchmarkSpan_SetAttrs and BenchmarkSpan_AddEvent price the two calls made
// DURING a span, which a handler makes as it learns things.
func BenchmarkSpan_SetAttrs(b *testing.B) {
	tracer := benchTracer(b, trace.AlwaysSample)
	_, span := tracer.Start(b.Context(), "bench", trace.SpanParams{})
	defer span.End()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		span.SetAttrs(benchAttrs...)
	}
	spanSink = span
}

func BenchmarkSpan_AddEvent(b *testing.B) {
	tracer := benchTracer(b, trace.AlwaysSample)
	_, span := tracer.Start(b.Context(), "bench", trace.SpanParams{})
	defer span.End()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		span.AddEvent("cache.miss", benchAttrs...)
	}
	spanSink = span
}

// BenchmarkSpan_SpanContext is the accessor a propagator and a log correlator
// both call, so it must stay a field read.
func BenchmarkSpan_SpanContext(b *testing.B) {
	tracer := benchTracer(b, trace.AlwaysSample)
	_, span := tracer.Start(b.Context(), "bench", trace.SpanParams{})
	defer span.End()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		scSink = span.SpanContext()
	}
}

// BenchmarkSampler_Ratio prices the decision itself, made once per root span.
func BenchmarkSampler_Ratio(b *testing.B) {
	sampler, err := trace.Ratio(0.1)
	if err != nil {
		b.Fatalf("Ratio: %v", err)
	}
	tracer := benchTracer(b, sampler)
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		child, span := tracer.Start(ctx, "bench", trace.SpanParams{})
		span.End()
		ctxSink, spanSink = child, span
	}
}
