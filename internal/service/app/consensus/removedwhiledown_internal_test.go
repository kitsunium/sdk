package consensus

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// #763, the remainder #758 left: its refusal reads the node's own log, and
// a removal committed while the node was down is not there. A founder V of
// {A, B, V} saw D and E join and be promoted, D lead and A be removed: its
// log ended on {B, V, D, E}, and D heard it there. V crashed, its log
// damaged before D's admission. While V was down, B was removed on D: #603's
// guard trusted the configuration D last heard V at, which lists D. V's
// TruncateAndStaySilent kept {A, B, V}, and its newest readable
// configuration, {B, V, D, E}, still listed B, so the exit was taken: V
// restarted silent over {A, B, V}, asked A and B, both gone, refused D and
// E, and never ended its silence; once E failed, D and V, two live voters
// of three, committed nothing. The leader now trusts a voter's
// configuration only while that voter answers: B's removal is refused while
// V is down. V's truncation is then taken, V asks B for D's record and
// rejoins, and B is removed. Every directory, the runtime one included, is
// in t.TempDir(); D and E run on MemoryLog.

// TestARemovalWhileAHeardVoterIsDownWaitsForIt: B's removal is refused
// MEMBERSHIP_INVALID while V is down; V clears its quarantine silent over
// {A, B, V}, rejoins through B, and B's removal then goes through. Once E
// fails, D and V still commit.
func TestARemovalWhileAHeardVoterIsDownWaitsForIt(t *testing.T) {
	net := NewMemoryNetwork()
	net.admission = true
	x := &replacedCase{c: &cluster{t: t, net: net, cid: coreconsensus.ClusterID{5, 76, 3}, timing: testTimingDisk()}, run: t.TempDir()}
	x.c.configure = func(_ *testNode, cfg *Config) { cfg.MinVoters = 3 }
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	x.ctx = ctx
	var founders []*diskNode
	var members []coreconsensus.MemberValue
	for i := range 3 {
		dn := &diskNode{dir: t.TempDir()}
		dn.dd = silenceOpen(t, dn.dir, x.run)
		t.Cleanup(func() {
			if dn.node != nil {
				dn.crash(t, x.c)
			}
		})
		slot := fmt.Sprintf("f%d", i)
		id, inc, err := Init(dn.dd, x.c.cid, slot)
		if err != nil {
			t.Fatal(err)
		}
		dn.id = id
		founders = append(founders, dn)
		members = append(members, coreconsensus.MemberValue{ID: id, Incarnation: inc, Slot: slot, SPKI: MemorySPKI(id), Voter: true})
	}
	for _, dn := range founders {
		dn.sm = newKV()
		if err := Bootstrap(context.Background(), new(x.cfg(dn)), members); err != nil {
			t.Fatal(err)
		}
		if err := dn.dd.Close(); err != nil {
			t.Fatal(err)
		}
		dn.dd = silenceOpen(t, dn.dir, x.run)
		x.start(t, dn)
	}
	x.a, x.b, x.v = founders[0], founders[1], founders[2]
	for i := range 3 {
		proposeOnDisk(t, founders, fmt.Sprintf("k%d=v%d", i, i))
	}
	d := x.joinVoter(t, "slot-d")
	e := x.joinVoter(t, "slot-e")
	// D leads from here on: the leader that heard V judges B's removal.
	retryAdmin(t, "transfer to D", func() error { return x.leader(t).TransferLeadership(x.ctx, "op:test", d.id) })
	for deadline := time.Now().Add(10 * time.Second); d.node.Status().Role != coreconsensus.RoleLeader; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("setup: D never led: %+v", d.node.Status())
		}
	}
	// A is removed while V is up: V acknowledges {B, V, D, E}.
	x.a.crash(t, x.c)
	retryAdmin(t, "remove A", func() error { return d.node.Remove(x.ctx, "op:test", x.a.id) })
	tok := x.propose(t, "after=a")
	for deadline := time.Now().Add(10 * time.Second); x.v.node.Status().Applied < tok.Index; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("setup: V did not apply A's removal: %+v", x.v.node.Status())
		}
	}
	if conf := x.v.node.Membership(); len(conf.Members) != 4 {
		t.Fatalf("setup: V's configuration %+v is not {B, V, D, E}", conf)
	}

	// V crashes, its log damaged before D's admission. Once D has not heard
	// it for a vote window (ElectionTimeoutMin), V is down to D, and may
	// come back over {A, B, V}: the removal of B, which answers, is refused.
	x.v.crash(t, x.c)
	replacedDamageFirstCommand(t, x.v)
	time.Sleep(testTimingDisk().ElectionTimeoutMax)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		err := d.node.Remove(x.ctx, "op:test", x.b.id)
		if errs.HasCode(err, coreconsensus.CodeMembershipInvalid) {
			break
		}
		if err == nil {
			var slots []string
			for _, m := range d.node.Membership().Members {
				slots = append(slots, m.Slot)
			}
			t.Fatalf("B's removal was admitted while V, down, may come back over {A, B, V}: the cluster is now %v, V's truncation keeps {f0, f1, f2} and its newest readable configuration still lists B", slots)
		}
		if time.Now().After(deadline) {
			t.Fatalf("B's removal while V is down: %v, want MEMBERSHIP_INVALID", err)
		}
	}

	// V clears its quarantine: the truncation keeps {A, B, V}, and its
	// newest readable configuration still lists B. V asks B, which still
	// follows D, for D's record and rejoins.
	x.v.dd = silenceOpen(t, x.v.dir, x.run)
	// Registered after V's TempDir, so it runs before the directory is
	// removed, which Windows refuses while its LOCK is held.
	t.Cleanup(func() {
		if x.v.node == nil {
			if err := x.v.dd.Close(); err != nil {
				t.Errorf("close V's data directory: %v", err)
			}
		}
	})
	x.v.sm = newKV()
	if _, err := NewNode(x.cfg(x.v)); !errors.Is(err, coreconsensus.LogQuarantined) {
		t.Fatalf("damaged log: %v", err)
	}
	x.rejoins(t)

	// D heard V again, on a configuration that lists D: B goes.
	retryAdmin(t, "remove B once V is back", func() error { return d.node.Remove(x.ctx, "op:test", x.b.id) })
	x.b.crash(t, x.c)
	tok = x.propose(t, "after=b")
	for deadline := time.Now().Add(10 * time.Second); x.v.node.Status().Applied < tok.Index; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("V did not apply B's removal: %+v", x.v.node.Status())
		}
	}

	// One more failure: D and V, two live voters of three, still commit.
	x.c.kill(e)
	x.propose(t, "after=e")
}
