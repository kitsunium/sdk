//go:build !race

// The fixture of buffer's performance contract (design/sdk.yaml, budgets):
// it sets one call up and returns it, and the perf_gen_test.go kit gen
// writes beside this file counts its allocations against its budget.
//
// It was held twice, bound 0 both times: by the kernel's zero-allocation
// gate (internal/kernel/zeroalloc_gate_integration_test.go) and by the
// "buffer Get+Put" arm of the recycler's (TestZeroAllocInvariant in
// internal/kernel/concur/recycler). A warm-pool round trip must allocate
// nothing at all, not "nothing on average": a Put that appended to an
// instrument slice ("how many buffers are out?") allocated 10 times over
// 1 000 round trips, which testing.AllocsPerRun read as 0, and a total
// sees. Race off.
package buffer_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/concur/buffer"
)

// perfBuf keeps the borrowed buffer alive.
var perfBuf *[]byte

// perfGetPut is Get's fixture: a borrow and its return, the pool warmed
// first so the factory is never the call measured.
func perfGetPut(testing.TB) func() {
	buffer.Put(buffer.Get())
	return func() {
		b := buffer.Get()
		perfBuf = b
		buffer.Put(b)
	}
}
