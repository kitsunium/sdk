//go:build !race

// The fixtures of snapshot's performance contracts (design/sdk.yaml,
// budgets): each sets one call up and returns it, and the perf_gen_test.go
// kit gen writes beside this file counts its allocations against its
// budget — the total over 30 000 calls after as many warm-up calls.
//
// They were TestZeroAllocInvariant (snapshot_integration_test.go), bound 0:
// Load, Store and Swap of a pre-built pointer allocate nothing (only a
// writer's own clone allocates, and that is the caller's).
//
// MUTATION-CHECKED there: giving Value[T] a `published []*T` field and
// appending next to it inside Store — the first thing anyone reaches for to
// ask which snapshots a container served — failed at `Store: 1000 calls
// performed 10 allocations, want 0`: ten, not a thousand, the slice
// doubling. testing.AllocsPerRun read the same Store as 0, three runs out
// of three. Race off.
package snapshot_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/concur/snapshot"
)

// perfPtr keeps a loaded or swapped snapshot alive.
var perfPtr *int

// perfValue is a container holding a, and b, the pointer a writer stores.
func perfValue() (*snapshot.Value[int], *int, *int) {
	a, b := 1, 2
	return snapshot.NewValue(&a), &a, &b
}

// perfLoad is (*Value).Load's fixture.
func perfLoad(testing.TB) func() {
	v, _, _ := perfValue()
	return func() { perfPtr = v.Load() }
}

// perfStore is (*Value).Store's fixture: a pre-built pointer published.
func perfStore(testing.TB) func() {
	v, _, b := perfValue()
	return func() { v.Store(b) }
}

// perfSwap is (*Value).Swap's fixture: a pre-built pointer swapped in.
func perfSwap(testing.TB) func() {
	v, a, _ := perfValue()
	return func() { perfPtr = v.Swap(a) }
}
