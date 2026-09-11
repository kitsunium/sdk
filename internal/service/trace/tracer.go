// Package trace is the concrete tracing implementation behind
// internal/core/trace: a Tracer, three samplers, an in-memory recorder, the
// OTLP/JSON encoder and the OTLP/HTTP emitter — all from the OpenTelemetry and
// W3C specifications, importing nothing from go.opentelemetry.io. See ADR 0051.
package trace

import (
	"context"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"

	coretrace "github.com/kitsunium/sdk/internal/core/trace"
)

// Tracer is the concrete core/trace.Tracer. It holds a resolved configuration
// and no mutable state at all: a span is the only thing that changes, and each
// one owns its own.
//
// That is why there is no lock here and no Collect: the tracer FANS OUT to a
// SpanSink rather than accumulating, so N goroutines starting spans contend on
// nothing in this type. Whatever buffering a deployment wants lives in the sink,
// where its cost is visible.
type Tracer struct {
	// cfg is the resolved configuration — every clamp already applied.
	cfg TracerConfig
}

// NewTracer builds a Tracer from cfg, applying every clamp once.
//
// It returns the concrete type rather than the core/trace.Tracer interface so a
// caller keeps reach to anything the concrete type grows later without the
// interface having to (ADR 0039 §widening a returned value is safe). It cannot
// fail: every field of TracerConfig has a resolved meaning, and the only inputs
// that could be refused — a sampling ratio, an OTLP endpoint — are refused by
// their own constructors, before they ever reach here.
func NewTracer(cfg TracerConfig) *Tracer {
	//: resolve once, so nothing on the per-span path tests a nil field.
	return &Tracer{cfg: cfg.resolved()}
}

// Resource returns the producing resource this Tracer stamps on its payloads.
func (t *Tracer) Resource() coremetrics.ResourceValue {
	//: already normalised at construction.
	return t.cfg.Resource
}

// Scope returns the instrumentation scope this Tracer stamps on its payloads.
func (t *Tracer) Scope() coremetrics.ScopeValue {
	//: already normalised at construction.
	return t.cfg.Scope
}

// Start implements core/trace.Tracer.
//
// The sequence is fixed and each step exists for a stated reason:
//
//  1. The PARENT comes from ctx. A caller never passes one, because a parent
//     passed by hand is a parent that can be the wrong one — the context is
//     already the thing that travels down a call tree.
//  2. The TRACE ID is inherited from a valid parent, or minted. Inheriting is
//     what makes a trace one trace; minting is what starts one.
//  3. The SAMPLER is consulted only when there is no valid parent. A child
//     inherits the sampled bit, so the decision is taken once per trace — see
//     core/trace.Sampler for what re-deciding costs.
//  4. An unsampled span becomes a NO-OP whose context still carries the trace
//     and span ids. Propagation continues, so a downstream service sees the
//     same decision instead of starting a second, contradictory trace.
//
// An empty name panics with InvalidSpanName: it is a literal at the call site,
// so it is wrong on the first call or never.
//
// An entropy failure — crypto/rand refusing — cannot be reported through this
// signature, and the alternative would be a Start that returns an error every
// call site has to handle for a case that has never happened on a working
// kernel. It degrades to a NO-OP span with an invalid context, which propagates
// nothing and records nothing: the trace is lost, the request is not.
func (t *Tracer) Start(ctx context.Context, name string, params coretrace.SpanParams) (child context.Context, span coretrace.Span) {
	//: an unnamed span is a trace nobody can group; fail at the call site.
	if name == "" {
		//: the same refusal an unusable attribute key gets in metrics.
		panic(coretrace.InvalidSpanName.Error())
	}
	//: the parent is whatever this scope carries — extracted from a header
	//: upstream, or started by an outer call.
	parent := coretrace.SpanContextFromContext(ctx)
	//: mint the identity; an entropy fault degrades to the invalid context.
	spanContext, ok := t.mint(parent, name, params)
	//: an unsampled or unmintable span records nothing and costs nothing.
	if !ok || !spanContext.IsSampled() {
		//: the context still travels, so the decision propagates downstream.
		return coretrace.ContextWithSpanContext(ctx, spanContext), noopSpan{context: spanContext}
	}
	//: a recording span owns its own state and its own mutex.
	recording := newSpan(t, spanContext, parent, name, params)
	//: children of this scope become children of this span.
	return coretrace.ContextWithSpanContext(ctx, spanContext), recording
}

// mint builds the new span's context: the trace id, a fresh span id, the
// sampled bit and the inherited tracestate. It reports false when the CSPRNG
// refused, which is the only failure this path has.
func (t *Tracer) mint(parent coretrace.SpanContextValue, name string, params coretrace.SpanParams) (context coretrace.SpanContextValue, ok bool) {
	//: every span has its own id, root or not.
	spanID, spanErr := NewSpanID()
	//: a CSPRNG fault produces the invalid context, which records nothing.
	if spanErr != nil {
		//: degrade rather than fail the caller's request.
		return coretrace.SpanContextValue{}, false
	}
	//: a valid parent hands down the trace id, the flags and the vendor list
	//: unchanged — that inheritance IS the trace.
	if parent.IsValid() {
		//: same trace, same decision, new span.
		return coretrace.SpanContextValue{
			TraceID: parent.TraceID,
			SpanID:  spanID,
			Flags:   parent.Flags,
			State:   parent.State,
		}, true
	}
	//: no parent: this is a root, so a trace id is minted for it.
	traceID, traceErr := NewTraceID()
	//: a CSPRNG fault produces the invalid context, which records nothing.
	if traceErr != nil {
		//: degrade rather than fail the caller's request.
		return coretrace.SpanContextValue{}, false
	}
	//: THE decision, taken exactly here and nowhere else in the trace.
	sampled := t.cfg.Sampler(coretrace.SamplingParams{
		Parent:  parent,
		TraceID: traceID,
		Name:    name,
		Kind:    params.Kind.Resolved(),
		Attrs:   params.Attrs,
		Links:   params.Links,
	})
	//: the verdict rides in the flag byte, which is what propagates it.
	return coretrace.SpanContextValue{
		TraceID: traceID,
		SpanID:  spanID,
		Flags:   coretrace.TraceFlags(0).WithSampled(sampled),
	}, true
}
