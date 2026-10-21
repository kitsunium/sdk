package resilience

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// BulkheadWaitConfig parameterises NewBulkheadWait: how many calls run at
// once, how many more may wait for a slot, for how long, and on which
// clock. Its zero value is NewBulkhead(1) — one slot and no waiting —:
// each field's zero is the reject-at-once bulkhead the SDK has always
// had, never a queue nobody sized (ADR 0031).
type BulkheadWaitConfig struct {
	// Max is how many operations run at once. A non-positive Max is 1,
	// as NewBulkhead's limit is.
	Max int
	// Queue is how many calls may wait for a slot beyond the Max
	// running; a call that finds the queue full is rejected at once with
	// BulkheadFull. Zero, or less, is no queue. It bounds the memory
	// and the goroutines a saturated dependency can pin, which an
	// unbounded queue would not.
	Queue int
	// MaxWait is how long a queued call waits for a slot before it is
	// rejected with BulkheadFull. Zero, or less, is no wait. Size it
	// below the caller's own deadline: a wait the caller cannot afford
	// only moves the timeout from this policy to the caller.
	MaxWait time.Duration
	// Clock is what MaxWait is measured on. Nil means clock.System; a
	// ManualClock lets a test run the wait out without sleeping.
	Clock clock.Timed
}
