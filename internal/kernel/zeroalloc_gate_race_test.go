//go:build race

// This file is the race-build counterpart to zeroalloc_gate_test.go. The real
// gate carries //go:build !race (testing.Benchmark reports a spurious +1 alloc
// under the race detector), so under the default race-enabled Bazel config the
// //internal/kernel:kernel_test target would otherwise have zero buildable Go
// sources. This trivial test keeps the target buildable and green under race;
// the actual zero-alloc assertions run in the race-off alloc lane
// (`bazel test --config=alloc` / `make test-alloc`).
package kernel_zeroalloc_test

import "testing"

// TestZeroAllocGateRaceLane documents, in the race build, that the zero-alloc
// invariant is intentionally enforced only in the race-off lane. It performs no
// allocation measurement (those are unreliable under -race) — it simply keeps
// the target compiling and passing so `bazel test --config=ci //...` stays green.
func TestZeroAllocGateRaceLane(t *testing.T) {
	t.Log("zero-alloc assertions run in the race-off alloc lane; see zeroalloc_gate_test.go")
}
