package consensus

import (
	"runtime"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// BenchmarkApplied1WithAForwardOutstanding measures applied1 for a command
// of a nil result while a forward is outstanding (keepResults > 0) and one
// caller is parked far ahead: one ring store and one look at the queue's
// head, where the map, the order slice and the Broadcast it replaced cost a
// map write, an append and, past 4 096 entries, a delete per entry (#1109).
func BenchmarkApplied1WithAForwardOutstanding(b *testing.B) {
	n := bareApplyNode()
	n.keepResults = 1
	n.results = new([resultRing]applyResult)
	done := make(chan struct{})
	go n.waitApplied(done, 1<<62)
	for parked(n) != 1 {
		runtime.Gosched()
	}
	b.ReportAllocs()
	e := coreconsensus.EntryValue{Position: coreconsensus.PositionValue{Term: 1}}
	for b.Loop() {
		e.Position.Index++
		n.applied1(&e, nil)
	}
	close(done)
}
