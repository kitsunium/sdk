package consensus

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// joinTCP starts a new node on slot, sends its JOIN to every member of ns
// and returns it once admitted as a learner.
func joinTCP(t *testing.T, ca *testCA, cid coreconsensus.ClusterID, ns []*tcpNode, slot string) *tcpNode {
	t.Helper()
	return joinTCPWith(t, ca, cid, ns, slot, nil)
}

// joinTCPWith is joinTCP with tweak applied to the joiner's configuration.
func joinTCPWith(t *testing.T, ca *testCA, cid coreconsensus.ClusterID, ns []*tcpNode, slot string, tweak func(*Config)) *tcpNode {
	t.Helper()
	j := &tcpNode{slot: slot, log: NewMemoryLog(), snaps: NewMemorySnapshots(), tweak: tweak}
	j.cert, j.spki = ca.issue(t, SAN(cid, j.slot), nil)
	j.listen(t, ca)
	id, _, err := InitLog(j.log, cid, j.slot)
	if err != nil {
		t.Fatal(err)
	}
	j.id = id
	j.start(t, cid)
	t.Cleanup(func() { j.stop(t) })
	st := must(j.log.Stamp())(t)
	addrs := make([]string, 0, len(ns))
	for _, tn := range ns {
		addrs = append(addrs, tn.addr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for {
		_, err := j.tr.Join(ctx, coreconsensus.JoinConfig{Addresses: addrs, Stamp: st, Address: j.addr, Pins: pinsOf(ns)})
		if err == nil {
			return j
		}
		if ctx.Err() != nil {
			t.Fatalf("join %s: %v", slot, err)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// waitKV waits until tn's machine holds key=want.
func waitKV(t *testing.T, tn *tcpNode, key, want string, within time.Duration) bool {
	t.Helper()
	for deadline := time.Now().Add(within); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if v, _ := tn.sm.get(key); v == want {
			return true
		}
	}
	return false
}

// #552: a joiner whose leader was admitted after the configuration the
// joiner replays (an older snapshot, or the first batch of a log fed from
// index 1) keeps admitting that leader and catches up. Its transport took
// the leader only from the JOIN answer, in the engine's own group: the
// first configuration the joiner replayed replaced it, cut the leader's
// connections and refused its handshakes for as long as it led.
func TestAJoinerKeepsALeaderNewerThanTheConfigurationItReplays(t *testing.T) {
	cases := []struct {
		name    string
		founder func(*Config)
		leader  func(*Config)
	}{
		{"older snapshot", func(c *Config) { c.SnapshotEvery, c.SnapshotTrailing = 8, 1 }, nil},
		{"first batch of the log", nil, func(c *Config) { c.MaxBatchBytes, c.MaxEntryBytes = 4096, 4096 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ca, cid, ns := newTCPClusterWith(t, c.founder)
			pad := strings.Repeat("x", 512)
			for i := range 40 {
				proposeOnTCPLeader(t, ns, fmt.Sprintf("k%d=%s", i, pad))
			}
			// D joins, catches up, is promoted and takes the leadership.
			d := joinTCPWith(t, ca, cid, ns, "site-d", c.leader)
			proposeOnTCPLeader(t, ns, "a=1")
			if !waitKV(t, d, "a", "1", 10*time.Second) {
				t.Fatalf("D never caught up: %+v", d.node.Status())
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := tcpLeader(t, ns).node.Promote(ctx, "op:test", d.id); err != nil {
				t.Fatalf("promote D: %v", err)
			}
			if err := tcpLeader(t, ns).node.TransferLeadership(ctx, "op:test", d.id); err != nil {
				t.Fatalf("transfer to D: %v", err)
			}
			all := append(append([]*tcpNode(nil), ns...), d)
			if l := tcpLeader(t, all); l != d {
				t.Fatalf("D does not lead: %d does", l.id)
			}
			// E joins while D leads: D feeds it what predates D.
			e := joinTCP(t, ca, cid, all, "site-e")
			proposeOnTCPLeader(t, all, "b=2")
			if !waitKV(t, e, "b", "2", 4*testTimingFast().ElectionTimeoutMax+5*time.Second) {
				t.Fatalf("E never caught up under D: %+v", e.node.Status())
			}
		})
	}
}

// #632, #552's residual: a joiner J admits, apart from its engine's
// configuration, only the members its JOIN answer listed. A member L added
// AFTER that answer, promoted and handed the leadership before J replayed
// L's AddLearner, is in no group of J's transport: J refused every
// connection of L, and a learner never campaigned, so no leader record
// vouched for L either. J was never fed while L led. A joiner that hears no
// leader now sends its JOIN again with the configuration it last joined
// with, and the fresh answer, which lists L, replaces the old one.
//
// J's lag is emulated as TestAJoinerRestartedBeforeItsFirstInstallRejoins
// does: its JOIN is answered before its node runs, so nothing the old leader
// sends reaches its log (in production: a long snapshot install cut by the
// leadership change, or a partition).
func TestAJoinerHearsALeaderAdmittedAfterItsJoinAnswer(t *testing.T) {
	ca, cid, ns := newTCPCluster(t)
	proposeOnTCPLeader(t, ns, "a=1")
	j := &tcpNode{slot: "site-j", log: NewMemoryLog(), snaps: NewMemorySnapshots()}
	j.cert, j.spki = ca.issue(t, SAN(cid, j.slot), nil)
	j.listen(t, ca)
	id, _, err := InitLog(j.log, cid, j.slot)
	if err != nil {
		t.Fatal(err)
	}
	j.id = id
	st := must(j.log.Stamp())(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	addrs := make([]string, 0, len(ns))
	for _, tn := range ns {
		addrs = append(addrs, tn.addr)
	}
	for i := 0; ; i++ {
		if _, err = j.tr.Join(ctx, coreconsensus.JoinConfig{Addresses: addrs, Stamp: st, Address: j.addr, Pins: pinsOf(ns)}); err == nil {
			break
		}
		if i == 40 {
			t.Fatalf("J's JOIN: %v", err)
		}
		time.Sleep(150 * time.Millisecond)
	}
	// L joins after J's answer, catches up, is promoted and takes the lead.
	l := joinTCP(t, ca, cid, ns, "site-l")
	proposeOnTCPLeader(t, ns, "b=2")
	if !waitKV(t, l, "b", "2", 10*time.Second) {
		t.Fatalf("L never caught up: %+v", l.node.Status())
	}
	if err := tcpLeader(t, ns).node.Promote(ctx, "op:test", l.id); err != nil {
		t.Fatalf("promote L: %v", err)
	}
	if err := tcpLeader(t, ns).node.TransferLeadership(ctx, "op:test", l.id); err != nil {
		t.Fatalf("transfer to L: %v", err)
	}
	all := append(append([]*tcpNode(nil), ns...), l)
	if ld := tcpLeader(t, all); ld != l {
		t.Fatalf("L does not lead: %d does", ld.id)
	}
	if _, ok := l.node.Membership().Member(j.id); !ok {
		t.Fatal("J is not in L's configuration")
	}
	// J's node now runs, on the transport that holds the JOIN answer, which
	// predates L.
	j.start(t, cid)
	defer j.stop(t)
	proposeOnTCPLeader(t, all, "c=3")
	within := 4*testTimingFast().ElectionTimeoutMax + 5*time.Second
	if !waitKV(t, j, "c", "3", within) {
		j.tr.mu.Lock()
		_, admitsL := j.tr.bySPKI[l.spki]
		j.tr.mu.Unlock()
		t.Fatalf("J, a learner in L's configuration, was never fed while L leads (J's transport admits L's key: %v; J %+v)",
			admitsL, j.node.Status())
	}
}

// #632: a JOIN sent again replaces a held answer with the fresh one, and
// installs nothing once the node's admission released the answer: it would
// keep admitting members the configuration no longer lists.
func TestAJoinSentAgainReplacesOnlyAHeldAnswer(t *testing.T) {
	net := NewMemoryNetwork()
	a := NewMemoryTransport(net, 1)
	a.SetMembership(1, []coreconsensus.MemberValue{{ID: 1, SPKI: MemorySPKI(1)}})
	if err := a.Bind(1, func(coreconsensus.AuthenticatedPeerValue, []byte) {}, nil, nil,
		func(context.Context, coreconsensus.AuthenticatedPeerValue, coreconsensus.JoinRequestValue) (coreconsensus.PositionValue, errs.Code) {
			return coreconsensus.PositionValue{Term: 1, Index: 1}, 0
		}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	j := NewMemoryTransport(net, 2)
	ctx := context.Background()
	if changed, err := j.refreshJoin(ctx); changed || err != nil {
		t.Fatalf("a JOIN sent again before any Join: %v %v", changed, err)
	}
	cfg := coreconsensus.JoinConfig{Stamp: coreconsensus.StampValue{ID: 2, Slot: "j"}, Pins: [][32]byte{MemorySPKI(1)}}
	if _, err := j.Join(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	held := func() []coreconsensus.MemberValue {
		j.mu.Lock()
		defer j.mu.Unlock()
		return slices.Clone(j.groups[joinGroup])
	}
	if len(held()) != 1 {
		t.Fatalf("the answer: %+v", held())
	}
	if changed, err := j.refreshJoin(ctx); changed || err != nil {
		t.Fatalf("a JOIN sent again to an unchanged cluster: %v %v", changed, err)
	}
	a.SetMembership(1, []coreconsensus.MemberValue{{ID: 1, SPKI: MemorySPKI(1)}, {ID: 3, SPKI: MemorySPKI(3)}})
	if changed, err := j.refreshJoin(ctx); !changed || err != nil || len(held()) != 2 {
		t.Fatalf("a JOIN sent again after a member was admitted: %v %v, answer %+v", changed, err, held())
	}
	j.releaseJoinAnswer()
	a.SetMembership(1, []coreconsensus.MemberValue{{ID: 1, SPKI: MemorySPKI(1)}, {ID: 4, SPKI: MemorySPKI(4)}})
	if changed, err := j.refreshJoin(ctx); changed || err != nil || len(held()) != 0 {
		t.Fatalf("a JOIN sent again after the release: %v %v, answer %+v", changed, err, held())
	}
	if _, err := j.Join(ctx, cfg); err != nil || len(held()) != 2 {
		t.Fatalf("an explicit Join after the release: %v, answer %+v", err, held())
	}
}
