// Package async: config.go holds the Config struct consumed by New. Pulled
// into its own file so async_sink.go stays focused on the Sink contract.
package async

// Config tunes the async sink at construction time. Every field is optional
// — the zero-value Config produces a sink with the documented defaults
// (defaultBufferSize ring, DropNewest policy, no OnDrop callback).
type Config struct {
	// BufferSize is the ring buffer capacity; non-positive falls back to
	// defaultBufferSize (1024 slots).
	BufferSize int
	// Policy selects DropNewest (default) or DropOldest behaviour on saturation.
	Policy DropPolicy
	// OnDrop is invoked for every entry the policy discards. Useful for
	// wiring a metric counter; nil disables the callback.
	OnDrop func(missed int)
}
