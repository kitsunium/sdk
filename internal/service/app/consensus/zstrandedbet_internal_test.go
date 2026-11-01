package consensus

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// isolatedAs reports whether an audit line names voter id with cause.
func isolatedAs(line map[string]string, id coreconsensus.NodeID, cause string) bool {
	//: one entry per voter, "id:cause+cause".
	for _, v := range strings.Split(line["isolated"], ",") {
		//: id's entry, with that cause among its causes.
		if who, causes, ok := strings.Cut(v, ":"); ok && who == fmt.Sprint(uint64(id)) && strings.Contains(causes, cause) {
			return true
		}
	}
	return false
}

// #765, option (b): #603's guard keeps its two bets on the leader, and
// says when a stopped voter's reach rests on one. The first: the leader is
// counted as a stopped voter's voucher at any address, since it dials every
// voter that lists it. A voter (V) down knows {L, X, V}; D joined and was
// promoted meanwhile; X's removal is admitted, L vouching for V; then L
// moves itself, which the guard never judges. V knows L at the old address
// only, and once L hands over to D, which V does not know, nothing V asks
// answers it. Both stay admitted, and L says what they rest on: its health
// names V as reached only through a leader that moved, and the audit lines
// of its move and of its hand-over name V. Once D leads, its health names V
// stranded. Nothing said any of it.
func TestALeaderThatMovedItselfIsSaidToBeAStoppedVotersLastVoucher(t *testing.T) {
	c, logOf := loggedCluster(t, coreconsensus.ClusterID{7, 6, 5}, 1000)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	l := c.settledLeader(5 * time.Second)
	var v, x *testNode
	//: V and X, the founders that do not lead.
	for _, tn := range c.nodes {
		//: a founder that does not lead.
		if tn != l {
			v, x = tn, v
		}
	}
	tok := propose(t, l.node, "a=1")
	waitFor(t, "V applied a=1", func() bool { return v.node.Status().Applied >= tok.Index })

	// V goes down knowing {L, X, V}. D joins and is promoted; X is removed,
	// L vouching for V at the address V knows.
	c.kill(v)
	waitUnheard(t, l, v.id)
	d := c.join(t, "slot-d")
	retryAdmin(t, "promote D", func() error { return l.node.Promote(ctx, "op:test", d.id) })
	retryAdmin(t, "remove X", func() error { return l.node.Remove(ctx, "op:test", x.id) })
	c.kill(x)
	//: L vouches for V where V looks for it: nothing to say yet.
	if f, ok := peerFlags(l, v.id); !ok || f&(PeerStranded|PeerLeaderMoved|PeerLeaderUnconfirmed) != 0 {
		t.Fatalf("setup: L's health lists V %v with flags %#b before L moved", ok, f)
	}

	// L moves itself: the guard never judges the leader's own move.
	//: the leader's move stays admitted.
	if err := onTheMerits(func() error { return l.node.UpdateAddress(ctx, "op:test", l.id, "moved-l") }); err != nil {
		t.Fatalf("UpdateAddress(L) = %v, want it admitted: the leader's own move is never judged", err)
	}
	//: the status must name V, reached only through the moved leader.
	if f, ok := peerFlags(l, v.id); !ok || !f.Has(PeerLeaderMoved) || f.Has(PeerStranded) {
		t.Errorf("L's health lists V %v with flags %#b: want PeerLeaderMoved, V reached only through L, which it knows at another address", ok, f)
	}
	//: the audit line of L's move must name V.
	if line := appendedLine(logOf(l.id), coreconsensus.EntryMembership, wire.ChangeUpdateAddress, l.id); !isolatedAs(line, v.id, "leader-moved") {
		t.Errorf("L's audit line for its own move = %v: want V (%d) named, leader-moved", line, v.id)
	}

	// L hands over to D, which V does not know.
	retryAdmin(t, "transfer to D", func() error { return l.node.TransferLeadership(ctx, "op:test", d.id) })
	//: the audit line of the hand-over must name V.
	if line := appendedLine(logOf(l.id), coreconsensus.EntryTransfer, 0, d.id); !isolatedAs(line, v.id, "leader-moved") {
		t.Errorf("L's audit line for its hand-over to D = %v: want V (%d) named, leader-moved", line, v.id)
	}
	waitFor(t, "D leads", func() bool { return d.node.Status().Role == coreconsensus.RoleLeader })
	//: under D, the status must name V stranded.
	if f, ok := peerFlags(d, v.id); !ok || !f.Has(PeerStranded) {
		t.Errorf("D's health lists V %v with flags %#b: want PeerStranded, V knows neither D nor L where L serves", ok, f)
	}
}

// #765, option (b): the second bet. Over a compacted log nothing says
// whether a stopped voter the snapshot's base lists knows the leader as a
// voter or as a learner, and the guard counts the leader, listed before it,
// as its voucher. V, down, is listed in the base after L; once L's log is
// compacted, its health names V as reached on that bet alone, and the audit
// line of X's removal, which the bet admits, names V. Nothing said any of
// it.
func TestACompactedLogsBetOnAStoppedVoterIsSaid(t *testing.T) {
	c, logOf := loggedCluster(t, coreconsensus.ClusterID{7, 6, 6}, 8)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	l := c.settledLeader(5 * time.Second)
	// V is the founder the configuration lists last: whichever founder
	// leads, L, is listed before it.
	byID := map[coreconsensus.NodeID]*testNode{}
	//: every founder by its ID.
	for _, tn := range c.nodes {
		byID[tn.id] = tn
	}
	founders := l.node.Membership().Members
	v := byID[founders[len(founders)-1].ID]
	//: V must not lead.
	if l == v {
		to := byID[founders[0].ID]
		retryAdmin(t, "transfer away from V", func() error { return l.node.TransferLeadership(ctx, "op:test", to.id) })
		waitFor(t, "the first founder leads", func() bool { return to.node.Status().Role == coreconsensus.RoleLeader })
		l = c.settledLeader(5 * time.Second)
	}
	var x *testNode
	//: X is the founder that neither leads nor is V.
	for _, tn := range c.nodes {
		//: neither L nor V.
		if tn != l && tn != v {
			x = tn
		}
	}
	tok := propose(t, l.node, "a=1")
	waitFor(t, "V applied a=1", func() bool { return v.node.Status().Applied >= tok.Index })

	// V goes down; L's log is compacted past the founding configuration.
	c.kill(v)
	waitUnheard(t, l, v.id)
	//: entries until the log's first index moved.
	for i := 0; ; i++ {
		//: compacted at last, or out of time.
		if first, _ := l.log.Bounds(); first > 1 {
			break
		}
		//: never compacted.
		if i == 200 {
			t.Fatal("setup: L's log was never compacted")
		}
		propose(t, l.node, fmt.Sprintf("k%d=1", i))
	}
	//: the status must name V, reached on the compacted log's bet.
	if f, ok := peerFlags(l, v.id); !ok || !f.Has(PeerLeaderUnconfirmed) || f.Has(PeerStranded) {
		t.Errorf("L's health lists V %v with flags %#b: want PeerLeaderUnconfirmed, V reached only on the bet that it knows L as a voter", ok, f)
	}

	// D joins and is promoted; X's removal is admitted on the bet.
	d := c.join(t, "slot-d")
	retryAdmin(t, "promote D", func() error { return l.node.Promote(ctx, "op:test", d.id) })
	//: the removal stays admitted.
	if err := onTheMerits(func() error { return l.node.Remove(ctx, "op:test", x.id) }); err != nil {
		t.Fatalf("Remove(X) = %v, want it admitted on the compacted log's bet", err)
	}
	//: the audit line of the removal must name V.
	if line := appendedLine(logOf(l.id), coreconsensus.EntryMembership, wire.ChangeRemove, x.id); !isolatedAs(line, v.id, "leader-unconfirmed") {
		t.Errorf("L's audit line for X's removal = %v: want V (%d) named, leader-unconfirmed", line, v.id)
	}
}
