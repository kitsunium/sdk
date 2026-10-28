package consensus

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// joinerGate is a joiner's TCP transport whose deliveries of appends from
// any member but open reaching index stop (or beyond) are dropped while
// shut: it pins the joiner's catch-up below stop, as a long catch-up of a
// big log does when the leadership moves during it.
type joinerGate struct {
	*TCPTransport
	open coreconsensus.NodeID
	stop uint64
	shut atomic.Bool
}

// Bind wraps deliver with the gate.
func (g *joinerGate) Bind(group coreconsensus.GroupID,
	deliver func(coreconsensus.AuthenticatedPeerValue, []byte),
	offer func(coreconsensus.AuthenticatedPeerValue, coreconsensus.InstallOfferValue) coreconsensus.InstallAcceptValue,
	install func(coreconsensus.InstallParams) error,
	join func(context.Context, coreconsensus.AuthenticatedPeerValue, coreconsensus.JoinRequestValue) (coreconsensus.PositionValue, errs.Code),
) error {
	gated := func(p coreconsensus.AuthenticatedPeerValue, frame []byte) {
		if g.shut.Load() && p.ID != g.open {
			var m coreconsensus.MessageValue
			if wire.ParseMessage(frame, &m) == nil && m.Kind == coreconsensus.MsgApp && len(m.Entries) > 0 &&
				m.Entries[len(m.Entries)-1].Position.Index >= g.stop {
				return
			}
		}
		deliver(p, frame)
	}
	return g.TCPTransport.Bind(group, gated, offer, install, join)
}

// joinerLead makes to the leader of ns, transferring from whoever leads.
func joinerLead(t *testing.T, ns []*tcpNode, to *tcpNode) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		l := tcpLeader(t, ns)
		if l == to {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := l.node.TransferLeadership(ctx, "op:test", to.id)
		cancel()
		if err != nil {
			t.Logf("transfer to %d: %v (retrying)", to.id, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("node %d never took the leadership", to.id)
}

// joinerFreeAddr is a loopback address nothing listens on right now.
func joinerFreeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// newUnionTransport is a bare TCP transport whose admission sets are set
// by hand.
func newUnionTransport() *TCPTransport {
	return &TCPTransport{
		members: map[coreconsensus.GroupID][]coreconsensus.MemberValue{}, byID: map[coreconsensus.NodeID]coreconsensus.MemberValue{},
		bySPKI: map[[32]byte]coreconsensus.MemberValue{}, out: map[outKey]*peerConn{}, in: map[net.Conn][32]byte{},
		admission: server.NewAdmission(server.AdmissionConfig{}), peerExpiry: map[coreconsensus.NodeID]time.Time{},
	}
}

// A joining node's transport admits the union of two groups: the JOIN
// answer (joinGroup) and the engine's configuration. A member both list,
// with another address in each — an UpdateAddress between the configuration
// the joiner replays and its JOIN answer — was dialled at whichever address
// the map iteration of indexMembers visited last, drawn again at every
// SetMembership: about 7 times in 8 the replayed, stale one (#742). The
// record of the newer set wins now, every time: the answer's, as of the
// AddLearner it answers, over a configuration replayed from below it; the
// engine's once its configuration passes that position.
func TestTheUnionOfTwoGroupsKeepsTheNewerRecordOfAMember(t *testing.T) {
	a := coreconsensus.MemberValue{ID: 7, Slot: "site-a", SPKI: [32]byte{1}, Voter: true}
	answer, replayed := a, a
	answer.Address, replayed.Address = "10.0.0.1:7001", "10.0.0.1:7000"
	tr := newUnionTransport()
	defer tr.wg.Wait()
	tr.setMembershipAt(joinGroup, []coreconsensus.MemberValue{answer}, rankedAt(1, 40))
	for i := range 200 {
		// The engine's group is published again, as at every change of its
		// admitted set, from a configuration below the answer's position.
		tr.setMembershipAt(1, []coreconsensus.MemberValue{replayed}, rankedAt(1, uint64(1+i%39)))
		if addr, _, _ := tr.address(a.ID); addr != answer.Address {
			t.Fatalf("publication %d: dialled at %s, want the JOIN answer's %s", i, addr, answer.Address)
		}
	}
	// The engine's configuration passes the answer's position: its record,
	// newer, wins, whatever the iteration order.
	moved := a
	moved.Address = "10.0.0.1:7002"
	for i := range 200 {
		tr.setMembershipAt(1, []coreconsensus.MemberValue{moved}, rankedAt(1, uint64(41+i)))
		if addr, _, _ := tr.address(a.ID); addr != moved.Address {
			t.Fatalf("publication %d: dialled at %s, want the newer configuration's %s", i, addr, moved.Address)
		}
	}
	// A key the newer record no longer pins admits nobody: only the newer
	// record's does.
	rekeyed := moved
	rekeyed.SPKI = [32]byte{2}
	tr.setMembershipAt(1, []coreconsensus.MemberValue{rekeyed}, rankedAt(1, 60))
	tr.mu.Lock()
	_, oldKey := tr.bySPKI[a.SPKI]
	_, newKey := tr.bySPKI[rekeyed.SPKI]
	tr.mu.Unlock()
	if oldKey || !newKey {
		t.Fatalf("after the newer record changed the key: old key admitted %v, new key admitted %v", oldKey, newKey)
	}
	// A member one group alone lists stays admitted from it (#552).
	only := coreconsensus.MemberValue{ID: 9, Slot: "site-l", SPKI: [32]byte{3}, Address: "10.0.0.9:7000", Voter: true}
	tr.setMembershipAt(joinGroup, []coreconsensus.MemberValue{answer, only}, rankedAt(2, 40))
	if addr, _, _ := tr.address(only.ID); addr != only.Address {
		t.Fatalf("a member only the answer lists is dialled at %q, want %s", addr, only.Address)
	}
	// Each record is ranked on its own: the leader's, raised to the commit
	// a member's record of its address was read at (#724), wins over the
	// answer, and another member's, as of the replayed configuration, still
	// loses to the answer that moved it.
	tr.setMembershipAt(joinGroup, []coreconsensus.MemberValue{answer, only}, rankedAt(2, 40))
	stale := only
	stale.Address = "10.0.0.9:6999"
	tr.setMembershipAt(1, []coreconsensus.MemberValue{moved, stale}, []uint64{50, 5})
	if addr, _, _ := tr.address(a.ID); addr != moved.Address {
		t.Fatalf("the leader's raised record is dialled at %s, want %s", addr, moved.Address)
	}
	if addr, _, _ := tr.address(only.ID); addr != only.Address {
		t.Fatalf("a member the answer moved is dialled at %s from the replayed configuration, want the answer's %s", addr, only.Address)
	}
}

// A joiner J holds, in its JOIN answer, the current address of every member.
// It replays the cluster's log from index 1, whose configuration still
// names member A at the address an UpdateAddress has since replaced. When
// the leadership moves to A before J's log reaches the UpdateAddress, J,
// a learner that never sent anything to A while B led, answers A at the
// address its transport dials: the replayed one about 7 times in 8, and J
// was then not fed for as long as A led (#742). The JOIN answer's record,
// the newer, is the one dialled: J is fed by A.
func TestAJoinerDialsAMovedLeaderAtItsJoinAnswersAddress(t *testing.T) {
	small := func(c *Config) { c.MaxBatchBytes, c.MaxEntryBytes = 4096, 4096 }
	ca, cid, ns := newTCPClusterWith(t, small)
	a, b := ns[0], ns[1]
	tcpLeader(t, ns)
	// A log long enough that its first batch, which carries the founding
	// configuration, ends well before the move.
	pad := strings.Repeat("x", 512)
	for i := range 40 {
		proposeOnTCPLeader(t, ns, fmt.Sprintf("k%d=%s", i, pad))
	}
	joinerLead(t, ns, b)

	// The operator moves A (ADR 0152 D8, D20): UpdateAddress, then A
	// restarts listening at its new address. Every member is up.
	oldA, newA := a.addr, joinerFreeAddr(t)
	upd := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		upd <- b.node.UpdateAddress(ctx, "op:test", a.id, newA)
	}()
	time.Sleep(300 * time.Millisecond)
	a.stop(t)
	a.addr = newA
	a.listen(t, ca)
	a.start(t, cid)
	if err := <-upd; err != nil {
		t.Fatalf("UpdateAddress: %v", err)
	}
	u := b.node.Membership().Index
	joinerLead(t, ns, b)
	proposeOnTCPLeader(t, ns, "after=move")

	// J joins under B. Its catch-up is pinned below the move.
	gate := &joinerGate{open: a.id, stop: u}
	gate.shut.Store(true)
	j := joinTCPWith(t, ca, cid, ns, "site-j", func(c *Config) {
		small(c)
		gate.TCPTransport = c.Transport.(*TCPTransport)
		c.Transport = gate
	})
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, ok := j.node.Membership().Member(a.id); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("J never replayed a configuration: %+v", j.node.Status())
		}
	}
	time.Sleep(200 * time.Millisecond)
	rm, _ := j.node.Membership().Member(a.id)
	if dial, _, _ := j.tr.address(a.id); dial != newA {
		t.Errorf("J's log replays A at %s (moved %s -> %s at %d), its JOIN answer names %s: J's transport dials A at %s",
			rm.Address, oldA, newA, u, newA, dial)
	}

	// The leadership moves to A while J is still below the move.
	joinerLead(t, ns, a)
	proposeOnTCPLeader(t, ns, "z=1")
	if !waitKV(t, b, "z", "1", 5*time.Second) {
		t.Fatal("B did not apply z=1")
	}
	// The gate is lifted: from here on nothing but J's own transport stands
	// between J and A.
	gate.shut.Store(false)
	if !waitKV(t, j, "z", "1", 4*time.Second) {
		dial, _, _ := j.tr.address(a.id)
		t.Fatalf("J, a learner whose JOIN answer names A at %s, was not fed in 4s while A leads: its transport dials A at %s; J %+v",
			newA, dial, j.node.Status())
	}
	cm, _ := j.node.Membership().Member(a.id)
	if cm.Address != newA {
		t.Fatalf("J caught up, its configuration names A at %s, want %s", cm.Address, newA)
	}
}
