package trace_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"

	coretrace "github.com/kitsunium/sdk/internal/core/trace"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	svctrace "github.com/kitsunium/sdk/internal/service/trace"
)

// collector is a SpanSink that records every span it is handed, safely.
type collector struct {
	mu    sync.Mutex
	spans []coretrace.SpanValue
}

// sink returns the core/trace.SpanSink form.
func (c *collector) sink() coretrace.SpanSink {
	return func(span coretrace.SpanValue) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.spans = append(c.spans, span)
	}
}

// list returns a copy of what was collected.
func (c *collector) list() []coretrace.SpanValue {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.spans)
}

// TestSamplerIsConsultedOnceAtTheRoot is the load-bearing test of the whole
// domain.
//
// A trace with holes in it is worse than no trace: a span whose parent was
// dropped becomes an orphan the backend renders as its own root, so one request
// appears as several unrelated ones. The guarantee that prevents it is that the
// Sampler runs for the ROOT and for nothing else — which is what this counts.
func TestSamplerIsConsultedOnceAtTheRoot(t *testing.T) {
	var calls int
	var seen []coretrace.SamplingParams
	sink := &collector{}
	tracer := svctrace.NewTracer(svctrace.TracerConfig{
		Sink: sink.sink(),
		Sampler: func(params coretrace.SamplingParams) bool {
			calls++
			seen = append(seen, params)
			return true
		},
	})
	ctx, root := tracer.Start(context.Background(), "root", coretrace.SpanParams{})
	ctx, child := tracer.Start(ctx, "child", coretrace.SpanParams{})
	_, grandchild := tracer.Start(ctx, "grandchild", coretrace.SpanParams{})
	grandchild.End()
	child.End()
	root.End()

	if calls != 1 {
		t.Fatalf("the sampler ran %d times; it must run exactly once, for the root", calls)
	}
	if seen[0].Name != "root" || seen[0].Parent.IsValid() {
		t.Errorf("the sampler saw %+v; it must see the ROOT, with no parent", seen[0])
	}
	spans := sink.list()
	if len(spans) != 3 {
		t.Fatalf("collected %d spans, want 3", len(spans))
	}
	//: one trace id across all three, and each span's parent is its caller.
	traceID := spans[0].Context.TraceID
	for _, span := range spans {
		if span.Context.TraceID != traceID {
			t.Errorf("%q left the trace: %s != %s", span.Name, span.Context.TraceID, traceID)
		}
		if !span.Context.IsSampled() {
			t.Errorf("%q lost the sampled bit the root decided", span.Name)
		}
	}
	//: spans arrive in END order, so grandchild, child, root.
	if spans[0].Parent.SpanID != spans[1].Context.SpanID {
		t.Error("the grandchild's parent is not the child")
	}
	if spans[1].Parent.SpanID != spans[2].Context.SpanID {
		t.Error("the child's parent is not the root")
	}
	if !spans[2].IsRoot() {
		t.Error("the root must have no parent")
	}
}

// TestAnUnsampledTraceStillPropagates pins why an unsampled span is a noop with a
// CONTEXT rather than nothing at all.
//
// If the context did not travel, every downstream hop would start its own root
// and one dropped trace would become N kept ones — the opposite of sampling.
func TestAnUnsampledTraceStillPropagates(t *testing.T) {
	sink := &collector{}
	tracer := svctrace.NewTracer(svctrace.TracerConfig{Sink: sink.sink(), Sampler: svctrace.NeverSample})
	ctx, root := tracer.Start(context.Background(), "root", coretrace.SpanParams{})
	_, child := tracer.Start(ctx, "child", coretrace.SpanParams{})
	child.End()
	root.End()

	if got := len(sink.list()); got != 0 {
		t.Errorf("an unsampled trace recorded %d spans, want 0", got)
	}
	context := root.SpanContext()
	if !context.IsValid() {
		t.Fatal("an unsampled span must still carry a valid context, so the decision propagates")
	}
	if context.IsSampled() {
		t.Error("the sampled bit must be CLEAR, so the next hop inherits the decision")
	}
	header, ok := coretrace.FormatTraceParent(context)
	if !ok || header[len(header)-2:] != "00" {
		t.Errorf("traceparent = %q (%v), want a header whose flags say not-sampled", header, ok)
	}
	//: the child stayed in the same trace even though nothing was recorded.
	if child.SpanContext().TraceID != context.TraceID {
		t.Error("an unsampled child must stay in its parent's trace")
	}
}

// TestStartInheritsAnExtractedRemoteParent pins the join across a process
// boundary, including the fact that the remote parent's DECISION wins over the
// local sampler.
func TestStartInheritsAnExtractedRemoteParent(t *testing.T) {
	upstream, err := coretrace.ParseTraceParent("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	if err != nil {
		t.Fatalf("ParseTraceParent: %v", err)
	}
	sink := &collector{}
	//: NeverSample as the ROOT policy; ParentBased must never consult it here.
	tracer := svctrace.NewTracer(svctrace.TracerConfig{
		Sink:    sink.sink(),
		Sampler: svctrace.ParentBased(svctrace.NeverSample),
	})
	ctx := coretrace.ContextWithSpanContext(context.Background(), upstream)
	_, span := tracer.Start(ctx, "handle", coretrace.SpanParams{Kind: coretrace.SpanKindServer})
	span.End()

	spans := sink.list()
	if len(spans) != 1 {
		t.Fatalf("collected %d spans, want 1 — the remote parent said sampled", len(spans))
	}
	if spans[0].Context.TraceID != upstream.TraceID {
		t.Error("the span left the upstream's trace")
	}
	if spans[0].Parent.SpanID != upstream.SpanID {
		t.Error("the span's parent is not the upstream span")
	}
	if !spans[0].Parent.Remote {
		t.Error("the parent came from a header, so it must be marked Remote — OTLP carries the fact as a span flag")
	}
}

// TestStartPanicsOnAnEmptyName pins the refusal. A span name is the
// low-cardinality label a backend groups on and it is a literal at the call site,
// so it is wrong on the first call or never — the same argument that makes an
// unusable attribute key a panic in `metrics`.
func TestStartPanicsOnAnEmptyName(t *testing.T) {
	tracer := svctrace.NewTracer(svctrace.TracerConfig{})
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Error("Start with an empty name must panic")
		}
	}()
	tracer.Start(context.Background(), "", coretrace.SpanParams{})
}

// TestEndIsIdempotent pins the guard against the most common instrumentation
// mistake: a `defer span.End()` beside an explicit End on an early return. A
// duplicate span is a duplicate row in every backend, and it is the duplicate
// nobody notices.
func TestEndIsIdempotent(t *testing.T) {
	sink := &collector{}
	tracer := svctrace.NewTracer(svctrace.TracerConfig{Sink: sink.sink()})
	_, span := tracer.Start(context.Background(), "once", coretrace.SpanParams{})
	span.End()
	span.End()
	span.End()
	if got := len(sink.list()); got != 1 {
		t.Errorf("End was called three times and produced %d spans, want 1", got)
	}
}

// TestSetAttrsReplacesOnKey pins that an attribute set stays a SET. Two
// http.response.status_code entries on one span are a payload each backend renders
// differently, and the disagreement surfaces only when two people compare notes.
func TestSetAttrsReplacesOnKey(t *testing.T) {
	sink := &collector{}
	tracer := svctrace.NewTracer(svctrace.TracerConfig{Sink: sink.sink()})
	_, span := tracer.Start(context.Background(), "op", coretrace.SpanParams{
		Attrs: []coremetrics.AttrValue{coremetrics.Int64("status", 200)},
	})
	span.SetAttrs(coremetrics.Int64("status", 503), coremetrics.String("zone", "eu"))
	span.SetAttrs(coremetrics.String("app", "checkout"))
	span.End()

	attrs := sink.list()[0].Attrs
	if len(attrs) != 3 {
		t.Fatalf("attrs = %v, want exactly 3 — a repeated key replaces", attrs)
	}
	//: sorted by key: app, status, zone.
	if attrs[0].Key != "app" || attrs[1].Key != "status" || attrs[2].Key != "zone" {
		t.Errorf("attrs are not sorted by key: %v", attrs)
	}
	if attrs[1].Int64() != 503 {
		t.Errorf("status = %d, want the LAST value written", attrs[1].Int64())
	}
}

// TestWritesAfterEndAreDropped pins the closed-span rule. The value is already on
// its way to an exporter, so a late write would either race with the sink or be
// silently lost; being explicitly ignored is the honest one, and it is what makes
// a stray `defer` in a callback harmless.
func TestWritesAfterEndAreDropped(t *testing.T) {
	sink := &collector{}
	tracer := svctrace.NewTracer(svctrace.TracerConfig{Sink: sink.sink()})
	_, span := tracer.Start(context.Background(), "op", coretrace.SpanParams{})
	span.End()
	span.SetAttrs(coremetrics.String("late", "yes"))
	span.AddEvent("late")
	span.SetStatus(coretrace.StatusError, "late")

	recorded := sink.list()[0]
	if len(recorded.Attrs) != 0 || len(recorded.Events) != 0 || recorded.Status.Code != coretrace.StatusUnset {
		t.Errorf("a write after End reached the exported span: %+v", recorded)
	}
}

// TestSpanUsesTheConfiguredClock pins the seam that keeps duration assertions off
// the wall clock — a test that waited for real time to pass is either slow or
// flaky, and usually both.
func TestSpanUsesTheConfiguredClock(t *testing.T) {
	manual := clock.NewManualClock(time.Unix(0, 1_700_000_000_000_000_000))
	sink := &collector{}
	tracer := svctrace.NewTracer(svctrace.TracerConfig{Sink: sink.sink(), Clock: manual})
	_, span := tracer.Start(context.Background(), "op", coretrace.SpanParams{})
	manual.Advance(250 * time.Millisecond)
	span.AddEvent("halfway")
	manual.Advance(250 * time.Millisecond)
	span.End()

	recorded := sink.list()[0]
	if got := recorded.Duration(); got != 500*time.Millisecond {
		t.Errorf("Duration = %v, want 500ms from the manual clock", got)
	}
	if len(recorded.Events) != 1 || !recorded.Events[0].Time.Equal(time.Unix(0, 1_700_000_000_250_000_000)) {
		t.Errorf("the event was not stamped by the configured clock: %+v", recorded.Events)
	}
}

// TestStartTimeOverrideIsHonoured pins the ADR 0031 clamp on SpanParams.StartTime:
// the zero value means "now" (a description of what the tracer does), and a
// supplied instant is used exactly as given.
func TestStartTimeOverrideIsHonoured(t *testing.T) {
	replayed := time.Unix(0, 1_600_000_000_000_000_000)
	sink := &collector{}
	tracer := svctrace.NewTracer(svctrace.TracerConfig{Sink: sink.sink()})
	_, span := tracer.Start(context.Background(), "replay", coretrace.SpanParams{StartTime: replayed})
	span.End()
	if got := sink.list()[0].StartTime; !got.Equal(replayed) {
		t.Errorf("StartTime = %v, want the supplied instant %v", got, replayed)
	}
}

// TestInvalidLinksAreDroppedRatherThanExported pins the interaction between the
// model and the encoder: a link naming no span would encode as an all-zero
// trace-id, which the OTLP encoder refuses outright — so keeping it would turn one
// bad link into a whole payload nobody can export.
func TestInvalidLinksAreDroppedRatherThanExported(t *testing.T) {
	sink := &collector{}
	tracer := svctrace.NewTracer(svctrace.TracerConfig{Sink: sink.sink()})
	valid, err := coretrace.ParseTraceParent("00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	if err != nil {
		t.Fatalf("ParseTraceParent: %v", err)
	}
	_, span := tracer.Start(context.Background(), "batch", coretrace.SpanParams{
		Links: []coretrace.LinkValue{{Context: coretrace.SpanContextValue{}}, {Context: valid}},
	})
	span.End()
	links := sink.list()[0].Links
	if len(links) != 1 || links[0].Context.SpanID != valid.SpanID {
		t.Errorf("links = %+v, want only the valid one", links)
	}
}

// TestConcurrentAnnotationIsSafe exercises the mutex under the race detector,
// which is on by default in this repo's test lane.
func TestConcurrentAnnotationIsSafe(t *testing.T) {
	sink := &collector{}
	tracer := svctrace.NewTracer(svctrace.TracerConfig{Sink: sink.sink()})
	_, span := tracer.Start(context.Background(), "fanout", coretrace.SpanParams{})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			span.SetAttrs(coremetrics.Int64("worker", int64(i)))
			span.AddEvent("step")
			span.SetStatus(coretrace.StatusOK, "")
		})
	}
	wg.Wait()
	span.End()
	recorded := sink.list()[0]
	if len(recorded.Events) != 8 {
		t.Errorf("events = %d, want 8", len(recorded.Events))
	}
	if len(recorded.Attrs) != 1 {
		t.Errorf("attrs = %d, want 1 — every worker wrote the same key", len(recorded.Attrs))
	}
}

// TestTheValueASinkReceivesIsFinal is the safety net under the ownership
// TRANSFER in finish().
//
// The exported SpanValue no longer CLONES the span's attribute and event
// arrays; the span hands them over and nils its own references, which is the
// same "exactly one holder" guarantee for one allocation and 240 B less per
// sampled span (BENCH.md §1.4). What makes that safe is a conjunction, and the
// conjunction is what this test pins: every write site returns on `s.ended`
// under the same mutex finish sets it with, AND finish drops the references. A
// reader who later allows a post-End write has to defeat BOTH.
//
// The status attribute is set through a SEPARATE SetAttrs call on purpose, so
// the span's array carries the spare capacity slices.Insert leaves behind. A
// late in-place replacement writes into that spare array — which is exactly the
// tear this test would have to observe, and it cannot be provoked at all on a
// span whose attributes all arrived at Start.
//
// MUTATION, and it has to be a compound one: removing the `if s.ended { return }`
// guard from SetAttrs ALONE leaves the test passing, because the nil makes the
// binary search miss and slices.Insert builds a fresh array the exported value
// never sees. Removing the nil alone leaves it passing too, because the guard
// still turns the write away. Both must go. With both removed, observed:
// "the exported value's attrs changed after the span ended: got MUTATED at
// http.request.method, want GET". Restored; span.go is byte-identical to its
// intended form and the test passes again.
func TestTheValueASinkReceivesIsFinal(t *testing.T) {
	var captured coretrace.SpanValue
	tracer := svctrace.NewTracer(svctrace.TracerConfig{
		Sampler: svctrace.AlwaysSample,
		Sink:    func(value coretrace.SpanValue) { captured = value },
	})
	_, span := tracer.Start(context.Background(), "GET", coretrace.SpanParams{
		Kind: coretrace.SpanKindServer,
		Attrs: []coremetrics.AttrValue{
			coremetrics.String("http.request.method", "GET"),
			coremetrics.String("url.path", "/v1/orders/42"),
			coremetrics.String("url.scheme", "https"),
			coremetrics.String("server.address", "api.example.com"),
		},
	})
	//: a separate call, so the array grows and keeps spare capacity.
	span.SetAttrs(coremetrics.Int64("http.response.status_code", 200))
	span.End()
	if len(captured.Attrs) != 5 {
		t.Fatalf("the sink received %d attributes, want 5", len(captured.Attrs))
	}
	//: everything below happens to a span that has already been exported.
	span.SetAttrs(coremetrics.String("http.request.method", "MUTATED"))
	span.AddEvent("late")
	span.SetStatus(coretrace.StatusError, "late")
	for _, attr := range captured.Attrs {
		if attr.Key == "http.request.method" && attr.Str() != "GET" {
			t.Errorf("the exported value attrs changed after the span ended: got %s at %s, want GET", attr.Str(), attr.Key)
		}
	}
	if len(captured.Events) != 0 {
		t.Errorf("the exported value gained %d events after the span ended", len(captured.Events))
	}
}
