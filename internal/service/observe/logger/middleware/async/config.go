package async

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// Config tunes the async sink at construction time. Every field is optional
// — the zero-value Config produces a sink with the documented defaults
// (defaultBufferSize ring, DropNewest policy, no callbacks).
type Config struct {
	// BufferSize is the ring buffer capacity; non-positive falls back to
	// defaultBufferSize (1024 slots).
	BufferSize int
	// Policy selects DropNewest (default), DropOldest or Block behaviour on saturation.
	Policy DropPolicy
	// MaxBlock bounds how long a Write waits for a free slot under the Block
	// policy; non-positive falls back to DefaultMaxBlock (one second), never to
	// an unbounded wait (ADR 0031). Ignored by the two drop policies.
	MaxBlock time.Duration
	// Clock drives the MaxBlock wait; nil falls back to clock.System. A test
	// injects a ManualClock and waits on nothing.
	Clock clock.Timed
	// OnDrop is invoked for every entry the policy discards because the ring
	// is saturated. Useful for wiring a metric counter; nil disables the
	// callback. Fires for producer-side drops (ring full under Write).
	OnDrop func(missed int)
	// BatchMax is how many queued records the drainer hands the downstream
	// at once. When it is above one and the downstream implements
	// corelogger.BatchSink, the drainer takes up to BatchMax records off the
	// ring and delivers them with one WriteBatch — one write(2) for a file
	// sink instead of one per record; a downstream that does not implement
	// it still receives one Write per record. Non-positive means one, the
	// record-at-a-time delivery of a zero Config.
	BatchMax int
	// OnError is invoked for every downstream Write failure seen by the
	// drainer goroutine. Async cannot block the producer on a failing
	// downstream, so without this hook those errors are silently lost;
	// the callback gives operators a path to emit a counter or log to a
	// fallback sink. nil disables the callback. Finding #24. A failed
	// WriteBatch fires it once per record the batch did not deliver.
	OnError func(err error)
}
