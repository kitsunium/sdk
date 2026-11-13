package consensus

import (
	"fmt"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// BenchmarkALeaderTurnPerFollower measures what a leader turn of 64
// entries of 256 B to N followers in replicate state allocates, per
// follower: #912's second criterion, the bytes of a batch divided by the
// follower count (#1364). benchLeaderTurnNode (sendshared_benchnode_test.go)
// builds the node, the one line a copy of this file onto the commit before
// #912 replaces, so the two sides run the same turn.
func BenchmarkALeaderTurnPerFollower(b *testing.B) {
	log := sharedEntries(100, 64, 256)
	for _, followers := range []int{2, 4} {
		b.Run(fmt.Sprintf("followers=%d", followers), func(b *testing.B) {
			ms := make([]coreconsensus.MessageValue, followers)
			for i := range ms {
				ms[i] = appendTo(coreconsensus.NodeID(i+2), log)
			}
			n := benchLeaderTurnNode()
			b.ReportAllocs()
			for b.Loop() {
				n.sendMessages(ms)
			}
			// B/op is the batch's bytes; benchstat or a reader divides
			// it by the follower count, which the sub-benchmark names.
		})
	}
}
