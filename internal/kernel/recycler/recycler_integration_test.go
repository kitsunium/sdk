//go:build !race

package recycler_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/buffer"
	"github.com/kitsunium/sdk/internal/kernel/recycler"
)

// allocSink defeats dead-code elimination in the AllocsPerRun probes.
var allocSink any

// TestZeroAllocInvariant pins the documented zero-alloc steady-state hot paths
// for the recycler primitives and the byte-pool specialisation built on them.
// Carries //go:build !race (testing.AllocsPerRun reports +1 under -race) and no
// t.Parallel (AllocsPerRun reads a process-global counter, so concurrent
// allocations from sibling subtests would corrupt the measurement). Run via
// `bazel test --config=alloc //internal/kernel/recycler:recycler_test` or
// `--config=pure`.
func TestZeroAllocInvariant(t *testing.T) {
	rec := recycler.NewRecycler[*[64]byte](func() *[64]byte { return &[64]byte{} })
	capped := recycler.NewCappedRecycler[*[]byte](
		func() *[]byte { return new(make([]byte, 0, 1024)) },
		func(b *[]byte) { *b = (*b)[:0] },
		func(b *[]byte) int { return cap(*b) },
		64<<10,
	)
	type tc struct {
		name string
		fn   func()
	}
	tests := []tc{
		{"Recycler Get+Put", func() { v := rec.Get(); allocSink = v; rec.Put(v) }},
		{"CappedRecycler Get+Put", func() { v := capped.Get(); allocSink = v; capped.Put(v) }},
		{"buffer Get+Put", func() { b := buffer.Get(); allocSink = b; buffer.Put(b) }},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: warm the per-P pool so the measured runs hit the cache path.
		tc.fn()
		//: steady-state hot path must allocate nothing per op.
		if got := testing.AllocsPerRun(1000, tc.fn); got != 0 {
			t.Errorf("%s: %.1f allocs/op, want 0", tc.name, got)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { runCase(t, tc) })
	}
}
