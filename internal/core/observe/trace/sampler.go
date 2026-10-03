// Package trace — the sampling port.
package trace

// Sampler decides whether a span is recorded and exported. It reports true to
// keep the span, false to drop it.
//
// It is a FUNC port, not an interface, applying ADR 0041 structurally: a
// published func type cannot grow a method at all, so there is no widening to
// forbid and no sibling to invent later.
//
// # The decision is taken ONCE, at the root
//
// This is the load-bearing rule of the whole domain and it is not a convention —
// it is what the `sampled` bit of traceparent is FOR. A Tracer consults a
// Sampler only when it is starting a root span; every child, in this process or
// in any process downstream, inherits the answer through
// SpanContextValue.IsSampled.
//
// Deciding per span produces a trace with holes in the middle, and a trace with
// holes is worse than no trace: a span whose parent was dropped becomes an
// orphan a backend renders as its own root, so one request appears as several
// unrelated ones and the latency of the whole is unrecoverable. The failure is
// also invisible at the point that causes it — the process that dropped the
// parent shows nothing wrong at all.
//
// A Sampler is therefore consulted on ROOT spans only. That is a property of the
// Tracer, stated here because this is where a reader will look for it.
//
// # Concurrency
//
// A Sampler MUST be safe for concurrent use and MUST NOT block: it runs inline
// on the goroutine starting the span, before any work the span measures.
type Sampler func(params SamplingParams) bool

// SpanSink receives every span that ends and was sampled. It is where a Tracer
// hands its output, and it is a func port for the reason Sampler is.
//
// It is the seam a batching processor would occupy (ADR 0051 §Deferred): a batch
// is a SpanSink that buffers and forwards, which is why the port is one span at
// a time rather than a slice — a sink that took slices would force every
// non-batching consumer to allocate one.
//
// A SpanSink MUST be safe for concurrent use: spans end on whichever goroutine
// was doing the work. It runs on that goroutine, so a slow sink slows the
// request it is observing.
type SpanSink func(span SpanValue)
