package consensus

import (
	"context"
	"fmt"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// waitHealth waits until tn's verdict has ready and hasLeader as wanted.
func waitHealth(tn *testNode, ready, hasLeader bool, within time.Duration) (HealthValue, bool) {
	var h HealthValue
	for deadline := time.Now().Add(within); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		h = tn.node.Health()
		if h.Flags.Has(HealthReady) == ready && h.Flags.Has(HealthHasLeader) == hasLeader {
			return h, true
		}
	}
	return h, false
}

// #541: a follower whose messages no longer reach the leader, while the
// leader's still reach it, is neither ready nor sure of a leader within
// ElectionTimeoutMax, and is again once the link heals. Every heartbeat
// re-armed it while its answers were lost, its commit froze once the
// leader's window to it was full, and it reported ready on that frozen
// state for as long as the link stayed one-way.
func TestAFollowerTheLeaderNoLongerHearsIsNotReady(t *testing.T) {
	c := newCluster(t, 3, 0)
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	window := 2 * testTimingFast().ElectionTimeoutMax
	if h, ok := waitHealth(f, true, true, 5*time.Second); !ok {
		t.Fatalf("setup: the follower never became ready: %+v", h.Status)
	}
	c.net.Cut(f.id, l.id, true)
	for i := range 100 {
		propose(t, l.node, fmt.Sprintf("k%d=v", i))
	}
	if h, ok := waitHealth(f, false, false, window); !ok {
		t.Fatalf("the follower the leader no longer hears reports ready=%v hasLeader=%v, commit %d against the leader's %d",
			h.Flags.Has(HealthReady), h.Flags.Has(HealthHasLeader), h.Status.Commit, l.node.Status().Commit)
	}
	c.net.Cut(f.id, l.id, false)
	if h, ok := waitHealth(f, true, true, 10*time.Second); !ok {
		t.Fatalf("the healed follower never became ready again: %+v", h.Status)
	}
	// Ready again from a lag within maxReadyLag (#545): the catch-up is
	// waited for, not read at the instant ready returns.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if v, _ := f.sm.get("k99"); v == "v" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the healed follower did not catch up: %+v", f.node.Status())
		}
	}
}

// #540: a learner cut off from every voter is neither ready nor sure of a
// leader, as an isolated voter already was. It never campaigns, so it kept
// the last leader it heard of and reported ready on a frozen commit.
func TestAnIsolatedLearnerIsNotReady(t *testing.T) {
	c := newCluster(t, 3, 0)
	l := c.settledLeader(5 * time.Second)
	propose(t, l.node, "x=1")
	tn := &testNode{log: NewMemoryLog(), snaps: NewMemorySnapshots(), sm: newKV()}
	id, _, err := InitLog(tn.log, c.cid, "slot-learner")
	if err != nil {
		t.Fatal(err)
	}
	tn.id = id
	c.nodes = append(c.nodes, tn)
	c.start(tn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := NewMemoryTransport(c.net, id).Join(ctx, coreconsensus.JoinConfig{Stamp: must(tn.log.Stamp())(t), Address: "mem"}); err != nil {
		t.Fatalf("join: %v", err)
	}
	propose(t, l.node, "y=2")
	if h, ok := waitHealth(tn, true, true, 5*time.Second); !ok {
		t.Fatalf("setup: the learner never became ready: %+v", h.Status)
	}
	if tn.node.Status().Role != coreconsensus.RoleLearner {
		t.Fatalf("setup: role %v, want learner", tn.node.Status().Role)
	}
	c.net.Isolate(id, true)
	for i := range 10 {
		propose(t, l.node, fmt.Sprintf("z%d=v", i))
	}
	if h, ok := waitHealth(tn, false, false, 2*testTimingFast().ElectionTimeoutMax); !ok {
		t.Fatalf("the isolated learner reports ready=%v hasLeader=%v with leader %v",
			h.Flags.Has(HealthReady), h.Flags.Has(HealthHasLeader), h.Status.Leader)
	}
	c.net.Isolate(id, false)
	if h, ok := waitHealth(tn, true, true, 10*time.Second); !ok {
		t.Fatalf("the reconnected learner never became ready again: %+v", h.Status)
	}
}

// #545: once caught up, a node stays ready through an apply lag of a few
// entries that keeps moving, the window between a commit and the next
// publication of what was applied; it is not ready before it first caught
// up, past the lag bound, or once applying stalls.
func TestApplyReadiness(t *testing.T) {
	window := 100 * time.Millisecond
	cases := []struct {
		name            string
		caughtUp        bool
		commit, applied uint64
		moved           time.Duration
		want            bool
	}{
		{"caught up", false, 10, 10, time.Hour, true},
		{"one entry behind, moving", true, 11, 10, time.Millisecond, true},
		{"at the bound, moving", true, 10 + maxReadyLag, 10, time.Millisecond, true},
		{"past the bound", true, 11 + maxReadyLag, 10, time.Millisecond, false},
		{"one entry behind, stalled", true, 11, 10, 2 * window, false},
		{"never caught up", false, 11, 10, time.Millisecond, false},
	}
	for _, c := range cases {
		if got := applyReady(c.caughtUp, c.commit, c.applied, c.moved, window); got != c.want {
			t.Errorf("%s: applyReady = %v, want %v", c.name, got, c.want)
		}
	}
}

// #545, end to end: under a steady write load, a caught-up follower is
// never reported not ready for a lag of a few entries. Its published
// status paired each new commit with the applied index of the turn
// before, and a strict comparison flapped its readiness. The stall bound
// is the minimum election timeout: at testTimingFast's 100 ms, a runner
// that held the follower's apply that long had it not ready by the rule,
// once in 34 samples on linux/386 (#654); steadyTiming's 500 ms leaves the
// lag the test is about.
func TestAFollowerUnderLoadStaysReady(t *testing.T) {
	c := newClusterWith(t, &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 2, 3}, size: 3, timing: steadyTiming()})
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	if h, ok := waitHealth(f, true, true, 5*time.Second); !ok {
		t.Fatalf("setup: the follower never became ready: %+v", h.Status)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 400 {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, _, _ = l.node.Propose(ctx, []byte(fmt.Sprintf("w%d=v", i)))
			cancel()
		}
	}()
	flaps, samples := 0, 0
	for {
		select {
		case <-done:
			if flaps > 0 {
				t.Fatalf("a caught-up follower under load answered not ready %d times in %d samples with a lag within %d entries", flaps, samples, maxReadyLag)
			}
			return
		default:
		}
		h := f.node.Health()
		samples++
		if !h.Flags.Has(HealthReady) && h.Flags.Has(HealthHasLeader) && h.ApplyLagEntries > 0 && h.ApplyLagEntries <= maxReadyLag {
			flaps++
		}
		time.Sleep(time.Millisecond)
	}
}
