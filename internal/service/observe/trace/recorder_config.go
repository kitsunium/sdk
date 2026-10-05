package trace

import coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"

// RecorderConfig configures a Recorder: who produced the spans, what
// instrumented them, and how many may wait between collections.
type RecorderConfig struct {
	// Resource identifies the producer, stamped on every payload Collect
	// hands out.
	Resource coreotel.ResourceValue
	// Scope identifies the instrumentation, stamped on every payload.
	Scope coreotel.ScopeValue
	// MaxSpans bounds how many finished spans are held between Collects.
	//
	// Non-positive CLAMPS to DefaultMaxSpans. It does NOT mean "unbounded",
	// and there is no setting that does (ADR 0031): a buffer nobody drains is
	// how a tracing integration takes a process down, and the zero value must
	// not be the dangerous answer.
	MaxSpans int
}
