package consensus

import (
	"runtime"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// A sender goroutine and its queue are made per peer and lane on the first
// Send. They were kept after the peer left the admitted set, for the life
// of the transport: every node replacement left two goroutines and two
// 4096-frame queues on each survivor (#592). SetMembership now stops and
// forgets the senders of every peer it no longer admits, and keeps those of
// the peers it still admits.
func TestTheSendersOfPeersThatLeftAreReleased(t *testing.T) {
	ca := newTestCA(t)
	cid := coreconsensus.ClusterID{0xa3}
	tn := &tcpNode{slot: "site-0"}
	tn.cert, tn.spki = ca.issue(t, SAN(cid, tn.slot), nil)
	tn.listen(t, ca)
	t.Cleanup(func() { _ = tn.tr.Close() })
	before := runtime.NumGoroutine()
	send := func(id coreconsensus.NodeID) {
		hb := wire.AppendMessage(nil, &coreconsensus.MessageValue{Version: 1, Group: 1, Kind: coreconsensus.MsgHeartbeat, From: 1, To: id, Term: 1})
		app := wire.AppendMessage(nil, &coreconsensus.MessageValue{Version: 1, Group: 1, Kind: coreconsensus.MsgApp, From: 1, To: id, Term: 1})
		tn.tr.Send(id, hb)
		tn.tr.Send(id, app)
	}
	stay := coreconsensus.MemberValue{ID: 7, Slot: "y", SPKI: [32]byte{0xff, 1}, Address: "127.0.0.1:1"}
	// 100 members, each admitted beside one that stays, sent a heartbeat
	// and an append, then removed (node replacement over the cluster's
	// life: Remove retires the id).
	for i := range 100 {
		id := coreconsensus.NodeID(1000 + i)
		tn.tr.SetMembership(1, []coreconsensus.MemberValue{stay, {ID: id, Slot: "x", SPKI: [32]byte{byte(i), 1}, Address: "127.0.0.1:1"}})
		send(id)
		send(stay.ID)
	}
	tn.tr.SetMembership(1, []coreconsensus.MemberValue{stay})
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before+10 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	tn.tr.mu.Lock()
	out := len(tn.tr.out)
	_, kept := tn.tr.out[outKey{stay.ID, laneControl}]
	tn.tr.mu.Unlock()
	if out != 2 || !kept {
		t.Errorf("one member is admitted, yet the transport keeps %d sender queues (want its 2 lanes, its control lane kept: %v), each a %d-frame channel", out, kept, senderQueueFrames)
	}
	if after := runtime.NumGoroutine(); after > before+10 {
		t.Errorf("%d goroutines more than before 100 members came and left", after-before)
	}
}
