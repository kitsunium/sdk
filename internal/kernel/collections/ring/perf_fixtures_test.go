//go:build !race

// The fixtures of ring's performance contracts (design/sdk.yaml, budgets):
// each sets one call up and returns it, and the perf_gen_test.go kit gen
// writes beside this file counts its allocations against its budget.
//
// They were the ring half of the kernel's zero-allocation gate
// (internal/kernel/zeroalloc_gate_integration_test.go), four probes, bound
// 0: a happy TryWrite, a happy TryRead, a TryWrite on a full ring (the
// pre-allocated Full sentinel) and a TryRead on an empty one (Empty plus
// the zero value). Each budget's call walks both of its method's paths, so
// the four probes are two calls; a sum of zeros stays zero, and a path that
// allocates shows. The budget names the port's method (Queue.TryWrite,
// Queue.TryRead), since the ring's concrete type is unexported. No consumer
// goroutine: each call writes then drains, so the ring is used SPSC by one
// goroutine and never saturates on its happy path. Race off.
package ring_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/collections/ring"
)

// The sinks keep each call's results alive.
var (
	perfInt int
	perfErr error
)

// perfQueue is a ring of the given capacity, or the fixture fails.
func perfQueue(tb testing.TB, capacity int) ring.Queue[int] {
	tb.Helper()
	q, err := ring.New[int](capacity)
	//: a capacity New refuses leaves the fixture nothing to measure.
	if err != nil {
		tb.Fatalf("New(%d): %v", capacity, err)
	}
	return q
}

// perfTryWrite is Queue.TryWrite's fixture: a write that lands, drained at
// once, then a write into a full ring, which returns Full.
func perfTryWrite(tb testing.TB) func() {
	q, full := perfQueue(tb, 1024), perfQueue(tb, 8)
	//: fill the ring to capacity, so the call's second write returns Full.
	for full.TryWrite(0) == nil {
	}
	return func() {
		perfErr = q.TryWrite(1)
		perfInt, perfErr = q.TryRead()
		perfErr = full.TryWrite(1)
	}
}

// perfTryRead is Queue.TryRead's fixture: a read of an item just written,
// then a read of an empty ring, which returns Empty.
func perfTryRead(tb testing.TB) func() {
	q, empty := perfQueue(tb, 1024), perfQueue(tb, 8)
	return func() {
		perfErr = q.TryWrite(1)
		perfInt, perfErr = q.TryRead()
		perfInt, perfErr = empty.TryRead()
	}
}
