//go:build !race

package consensus

import (
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// appliedEntries is how many entries the window applies.
const appliedEntries int = 4096 + 512

// TestApplied1KeepsAResultWithoutAllocating is the alloc lane's budget of
// #1109: with a forward outstanding, applied1 keeps every result in its
// ring slot, past the ring's length too, and allocates nothing for a
// command of a nil result. Before, each entry wrote the results map and
// appended to resultOrder, which grew and was resliced past 4 096.
func TestApplied1KeepsAResultWithoutAllocating(t *testing.T) {
	n := bareApplyNode()
	n.keepResults = 1
	n.results = new([resultRing]applyResult)
	e := coreconsensus.EntryValue{Position: coreconsensus.PositionValue{Term: 1}}
	got := mallocsOver(appliedEntries, func() {
		e.Position.Index++
		n.applied1(&e, nil)
	})
	reportWindow(t, "applied1", got)
	if got.own != 0 {
		t.Errorf("%d entries applied with a forward outstanding performed %d allocations, want 0", appliedEntries, got.own)
	}
}
