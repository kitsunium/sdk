// Package logger — declares the Encoder port — the format-side
// boundary that mirrors Sink (transport-side). Both ports live in core/
// per hexagonal architecture conventions; concrete adapters live under
// internal/service/logger/encoder/. See ADR 0005.
package logger

// Encoder formats a RecordEvent into a byte slice. Implementations MUST be
// safe for concurrent use by multiple goroutines and SHOULD avoid allocating
// beyond the caller-supplied buffer (typically borrowed from the
// kernel/buffer pool) so the hot path stays zero-allocation.
type Encoder interface {
	// Name returns the canonical encoder identifier ("text", "ndjson",
	// "json"). Useful for diagnostic dumps and metrics tagging.
	Name() (name string)
	// Append serialises r onto dst and returns the (possibly re-allocated)
	// buffer. groups carries the active group prefix stack — encoders are
	// responsible for rendering it as their format-specific prefix
	// ("g1.g2.key" for text, nested objects for JSON).
	Append(dst []byte, groups []string, r RecordEvent) (out []byte)
}
