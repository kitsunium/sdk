package trace_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/observe/metrics"
	"github.com/kitsunium/sdk/pkg/v1/observe/trace"
)

// TestPublicAttributeIsTheMetricsAttribute pins ADR 0051 §Decision 2 at the
// PUBLIC edge, where it is a promise to consumers rather than an internal detail.
//
// A downstream that instruments both signals writes one attribute helper, not
// two, and the day exemplars link a metric data point to a trace there is no
// conversion at the boundary of the very feature the split would exist to keep
// clean.
func TestPublicAttributeIsTheMetricsAttribute(t *testing.T) {
	fromTrace := trace.String("k", "v")
	fromMetrics := metrics.String("k", "v")
	if fromTrace != fromMetrics {
		t.Error("trace.Attr and metrics.Attr must be one type with one value")
	}
	//: assignable in both directions, which is the actual guarantee — and it is
	//: a COMPILE fact: neither line converts, because there is one type.
	fromMetrics = fromTrace
	fromTrace = fromMetrics
	if fromTrace.Key != "k" {
		t.Fatal("the two aliases are not interchangeable")
	}
	//: both published aliases are exercised by NAME, so a rename on either side
	//: is caught: a []trace.Attr accepts a metrics.Attr and the reverse, with
	//: no conversion anywhere.
	traceAttrs := []trace.Attr{fromMetrics}
	metricAttrs := []metrics.Attr{fromTrace}
	if traceAttrs[0] != metricAttrs[0] {
		t.Error("the two published aliases must name the same type")
	}
}

// TestFacadeEndToEnd exercises the published surface the way a consumer would:
// extract, start, annotate, end, collect, encode.
func TestFacadeEndToEnd(t *testing.T) {
	recorder := trace.NewRecorder(trace.RecorderConfig{
		Resource: trace.Resource{Attrs: []trace.Attr{trace.String(trace.ServiceNameKey, "checkout")}},
	})
	tracer := trace.NewTracer(trace.TracerConfig{
		Resource: recorder.Resource(),
		Sink:     recorder.Sink(),
	})

	inbound := http.Header{}
	inbound.Set(trace.TraceParentHeader, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	parent := trace.Extract(inbound)
	if !parent.IsValid() || !parent.IsSampled() {
		t.Fatal("Extract did not read the specification's own example header")
	}

	ctx := trace.ContextWithSpanContext(context.Background(), parent)
	ctx, span := tracer.Start(ctx, "GET", trace.SpanParams{
		Kind:  trace.KindServer,
		Attrs: []trace.Attr{trace.String(trace.URLPathKey, "/orders/42")},
	})
	if trace.SpanContextFromContext(ctx).SpanID != span.SpanContext().SpanID {
		t.Error("the returned context does not carry the span that was started")
	}
	trace.RecordError(span, trace.OTLPPartialSuccess)
	span.End()

	outbound := http.Header{}
	trace.Inject(span.SpanContext(), outbound)
	if outbound.Get(trace.TraceParentHeader) == "" {
		t.Error("Inject wrote no traceparent for a valid context")
	}

	batch := recorder.Collect()
	if len(batch.Spans) != 1 {
		t.Fatalf("collected %d spans, want 1", len(batch.Spans))
	}
	if batch.Spans[0].Status.Code != trace.StatusError {
		t.Error("RecordError did not mark the span ERROR through the facade")
	}
	doc, err := trace.EncodeOTLPJSON(batch)
	if err != nil {
		t.Fatalf("EncodeOTLPJSON: %v", err)
	}
	if len(doc) == 0 {
		t.Error("EncodeOTLPJSON produced no bytes")
	}
}

// TestFacadeRefusesTheAmbiguousRatio pins the ADR 0031 answer at the public edge,
// which is the only place a consumer meets it.
func TestFacadeRefusesTheAmbiguousRatio(t *testing.T) {
	if _, err := trace.Ratio(0); !errors.Is(err, trace.InvalidSampleRatio) {
		t.Fatalf("trace.Ratio(0) must be refused, got %v", err)
	}
	if _, err := trace.Ratio(0.5); err != nil {
		t.Fatalf("trace.Ratio(0.5): %v", err)
	}
}

// TestFacadeRefusesAnEndpointWithoutAPath pins the construction-time refusal a
// consumer will actually hit: a bare host connects, answers 404 and looks exactly
// like a collector that is up.
func TestFacadeRefusesAnEndpointWithoutAPath(t *testing.T) {
	if _, err := trace.NewOTLPHTTPExporter("otlphttp", trace.OTLPHTTPConfig{Endpoint: "http://collector:4318"}); !errors.Is(err, trace.OTLPEndpointInvalid) {
		t.Fatalf("want OTLPEndpointInvalid, got %v", err)
	}
	exporter, err := trace.NewOTLPHTTPExporter("otlphttp", trace.OTLPHTTPConfig{Endpoint: "http://collector:4318" + trace.OTLPTracesPath})
	if err != nil {
		t.Fatalf("a full endpoint must be accepted: %v", err)
	}
	if exporter.Name() != "otlphttp" {
		t.Errorf("Name = %q, want the configured name", exporter.Name())
	}
}

// tracerHolder is a consumer's own wiring: the tracer kept in a field, which
// needs a type a downstream module can write down.
type tracerHolder struct {
	// tracer is what NewTracer returned, typed by its public name.
	tracer *trace.SDKTracer
}

// TestAConsumerCanNameTheTracerNewTracerReturns pins #259 at the public edge.
// NewTracer returned *svctrace.Tracer, declared under internal/: a consumer
// could call it and use the result, and could not write its type down, so the
// tracer could not sit in a field of the consumer's own or cross a function
// boundary it declared. This file imports nothing under internal/, so every
// line spelling trace.SDKTracer is half the assertion: without the alias it
// does not compile.
//
// MUTATION (2026-09-29): trace.go was put back as it is on main, with no
// SDKTracer and NewTracer returning *svctrace.Tracer. Observed: `undefined:
// trace.SDKTracer`, on the tracerHolder field; the test package did not build.
// Restored; trace.go is byte-identical to the pre-mutation file by SHA-256.
func TestAConsumerCanNameTheTracerNewTracerReturns(t *testing.T) {
	recorder := trace.NewRecorder(trace.RecorderConfig{
		Resource: trace.Resource{Attrs: []trace.Attr{trace.String(trace.ServiceNameKey, "checkout")}},
	})
	held := tracerHolder{tracer: trace.NewTracer(trace.TracerConfig{
		Resource: recorder.Resource(),
		Scope:    trace.Scope{Name: "example.com/checkout", Version: "1.2.3"},
		Sink:     recorder.Sink(),
	})}

	//: What the concrete type carries beyond the port, read by name: the
	//: scope it stamps on every span is the one it was configured with.
	if got := held.tracer.Scope(); got.Name != "example.com/checkout" || got.Version != "1.2.3" {
		t.Errorf("Scope() = %+v, want the configured scope", got)
	}
	named := false
	//: The resource carries the service name the recorder was given.
	for _, attr := range held.tracer.Resource().Attrs {
		named = named || (attr.Key == trace.ServiceNameKey && attr.Str() == "checkout")
	}
	//: A resource without it would not be the one this tracer was built on.
	if !named {
		t.Errorf("Resource() = %+v, want service.name=checkout", held.tracer.Resource())
	}

	//: And it is still the port a middleware takes, with no conversion.
	var port trace.Tracer = held.tracer
	_, span := port.Start(context.Background(), "charge", trace.SpanParams{})
	span.End()
	//: The span started through the port reached the recorder's sink.
	if got := len(recorder.Collect().Spans); got != 1 {
		t.Fatalf("collected %d spans, want 1", got)
	}
}
