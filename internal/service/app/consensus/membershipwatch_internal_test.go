package consensus

import (
	"context"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// #915: a product that routes clients or feeds a service registry learned of
// a configuration change only by polling Membership(), and each poll was a
// closure run on the event loop between protocol turns. WatchMembership
// pushes every configuration the loop publishes, and Membership() reads the
// last one without entering the loop.

// awaitConf reads ch until a configuration satisfies ok, never polling the
// node: the channel holds the latest value only, so one a later change
// replaced before it was read is skipped, never delivered out of order.
func awaitConf(t *testing.T, ch <-chan coreconsensus.MembershipValue, what string, ok func(coreconsensus.MembershipValue) bool) coreconsensus.MembershipValue {
	t.Helper()
	timeout := time.After(10 * time.Second)
	var last coreconsensus.MembershipValue
	for {
		select {
		case m, open := <-ch:
			if !open {
				t.Fatalf("the membership watch closed before %s", what)
			}
			if ok(m) {
				return m
			}
			last = m
		case <-timeout:
			t.Fatalf("the membership watch never delivered %s; last %+v", what, last)
		}
	}
}

// TestWatchMembershipDeliversEveryChange: AddLearner (through JOIN and
// directly), Promote, UpdateAddress and Remove each reach a watcher of the
// leader as a new MembershipValue, at a newer index.
func TestWatchMembershipDeliversEveryChange(t *testing.T) {
	c := newCluster(t, 3, 0)
	l := c.settledLeader(5 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	watch := l.node.WatchMembership(ctx)
	first := awaitConf(t, watch, "the founding configuration", func(m coreconsensus.MembershipValue) bool { return len(m.Members) == 3 })
	at := first.Index
	newer := func(m coreconsensus.MembershipValue) bool {
		if m.Index <= at {
			return false
		}
		at = m.Index
		return true
	}

	tn := &testNode{log: NewMemoryLog(), snaps: NewMemorySnapshots(), sm: newKV()}
	id, _, err := InitLog(tn.log, c.cid, "slot-new")
	if err != nil {
		t.Fatal(err)
	}
	tn.id = id
	c.nodes = append(c.nodes, tn)
	c.start(tn)
	st := must(tn.log.Stamp())(t)
	if _, err := NewMemoryTransport(c.net, id).Join(ctx, coreconsensus.JoinConfig{Stamp: st, Address: "mem"}); err != nil {
		t.Fatalf("join: %v", err)
	}
	awaitConf(t, watch, "the joiner as a learner", func(m coreconsensus.MembershipValue) bool {
		x, ok := m.Member(id)
		return ok && !x.Voter && newer(m)
	})
	c.waitApplied(propose(t, l.node, "y=2").Index, 5*time.Second)
	if err := l.node.Promote(ctx, "op:test", id); err != nil {
		t.Fatalf("promote: %v", err)
	}
	awaitConf(t, watch, "the promoted voter", func(m coreconsensus.MembershipValue) bool {
		x, ok := m.Member(id)
		return ok && x.Voter && newer(m)
	})

	pl := NewMemoryLog()
	pid, pinc, err := InitLog(pl, c.cid, "slot-phantom")
	if err != nil {
		t.Fatal(err)
	}
	pm := coreconsensus.MemberValue{ID: pid, Incarnation: pinc, Slot: "slot-phantom", SPKI: MemorySPKI(pid), Address: "mem"}
	if err := l.node.AddLearner(ctx, "op:test", pm); err != nil {
		t.Fatalf("add learner: %v", err)
	}
	awaitConf(t, watch, "the learner added", func(m coreconsensus.MembershipValue) bool {
		_, ok := m.Member(pid)
		return ok && newer(m)
	})
	if err := l.node.UpdateAddress(ctx, "op:test", pid, "mem-moved"); err != nil {
		t.Fatalf("update address: %v", err)
	}
	awaitConf(t, watch, "the learner's new address", func(m coreconsensus.MembershipValue) bool {
		x, ok := m.Member(pid)
		return ok && x.Address == "mem-moved" && newer(m)
	})
	if err := l.node.Remove(ctx, "op:test", pid); err != nil {
		t.Fatalf("remove: %v", err)
	}
	awaitConf(t, watch, "the learner removed", func(m coreconsensus.MembershipValue) bool {
		_, ok := m.Member(pid)
		return !ok && m.IsRetired(pid) && newer(m)
	})

	// The value delivered is the watcher's own copy: changing it changes
	// nothing the node holds.
	m := <-l.node.WatchMembership(ctx)
	m.Members[0].Address = "scribbled"
	if got := l.node.Membership().Members[0].Address; got == "scribbled" {
		t.Fatal("a watcher's value aliases the node's configuration")
	}

	// The channel is closed once ctx ends.
	cancel()
	for range watch {
	}
}

// TestMembershipDoesNotEnterTheLoop: with the event loop held inside a
// closure, Membership() still returns, at once, the configuration the last
// turn published.
func TestMembershipDoesNotEnterTheLoop(t *testing.T) {
	c := newCluster(t, 1, 0)
	l := c.settledLeader(5 * time.Second)
	want := l.node.Membership()
	if len(want.Members) != 1 {
		t.Fatalf("setup: the configuration has %d members, want 1", len(want.Members))
	}
	held, release := make(chan struct{}), make(chan struct{})
	go l.node.exec(func() {
		close(held)
		<-release
	})
	defer close(release)
	select {
	case <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("setup: the event loop never ran the holding closure")
	}
	got := make(chan coreconsensus.MembershipValue, 1)
	go func() { got <- l.node.Membership() }()
	select {
	case m := <-got:
		if m.Index != want.Index || len(m.Members) != 1 || m.Members[0] != want.Members[0] {
			t.Fatalf("Membership() under a held loop = %+v, want %+v", m, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Membership() waited for the held event loop")
	}
	w := l.node.WatchMembership(t.Context())
	select {
	case m := <-w:
		if m.Index != want.Index {
			t.Fatalf("WatchMembership's first value under a held loop is at %d, want %d", m.Index, want.Index)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WatchMembership waited for the held event loop")
	}
}
