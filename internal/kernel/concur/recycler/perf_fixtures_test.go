//go:build !race

// The fixtures of recycler's performance contracts (design/sdk.yaml,
// budgets): each sets one call up and returns it, and the perf_gen_test.go
// kit gen writes beside this file counts its allocations against its
// budget — the total over 30 000 calls after as many warm-up calls.
//
// They were TestZeroAllocInvariant (recycler_integration_test.go), bound 0:
// a steady-state Get+Put round trip on a Pool and on a CappedPool (its
// third arm, buffer's, is buffer's own contract now).
//
// MUTATION-CHECKED there, and the reason the bound is a total: giving
// Pool[T] a `returned []T` field and appending v to it inside Put — the
// shape of every "how many buffers are in flight?" instrument — failed both
// arms at once (CappedPool delegates its Put to Pool), at 10 allocations
// over 1 000 round trips: ten, not a thousand, because the slice doubles.
// testing.AllocsPerRun read the same mutated round trips as 0, three runs
// out of three. The generated window counts as many calls as the warm-up
// ran, so such a slice crosses one of its growth steps inside it.
//
// The pool is warmed and the collector held off by the generated test: a
// collection drains sync.Pool's victim cache, which would turn the next Get
// into a factory call and measure the cold path these contracts exist to
// stay off. Race off.
package recycler_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/concur/recycler"
)

// perfSink keeps the borrowed values alive.
var perfSink any

// perfPoolGetPut is (*Pool).Get's fixture: a borrow from a warm Pool and
// its return.
func perfPoolGetPut(testing.TB) func() {
	p := recycler.NewPool[*[64]byte](func() *[64]byte { return &[64]byte{} })
	return func() {
		v := p.Get()
		perfSink = v
		p.Put(v)
	}
}

// perfCappedPoolGetPut is (*CappedPool).Get's fixture: a borrow from a warm
// CappedPool, under its cap, and its return.
func perfCappedPoolGetPut(testing.TB) func() {
	p := recycler.NewCappedPool[*[]byte](
		func() *[]byte { return new(make([]byte, 0, 1024)) },
		func(b *[]byte) { *b = (*b)[:0] },
		func(b *[]byte) int { return cap(*b) },
		64<<10,
	)
	return func() {
		v := p.Get()
		perfSink = v
		p.Put(v)
	}
}
