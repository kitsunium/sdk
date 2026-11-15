package consensus

import coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"

// benchSink is a transport that keeps nothing, with the BatchSender
// sibling: what a turn allocates is the driver's alone.
type benchSink struct{ coreconsensus.Transport }

func (benchSink) Send(coreconsensus.NodeID, []byte)               {}
func (benchSink) SendShared(coreconsensus.NodeID, []byte, []byte) {}

// benchLeaderTurnNode is the node BenchmarkALeaderTurnPerFollower sends
// through. Before #912 the node had no batch field: the copy onto that
// commit drops it, and the turn encodes a frame per follower.
func benchLeaderTurnNode() *Node {
	tr := benchSink{}
	return &Node{cfg: Config{Transport: tr}, batch: tr, unfed: map[coreconsensus.NodeID]bool{}}
}
