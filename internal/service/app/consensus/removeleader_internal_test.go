package consensus

import (
	"context"
	"fmt"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Removing the leader is an operation of any node (Q8), and it first
// transfers leadership (D8). Submitted on a follower, it was forwarded to
// the leader, which refused a removal of itself MEMBERSHIP_INVALID: whether
// it succeeded depended on the node that received the call (#529).
func TestRemovingTheLeaderFromAFollowerTransfersFirst(t *testing.T) {
	c := newClusterWith(t, &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 2, 9}, size: 4,
		configure: func(_ *testNode, cfg *Config) { cfg.MinVoters = 3 },
	})
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	// The leader commits with three of the four nodes: f may not have heard
	// of it yet, and a follower that knows no leader answers NOT_LEADER.
	waitFor(t, "the follower knows the leader", func() bool {
		return f.node.Status().Leader == l.id
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := f.node.Remove(ctx, "op:test", l.id)
	if err != nil && !errs.HasCode(err, coreconsensus.CodeOutcomeUnknown) {
		t.Fatalf("Remove(leader %d) on follower %d = %v %v", l.id, f.id, err, errs.FieldsOf(err))
	}
	waitFor(t, "the old leader removed", func() bool {
		_, member := f.node.Membership().Member(l.id)
		return !member
	})
	if st := f.node.Status(); st.Leader == l.id {
		t.Fatalf("the removed node still leads: %+v", st)
	}
}

// removePreferredLeader removes the leader pref through by, the leader
// itself when self, a follower otherwise, at most five times, each attempt
// once pref leads again when preferred, and returns the codes of the
// attempts that failed and whether one succeeded. Frames take oneWay to
// arrive and a Sync takes fsync, a LAN under an SSD: the new leader's no-op
// commits a round trip and an fdatasync after the transfer, so the
// removal's first submission is refused MEMBERSHIP_CHANGE_PENDING and
// submitted again a heartbeat later (removeAfterTransfer).
func removePreferredLeader(t *testing.T, cid byte, preferred, self bool) ([]errs.Code, bool) {
	const oneWay, fsync = time.Millisecond, 2 * time.Millisecond
	c := &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 17, cid}, size: 4,
		timing: coreconsensus.TimingContractSpec{
			Generation: 1, HeartbeatInterval: 50 * time.Millisecond,
			ElectionTimeoutMin: 500 * time.Millisecond, ElectionTimeoutMax: time.Second, MaxClockDrift: 0.05,
		},
	}
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	c.configure = func(_ *testNode, cfg *Config) {
		cfg.MinVoters = 3
		if preferred {
			cfg.PreferredLeaders = []coreconsensus.NodeID{c.nodes[0].id}
		}
		cfg.Transport = delayed(cfg.Transport, oneWay, done)
		cfg.Log = &slowSyncLog{MemoryLog: cfg.Log.(*MemoryLog), d: fsync}
	}
	newClusterWith(t, c)
	pref := c.leader(5 * time.Second)
	if preferred {
		pref = c.nodes[0]
		waitLeaderIs(t, c, pref, 5*time.Second)
	}
	c.settledLeader(5 * time.Second)
	var codes []errs.Code
	for i := range 5 {
		if preferred {
			waitLeaderIs(t, c, pref, 5*time.Second)
		}
		by := pref
		if !self {
			by = c.otherThan(pref)
			waitFor(t, "the follower knows the leader", func() bool {
				return by.node.Status().Leader == pref.id
			})
		}
		// The leaders a third node sees while the Remove runs.
		seen := c.nodes[3]
		if seen == by {
			seen = c.nodes[2]
		}
		wctx, wcancel := context.WithCancel(context.Background())
		ch := seen.node.Watch(wctx)
		var leaders []string
		collected := make(chan struct{})
		go func() {
			defer close(collected)
			last := coreconsensus.NodeID(0)
			for st := range ch {
				if st.Leader != last && st.Leader != 0 {
					last = st.Leader
					who := "other"
					if st.Leader == pref.id {
						who = "removed"
					}
					leaders = append(leaders, fmt.Sprintf("t%d:%s", st.Term, who))
				}
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := by.node.Remove(ctx, "op:test", pref.id)
		cancel()
		// Whatever the answer, the removal took place only if no
		// configuration lists the node any more.
		time.Sleep(300 * time.Millisecond)
		wcancel()
		<-collected
		_, member := c.otherThan(pref).node.Membership().Member(pref.id)
		code, _ := errs.CodeOf(err)
		t.Logf("attempt %d: err=%v, still a member=%v, leaders seen during the call: %v", i+1, err, member, leaders)
		if !member {
			return codes, true
		}
		codes = append(codes, code)
	}
	return codes, false
}

// TestRemovingAPreferredLeaderOnItselfIsNotUndoneByTheWatch is #676: four
// voters, PreferredLeaders = [L], and Remove(L) submitted on L. The removal
// of the leader moves leadership away first (#529), and the new leader's
// preferred watch, started over with the leadership, handed it back to L
// at its first tick once L held the new no-op, before the removal's
// resubmission: L, leading again, refused to remove itself
// MEMBERSHIP_INVALID, at every attempt. A leader that took over by a
// transfer now starts no transfer of its own for the window in which the
// removal is submitted again.
func TestRemovingAPreferredLeaderOnItselfIsNotUndoneByTheWatch(t *testing.T) {
	if codes, ok := removePreferredLeader(t, 9, true, true); !ok {
		t.Fatalf("Remove of the preferred leader, on itself, failed %d times out of %d: %v", len(codes), len(codes), codes)
	}
}

// TestRemovingAPreferredLeaderFromAFollowerIsNotUndoneByTheWatch is #676
// submitted on a follower: the follower takes leadership by a transfer
// first (#529), and the race between its watch handing leadership back and
// the removal's resubmission is gone with the pause.
func TestRemovingAPreferredLeaderFromAFollowerIsNotUndoneByTheWatch(t *testing.T) {
	if codes, ok := removePreferredLeader(t, 10, true, false); !ok {
		t.Fatalf("Remove of the preferred leader, from a follower, failed %d times out of %d: %v", len(codes), len(codes), codes)
	}
}

// TestRemovingTheLeaderOnItselfWithoutPreferredLeaders is #676's control:
// with no PreferredLeaders, the removal of the leader submitted on itself
// goes through, as it did.
func TestRemovingTheLeaderOnItselfWithoutPreferredLeaders(t *testing.T) {
	if codes, ok := removePreferredLeader(t, 11, false, true); !ok {
		t.Fatalf("Remove of the leader, on itself, failed %d times out of %d: %v", len(codes), len(codes), codes)
	}
}

// delayedTransport delivers every frame d after Send, in order: a network
// with a one-way delay, so a quorum's acknowledgement takes a round trip.
type delayedTransport struct {
	*MemoryTransport
	d time.Duration
	q chan delayedFrame
}

// delayedFrame is one frame delayedTransport holds until at.
type delayedFrame struct {
	at    time.Time
	to    coreconsensus.NodeID
	frame []byte
}

// delayed puts tr, a memory transport, behind a one-way delay of d, until
// done is closed.
func delayed(tr coreconsensus.Transport, d time.Duration, done <-chan struct{}) coreconsensus.Transport {
	dt := &delayedTransport{MemoryTransport: tr.(*MemoryTransport), d: d, q: make(chan delayedFrame, 1<<16)}
	go func() {
		for {
			select {
			case <-done:
				return
			case f := <-dt.q:
				if w := time.Until(f.at); w > 0 {
					time.Sleep(w)
				}
				dt.MemoryTransport.Send(f.to, f.frame)
			}
		}
	}()
	return dt
}

// Send queues a copy of frame for delivery d from now.
func (t *delayedTransport) Send(to coreconsensus.NodeID, frame []byte) {
	t.q <- delayedFrame{at: time.Now().Add(t.d), to: to, frame: append([]byte(nil), frame...)}
}

// slowSyncLog is a memory log whose Sync takes d, as an fdatasync does: an
// acknowledgement leaves only after it (D5b), a forward does not wait.
type slowSyncLog struct {
	*MemoryLog
	d time.Duration
}

// Sync waits d, then syncs.
func (l *slowSyncLog) Sync() error {
	time.Sleep(l.d)
	return l.MemoryLog.Sync()
}
