package queue

import (
	"github.com/kitsunium/sdk/internal/kernel/clock"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// receiptRadix is the base a memory receipt's counter is written in. Base 36
// keeps it short without needing a second alphabet, and nothing ever parses
// it back — a receipt is opaque.
const receiptRadix int = 36

// MemoryConfig configures [NewMemory]: which clock reads its deadlines, and
// what delivery discipline it enforces.
//
// Its Policy's zero value is refused at construction through the SAME guard
// [NewFile] runs, which is what makes this broker an honest double for that
// one.
type MemoryConfig struct {
	// Clock reads time. A memory broker never WAITS — expiry is noticed on
	// the next Receive — so it asks for the narrow half of kernel/clock and
	// a test can drive every deadline in this package with a ManualClock and
	// no sleeping at all.
	//
	// Nil means clock.System.
	Clock clock.Clock
	// Random draws the retry jitter [corequeue.PolicyValue].RetryJitter asks
	// for: a number in [0, 1), as math/rand/v2.Float64 returns. Nil means
	// math/rand/v2.Float64. A test injects a fixed sequence and asserts every
	// retry instant to the nanosecond, as it does without a jitter; the
	// three brokers draw from it identically, so the memory broker stays an
	// exact double. It may be called by several goroutines at once.
	Random func() float64
	// Policy is the delivery discipline. Its zero value is refused; see
	// core/data/queue.PolicyValue.
	Policy corequeue.PolicyValue
}

// clockOrSystem resolves a nil clock to the wall clock.
func clockOrSystem(clk clock.Clock) clock.Clock {
	//: nil is the caller who has no opinion, which is the common case.
	if clk == nil {
		//: the production value.
		return clock.System
	}
	//: the caller's, usually a ManualClock in a test.
	return clk
}
