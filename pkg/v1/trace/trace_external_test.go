package trace_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/metrics"
	"github.com/kitsunium/sdk/pkg/v1/trace"
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
