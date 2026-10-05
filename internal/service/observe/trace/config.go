package trace

import (
	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// resolved returns the configuration a Tracer actually runs on: every clamp
// applied once, at construction, so nothing on the per-span path has to test a
// nil field.
func (c TracerConfig) resolved() TracerConfig {
	//: the specification's own defaults for an unnamed producer and scope.
	resolved := TracerConfig{
		Resource: coretrace.NormalizeResource(c.Resource),
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
