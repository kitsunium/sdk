package trace_test

import (
	"context"
	"sync"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"

	coretrace "github.com/kitsunium/sdk/internal/core/trace"
	svctrace "github.com/kitsunium/sdk/internal/service/trace"
)

// TestRecorderCollectDrains pins the verb. Collect MUTATES — it is the same
// choice ADR 0025 made when it named a cache read `Fetch`, and the same
// consequence ADR 0044 §Decision 3 states for a delta meter: two independent
// collectors would each carry away part of the spans, so a Recorder has exactly
// one reader.
func TestRecorderCollectDrains(t *testing.T) {
	recorder := svctrace.NewRecorder(svctrace.RecorderConfig{})
	tracer := svctrace.NewTracer(svctrace.TracerConfig{Sink: recorder.Sink()})
	for range 3 {
		_, span := tracer.Start(context.Background(), "op", coretrace.SpanParams{})
		span.End()
	}
	if got := recorder.Len(); got != 3 {
		t.Fatalf("Len = %d, want 3", got)
	}
	first := recorder.Collect()
	if len(first.Spans) != 3 {
		t.Fatalf("Collect returned %d spans, want 3", len(first.Spans))
	}
	second := recorder.Collect()
	if len(second.Spans) != 0 {
		t.Errorf("the second Collect returned %d spans; the first one drained them", len(second.Spans))
	}
	if len(first.Spans) != 3 {
		t.Error("the payload the first Collect handed out was mutated by the second")
	}
}

// TestRecorderStampsResourceAndScopeOncePerPayload pins the OTel model's whole
// reason for having a Resource: service.name on ten thousand spans is ten
// thousand copies of one fact.
func TestRecorderStampsResourceAndScopeOncePerPayload(t *testing.T) {
	recorder := svctrace.NewRecorder(svctrace.RecorderConfig{
		Resource: coremetrics.ResourceValue{Attrs: []coremetrics.AttrValue{
			coremetrics.String(coremetrics.ServiceNameKey, "checkout"),
		}},
	})
	batch := recorder.Collect()
	if len(batch.Resource.Attrs) != 1 || batch.Resource.Attrs[0].Str() != "checkout" {
		t.Errorf("Resource = %+v, want the configured service.name", batch.Resource)
	}
	if batch.Scope.Name != coretrace.DefaultScopeName {
		t.Errorf("Scope.Name = %q, want %q", batch.Scope.Name, coretrace.DefaultScopeName)
	}
	if recorder.Resource().Attrs[0].Str() != "checkout" {
		t.Error("Resource() must hand back the same normalised resource, so a Tracer and its Recorder cannot drift")
	}
}

// TestRecorderFillsAnAbsentServiceName pins the specification's OWN mandate for
// this case, which is what makes it a clamp rather than an invention.
func TestRecorderFillsAnAbsentServiceName(t *testing.T) {
	recorder := svctrace.NewRecorder(svctrace.RecorderConfig{})
	attrs := recorder.Collect().Resource.Attrs
	if len(attrs) != 1 || attrs[0].Key != coremetrics.ServiceNameKey {
		t.Fatalf("Resource attrs = %+v, want a filled service.name", attrs)
	}
	if attrs[0].Str() != "unknown_service" {
		t.Errorf("service.name = %q, want the specification's unknown_service", attrs[0].Str())
	}
}

// TestRecorderOverflowIsCountedNotSilent pins the bound and its counter.
//
// Growing without bound converts a collector outage into an OOM; dropping without
// counting makes an integration that is losing telemetry look exactly like a
// service that is quiet. The NEWEST span is the one dropped, so a burst cannot
// erase the spans that preceded it — the beginning of an incident is more
// informative than its middle.
func TestRecorderOverflowIsCountedNotSilent(t *testing.T) {
	recorder := svctrace.NewRecorder(svctrace.RecorderConfig{MaxSpans: 2})
	tracer := svctrace.NewTracer(svctrace.TracerConfig{Sink: recorder.Sink()})
	names := []string{"first", "second", "third", "fourth"}
	for _, name := range names {
		_, span := tracer.Start(context.Background(), name, coretrace.SpanParams{})
		span.End()
	}
	batch := recorder.Collect()
	if len(batch.Spans) != 2 {
		t.Fatalf("held %d spans, want the bound of 2", len(batch.Spans))
	}
	if batch.Spans[0].Name != "first" || batch.Spans[1].Name != "second" {
		t.Errorf("kept %q and %q; the OLDEST spans must survive a burst", batch.Spans[0].Name, batch.Spans[1].Name)
	}
	if got := recorder.Dropped(); got != 2 {
		t.Errorf("Dropped = %d, want 2 — an overflow that is not counted is a silence", got)
	}
	//: the counter is CUMULATIVE across intervals: a counter that reset on read
	//: could not be scraped by two readers, and this one is meant to be alerted on.
	recorder.Collect()
	if got := recorder.Dropped(); got != 2 {
		t.Errorf("Dropped = %d after a Collect, want the cumulative 2", got)
	}
}

// TestRecorderClampsANonPositiveBound pins ADR 0031: a non-positive MaxSpans is
// not "unbounded", and there is no setting that is. A caller who leaves it zero
// has not decided that memory is free.
func TestRecorderClampsANonPositiveBound(t *testing.T) {
	recorder := svctrace.NewRecorder(svctrace.RecorderConfig{MaxSpans: -1})
	tracer := svctrace.NewTracer(svctrace.TracerConfig{Sink: recorder.Sink()})
	for range svctrace.DefaultMaxSpans + 5 {
		_, span := tracer.Start(context.Background(), "op", coretrace.SpanParams{})
		span.End()
	}
	if got := recorder.Len(); got != svctrace.DefaultMaxSpans {
		t.Errorf("held %d spans, want the clamped default of %d", got, svctrace.DefaultMaxSpans)
	}
	if got := recorder.Dropped(); got != 5 {
		t.Errorf("Dropped = %d, want 5", got)
	}
}

// TestRecorderIsConcurrencySafe exercises the sink from many goroutines, which is
// how spans actually end: on whichever goroutine was doing the work.
func TestRecorderIsConcurrencySafe(t *testing.T) {
	recorder := svctrace.NewRecorder(svctrace.RecorderConfig{})
	tracer := svctrace.NewTracer(svctrace.TracerConfig{Sink: recorder.Sink()})
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 16 {
				_, span := tracer.Start(context.Background(), "op", coretrace.SpanParams{})
				span.End()
			}
		})
	}
	wg.Wait()
	if got := len(recorder.Collect().Spans); got != 256 {
		t.Errorf("collected %d spans, want 256", got)
	}
}

// TestATracerWithNoSinkRecordsNothing pins the honest zero value: a Tracer with no
// destination has nowhere to put a span, and buffering into a slice nobody drains
// is the memory leak that shape invites.
func TestATracerWithNoSinkRecordsNothing(t *testing.T) {
	tracer := svctrace.NewTracer(svctrace.TracerConfig{})
	_, span := tracer.Start(context.Background(), "op", coretrace.SpanParams{})
	//: it must not panic, and the span must still carry a usable context.
	span.End()
	if !span.SpanContext().IsValid() {
		t.Error("a sink-less tracer must still mint a propagatable context")
	}
}
