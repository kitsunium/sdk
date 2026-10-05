package trace

import (
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
)

// DefaultMaxSpans is the bound a Recorder uses when RecorderConfig leaves the
// knob unset. 2048 finished spans is roughly one busy second of a single
// service: high enough that a normal collection interval never reaches it, low
// enough that a collector which stopped collecting costs megabytes rather than
// the process.
const DefaultMaxSpans int = 2048

// NewRecorder builds a Recorder from cfg, applying every clamp once.
func NewRecorder(cfg RecorderConfig) *Recorder {
	//: a non-positive bound is not "unbounded" (ADR 0031).
	maxSpans := cfg.MaxSpans
	//: clamp to the documented default.
	if maxSpans <= 0 {
		//: the resolved bound.
		maxSpans = DefaultMaxSpans
	}
	//: resource and scope are normalised once, exactly as a Meter does.
	return &Recorder{
		resource: coretrace.NormalizeResource(cfg.Resource),
		scope:    coretrace.NormalizeScope(cfg.Scope),
		maxSpans: maxSpans,
	}
}

// Resource returns the producing resource this Recorder stamps on every payload,
// already normalised. It exists so a Tracer and its Recorder can be given ONE
// resource literal rather than two that a later edit could let drift apart.
func (r *Recorder) Resource() coreotel.ResourceValue {
	//: normalised at construction.
	return r.resource
}

// Scope returns the instrumentation scope this Recorder stamps on every payload.
func (r *Recorder) Scope() coreotel.ScopeValue {
	//: normalised at construction.
	return r.scope
}

// sink is Recorder.Sink's body: decl_gen.go writes Recorder.Sink, from the
// design, as one call of it.
func (r *Recorder) sink() coretrace.SpanSink {
	//: a bound method value, allocated once per call rather than per span.
	return r.record
}

// record is the SpanSink itself.
func (r *Recorder) record(span coretrace.SpanValue) {
	//: the buffer is shared by every goroutine that ends a span.
	r.mu.Lock()
	defer r.mu.Unlock()
	//: past the bound, the newest span is refused and counted.
	if len(r.spans) >= r.maxSpans {
		//: visible overflow, never silent loss.
		r.dropped++
		//: nothing is stored.
		return
	}
	//: kept, in the order it ended.
	r.spans = append(r.spans, span)
}

// Collect DRAINS the recorder and returns everything it held.
//
// Draining, not copying, and the verb is chosen for the same reason ADR 0025
// named a cache read `Fetch`: a read here MUTATES. Two independent collectors
// would each carry away part of the spans, exactly as two readers of a delta
// meter each carry away part of the observations (ADR 0044 §Decision 3). A
// Recorder has ONE reader, and this comment is where a second one finds out.
//
// The returned payload owns its slice, so the caller may hold it across the next
// collection interval.
func (r *Recorder) Collect() coretrace.SpansValue {
	//: swap the buffer out under the lock.
	r.mu.Lock()
	drained := r.spans
	//: a fresh nil rather than a truncated reuse: the caller now owns the old
	//: array, and appending into it would write into a payload they hold.
	r.spans = nil
	r.mu.Unlock()
	//: the payload an exporter walks without regrouping.
	return coretrace.SpansValue{Resource: r.resource, Scope: r.scope, Spans: drained}
}

// Len reports how many spans are waiting to be collected.
func (r *Recorder) Len() int {
	//: a plain read still takes a lock — the slice header is not atomic — and
	//: an exclusive one, because a shared one made the spans that are ending
	//: 9x more expensive. See the mu field comment.
	r.mu.Lock()
	defer r.mu.Unlock()
	//: the pending count.
	return len(r.spans)
}

// Dropped reports how many spans have been refused since construction, across
// every collection interval. It is cumulative — see the field comment.
func (r *Recorder) Dropped() uint64 {
	//: an exclusive read, for the reason Len takes one.
	r.mu.Lock()
	defer r.mu.Unlock()
	//: the cumulative overflow count.
	return r.dropped
}
