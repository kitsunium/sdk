// Package trace — the in-memory recorder's configuration.
package trace

import coremetrics "github.com/kitsunium/sdk/internal/core/metrics"

// RecorderConfig configures a Recorder: who produced the spans, what
// instrumented them, and how many may wait between collections.
type RecorderConfig struct {
	// Resource identifies the producer, stamped on every payload Collect
	// hands out.
	Resource coremetrics.ResourceValue
	// Scope identifies the instrumentation, stamped on every payload.
	Scope coremetrics.ScopeValue
	// MaxSpans bounds how many finished spans are held between Collects.
	//
	// Non-positive CLAMPS to DefaultMaxSpans. It does NOT mean "unbounded",
	// and there is no setting that does (ADR 0031): a buffer nobody drains is
	// how a tracing integration takes a process down, and the zero value must
	// not be the dangerous answer.
	MaxSpans int
}
