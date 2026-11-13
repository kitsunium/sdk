//go:build !race

package consensus

import (
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// discardSender is a BatchSender that keeps nothing: what a turn allocates
// is the driver's alone.
type discardSender struct{ coreconsensus.Transport }

func (discardSender) Send(coreconsensus.NodeID, []byte)               {}
func (discardSender) SendShared(coreconsensus.NodeID, []byte, []byte) {}

// sendSharedTurns is how many leader turns a window counts.
const sendSharedTurns int = 200

// TestALeaderTurnEncodesItsEntriesOnceWhateverItsFollowers is the alloc
// lane's budget of #912: a leader turn of 64 entries of 256 B to its
// followers in replicate state allocates two buffers, the one entries body
// and the one block of headers, with two followers as with four. Before,
// each follower cost an encoding of its own, a copy of the whole batch
// grown from nil: mutation-checked by sending through AppendMessage per
// peer, 200 turns performed 7 000 allocations at two followers and 14 001
// at four, and this gate failed both.
func TestALeaderTurnEncodesItsEntriesOnceWhateverItsFollowers(t *testing.T) {
	log := sharedEntries(100, 64, 256)
	for _, followers := range []int{2, 4} {
		ms := make([]coreconsensus.MessageValue, followers)
		for i := range ms {
			ms[i] = appendTo(coreconsensus.NodeID(i+2), log)
		}
		tr := discardSender{}
		n := &Node{cfg: Config{Transport: tr}, batch: tr, unfed: map[coreconsensus.NodeID]bool{}}
		got := mallocsOver(sendSharedTurns, func() { n.sendMessages(ms) })
		reportWindow(t, "leader turn", got)
		if want := uint64(2 * sendSharedTurns); got.own != want {
			t.Errorf("%d turns of 64 entries to %d followers performed %d allocations, want %d (2 a turn: one body, one header block)",
				sendSharedTurns, followers, got.own, want)
		}
	}
}
