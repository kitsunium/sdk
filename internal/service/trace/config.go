// Package trace — the Tracer's configuration: producing Resource,
// instrumentation Scope, sampling policy, span destination and clock.
package trace

import (
	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
	coretrace "github.com/kitsunium/sdk/internal/core/trace"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// TracerConfig configures a Tracer. Every field has a resolved meaning when
// left unset — none of them is inert (ADR 0031).
type TracerConfig struct {
	// Resource identifies the producer of the telemetry and is carried once
	// per exported payload. An absent service.name is filled with
	// UnknownService, which is what the OpenTelemetry specification mandates
	// rather than a value this SDK invented.
	//
	// It is the SAME type a MeterConfig takes, deliberately: service.name is
	// the key a backend correlates a trace with a metric on, so a process
	// that built two of them could silently fail to correlate at all.
	Resource coremetrics.ResourceValue

	// Scope identifies the instrumentation that starts the spans. An empty
	// Name resolves to DefaultScopeName; Version stays empty when the caller
	// has none, because it is optional in the specification and inventing one
	// would be a claim about code this package cannot see.
	Scope coremetrics.ScopeValue

	// Sampler decides which ROOT traces are recorded. It is consulted for a
	// root span and for nothing else — every child inherits the answer through
	// the traceparent's sampled bit (see core/trace.Sampler).
	//
	// Unset CLAMPS to ParentBased(AlwaysSample), and the clamp is chosen so
	// that the zero value is the SAFE answer rather than the cheap one: a
	// tracer that silently dropped everything would make a misconfiguration
	// look exactly like a healthy quiet service. A deployment that wants none
	// says NeverSample, which is a name a reviewer can grep for.
	Sampler coretrace.Sampler

	// Sink receives every sampled span at End. Unset means the tracer records
	// nothing — which is not an inert policy but the honest one: a Tracer with
	// no destination has nowhere to put a span, and buffering into a slice
	// nobody drains is the memory leak that shape invites.
	//
	// NewRecorder is the in-tree destination; a batching processor would be a
	// Sink that buffers and forwards (ADR 0051 §Deferred).
	Sink coretrace.SpanSink

	// Clock stamps StartTime and EndTime. Unset CLAMPS to clock.System.
	//
	// It is here for the reason every clock in this SDK is: a test that had to
	// wait for wall-clock time to assert a duration is a test that is either
	// slow or flaky, and usually both.
	Clock clock.Clock
}

// resolved returns the configuration a Tracer actually runs on: every clamp
// applied once, at construction, so nothing on the per-span path has to test a
// nil field.
func (c TracerConfig) resolved() TracerConfig {
	//: the specification's own defaults for an unnamed producer and scope.
	resolved := TracerConfig{
		Resource: c.Resource.Normalized(),
		Scope:    coretrace.NormalizeScope(c.Scope),
		Sampler:  c.Sampler,
		Sink:     c.Sink,
		Clock:    c.Clock,
	}
	//: an unnamed policy keeps traces rather than losing them silently.
	if resolved.Sampler == nil {
		//: honour an inbound decision, keep every root.
		resolved.Sampler = ParentBased(AlwaysSample)
	}
	//: a tracer with no destination records nothing rather than accumulating.
	if resolved.Sink == nil {
		//: the explicit no-op, so the per-span path never tests for nil.
		resolved.Sink = discardSpan
	}
	//: an unset clock is the real one.
	if resolved.Clock == nil {
		//: wall-clock time.
		resolved.Clock = clock.System
	}
	//: fully resolved.
	return resolved
}

// discardSpan is the SpanSink a Tracer with no configured destination uses. It
// is a named function rather than a nil check on the hot path, and rather than a
// buffer nobody drains.
func discardSpan(_ coretrace.SpanValue) {
	//: the span is deliberately dropped — this is the destination a Tracer with
	//: no configured Sink writes to, and it costs one call.
}
