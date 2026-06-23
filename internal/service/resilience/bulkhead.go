// Package resilience — bulkhead (bounded-concurrency) policy.
package resilience

import (
	"context"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
)

// bulkhead caps the number of in-flight operations via a buffered-channel
// semaphore; calls beyond the limit are rejected fast with BulkheadFull.
type bulkhead struct {
	slots chan struct{}
}

// NewBulkhead returns a Runner that admits at most maxConcurrent simultaneous
// operations (clamped to >= 1). Excess calls return BulkheadFull immediately
// (reject mode — no queuing).
func NewBulkhead(maxConcurrent int) coreres.Runner {
	//: at least one slot so the runner always admits a lone caller.
	if maxConcurrent < 1 {
		//: clamp non-positive limits to a single slot.
		maxConcurrent = 1
	}
	//: the buffered channel's capacity is the concurrency bound.
	return &bulkhead{slots: make(chan struct{}, maxConcurrent)}
}

// Run admits op if a slot is free, else rejects with BulkheadFull.
func (b *bulkhead) Run(ctx context.Context, op coreres.Operation) error {
	//: try to claim a slot without blocking.
	select {
	case b.slots <- struct{}{}:
		//: release the slot once the operation returns.
		defer func() { <-b.slots }()
		//: run under the held slot.
		return op(ctx)
	default:
		//: every slot occupied — reject fast.
		return wrapAs(coreres.BulkheadFull, nil)
	}
}
