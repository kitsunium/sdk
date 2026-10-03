// Package async — holds the Config struct consumed by New. Pulled
// into its own file so async_sink.go stays focused on the Sink contract.
package async

// Config tunes the async sink at construction time. Every field is optional
// — the zero-value Config produces a sink with the documented defaults
// (defaultBufferSize ring, DropNewest policy, no callbacks).
type Config struct {
	// BufferSize is the ring buffer capacity; non-positive falls back to
	// defaultBufferSize (1024 slots).
	BufferSize int
	// Policy selects DropNewest (default) or DropOldest behaviour on saturation.
	Policy DropPolicy
	// OnDrop is invoked for every entry the policy discards because the ring
	// is saturated. Useful for wiring a metric counter; nil disables the
	// callback. Fires for producer-side drops (ring full under Write).
	OnDrop func(missed int)
	// OnError is invoked for every downstream Write failure seen by the
	// drainer goroutine. Async cannot block the producer on a failing
	// downstream, so without this hook those errors are silently lost;
	// the callback gives operators a path to emit a counter or log to a
	// fallback sink. nil disables the callback. Finding #24.
	OnError func(err error)
}
