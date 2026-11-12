// Package memlimit — Derive reads what Apply would install and installs
// nothing (#809).
package memlimit

import (
	"runtime/debug"
	"testing"
)

// TestDerive_installsNothing pins Derive's one difference from Apply: the
// runtime's soft limit is left as it was.
func TestDerive_installsNothing(t *testing.T) {
	before := debug.SetMemoryLimit(-1)
	_ = Derive()
	if after := debug.SetMemoryLimit(-1); after != before {
		t.Fatalf("Derive changed the soft limit from %d to %d", before, after)
	}
}

// TestDerive_allowanceForTheLoopSlab pins the half of #809's chain this
// package owns: a cgroup v2 memory.max of 512 MiB read through the injected
// readFile is an Allowance of 512 MiB, which net/loop's slabBudgetFor turns
// into 32 MiB a loop for four loops (its TestSlabBudgetFor).
func TestDerive_allowanceForTheLoopSlab(t *testing.T) {
	t.Parallel()
	got := deriveWith(defaultConfig(), noEnv, fakeFS(map[string]string{"/sys/fs/cgroup/memory.max": "536870912\n"}))
	if got.Allowance != 512<<20 {
		t.Fatalf("Allowance = %d (%s), want %d", got.Allowance, got.Source, 512<<20)
	}
}
