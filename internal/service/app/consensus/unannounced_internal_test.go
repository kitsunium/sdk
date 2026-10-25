package consensus

import (
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
)

// loggedCount counts the messages l recorded that contain s.
func loggedCount(l *fakeLogger, s string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, m := range l.msgs {
		if strings.Contains(m, s) {
			n++
		}
	}
	return n
}

// #665: a leader at cluster version 3 offered every peer the same snapshot,
// its metadata in version 3, whatever the peer announced. A node of
// protocol 2, rolled back past the raise or an old binary that joined
// after it, cannot decode that version: its transport refused the offer
// before its node judged it, and the leader offered it again at every
// probe while the peer stayed out of replication and still answered its
// heartbeats. A peer whose last message of the term announced protocol 2
// but not 3 is no longer offered such a snapshot, and the leader says so,
// once; offered again as soon as the peer announces protocol 3.
func TestALeaderWithholdsAVersion3SnapshotFromAPeerOfProtocol2(t *testing.T) {
	lg := &fakeLogger{}
	c := newClusterWith(t, &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{6, 6, 5}, size: 3,
		configure: func(_ *testNode, cfg *Config) { cfg.Logger = lg },
	})
	l := c.leader(5 * time.Second)
	var nodes []*Node
	for _, tn := range c.nodes {
		nodes = append(nodes, tn.node)
	}
	raiseNodesTo(t, nodes, raft.Protocol3)
	l = c.leader(5 * time.Second)
	v := c.otherThan(l)
	c.kill(v)

	// v's end of the network, bound again by the test: it counts the offers
	// the leader makes it and installs none.
	var offered atomic.Int64
	tr := NewMemoryTransport(c.net, v.id)
	err := tr.Bind(1, func(coreconsensus.AuthenticatedPeerValue, []byte) {},
		func(_ coreconsensus.AuthenticatedPeerValue, o coreconsensus.InstallOfferValue) coreconsensus.InstallAcceptValue {
			offered.Add(1)
			return coreconsensus.InstallAcceptValue{SnapshotID: o.SnapshotID, Refused: coreconsensus.CodeOverloaded}
		},
		func(coreconsensus.InstallParams) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	term := l.node.Status().Term
	from := coreconsensus.AuthenticatedPeerValue{ID: v.id}
	answer := func(kind coreconsensus.MessageKind, flags coreconsensus.MessageFlags) {
		m := &coreconsensus.MessageValue{Version: 1, Group: 1, Kind: kind, From: v.id, To: l.id, Term: term, Flags: flags}
		if kind == coreconsensus.MsgAppResp {
			m.Reject = true
		}
		l.node.deliver(from, appendFrame(m))
	}
	// v answers as a node of protocol 2 does, before the leader has a
	// snapshot to offer it.
	answer(coreconsensus.MsgHeartbeatResp, coreconsensus.FlagProtocol2)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	meta, err := l.node.SaveSnapshot(ctx, "op:cut", io.Discard)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	if meta.ClusterVersion != raft.Protocol3 {
		t.Fatalf("setup: the snapshot is at cluster version %d", meta.ClusterVersion)
	}
	const withheld = "does not announce protocol 3"
	for range 25 {
		answer(coreconsensus.MsgAppResp, coreconsensus.FlagProtocol2|coreconsensus.FlagNeedSnapshot)
		time.Sleep(20 * time.Millisecond)
	}
	if n := offered.Load(); n != 0 {
		t.Fatalf("the leader offered %d times a snapshot at cluster version 3 to a peer whose last message announced protocol 2 only", n)
	}
	if n := loggedCount(lg, withheld); n != 1 {
		t.Errorf("the withheld snapshot was logged %d times, want once: %v", n, lg.msgs)
	}

	// Control: once the peer announces protocol 3, it is offered.
	deadline := time.Now().Add(5 * time.Second)
	for offered.Load() == 0 && time.Now().Before(deadline) {
		answer(coreconsensus.MsgAppResp, coreconsensus.FlagProtocol2|coreconsensus.FlagProtocol3|coreconsensus.FlagNeedSnapshot)
		time.Sleep(20 * time.Millisecond)
	}
	if offered.Load() == 0 {
		t.Fatal("the snapshot was never offered to the peer once it announced protocol 3")
	}
}
