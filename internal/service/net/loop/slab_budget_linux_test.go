//go:build linux

// Package loop — the slab budget a group resolved, in State (#809).
package loop

import (
	"io"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// TestSlabBudget_inState pins the resolved budget and its source in State:
// Start given an allowance of 512 MiB and four loops reports 32 MiB a loop.
func TestSlabBudget_inState(t *testing.T) {
	g, err := Start(Config{
		Group: "g", Value: corenet.LoopValue{Loops: 4, Backend: corenet.LoopBackendEpoll},
		Memory:  coreproc.MemoryLimitValue{Allowance: 512 << 20, Source: coreproc.MemorySourceCgroup},
		Handler: &lineH{}, Log: io.Discard,
	})
	if err != nil {
		t.Skipf("no native backend here: %v", err)
	}
	defer g.Close()
	st := g.State()
	if st.SlabBudget != 4*32<<20 || st.SlabBudgetSource != slabDerived {
		t.Fatalf("SlabBudget %d %q, want %d derived", st.SlabBudget, st.SlabBudgetSource, 4*32<<20)
	}
}
