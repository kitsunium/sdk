package consensus

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// holdApply blocks tn's machine in Apply until the returned release runs.
func holdApply(tn *testNode) (release func()) {
	tn.sm.mu.Lock()
	var once sync.Once
	return func() { once.Do(tn.sm.mu.Unlock) }
}

// #674: a leader that handed its leadership over because its apply made no
// progress (#554) declines a transfer back, an operator's included: it
// still acknowledges, so a transfer reached it, and it led again with every
// write entering at it hung, until it handed over once more. Once its apply
// moves, it takes one again.
func TestANodeThatHandedOverForAStuckApplyDeclinesATransferBack(t *testing.T) {
	c := newClusterWith(t, &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 2, 4}, size: 3})
	l := c.settledLeader(5 * time.Second)
	c.waitApplied(propose(t, l.node, "a=1").Index, 5*time.Second)
	release := holdApply(l)
	defer release()
	// A write entering at the stuck leader puts something to apply.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _, _ = l.node.Propose(ctx, []byte("stuck=1"))
	}()
	var x *testNode
	waitFor(t, "the stuck leader hands its leadership over", func() bool {
		for _, tn := range c.nodes {
			if tn != l && tn.node.Status().Role == coreconsensus.RoleLeader {
				x = tn
				return true
			}
		}
		return false
	})
	propose(t, x.node, "b=2")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := x.node.TransferLeadership(ctx, "op:test", l.id)
	cancel()
	if !errs.HasCode(err, coreconsensus.CodeTransferFailed) {
		t.Fatalf("TransferLeadership back to %d, whose apply is still stuck: %v, want TRANSFER_FAILED", l.id, err)
	}
	if l.node.Status().Role == coreconsensus.RoleLeader {
		t.Fatalf("the node whose apply is stuck leads again")
	}
	release()
	c.waitApplied(propose(t, x.node, "c=3").Index, 5*time.Second)
	if refused, err := transferRetried(x.node, l.id); err != nil {
		t.Fatalf("TransferLeadership to %d once its apply moved, after %d refusals: %v", l.id, refused, err)
	}
	propose(t, l.node, "d=4")
}

// #674's other side: with the leader's apply and the first voter's stuck,
// the first voter takes the leadership over, then hands it over in turn.
// The node that handed over first declines it, and the hand-off, which
// used to go to the same most caught-up voter at every try, kept the stuck
// voter leading. The next try goes to another voter.
func TestALeaderWithAStuckApplyHandsOverPastAVoterAlsoStuck(t *testing.T) {
	c := newClusterWith(t, &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 2, 5}, size: 3})
	l := c.settledLeader(5 * time.Second)
	// The leader is the first member of the configuration, x the second and
	// y the third: on equal matches a hand-off picks the first, so the
	// leader's goes to x, and x's to the leader, which declines it.
	var byConf []*testNode
	for _, m := range l.node.Membership().Members {
		for _, tn := range c.nodes {
			if tn.id == m.ID {
				byConf = append(byConf, tn)
			}
		}
	}
	if l != byConf[0] {
		if refused, err := transferRetried(l.node, byConf[0].id); err != nil {
			t.Fatalf("setup: TransferLeadership to %d, after %d refusals: %v", byConf[0].id, refused, err)
		}
		l = byConf[0]
	}
	x, y := byConf[1], byConf[2]
	c.waitApplied(propose(t, l.node, "a=1").Index, 5*time.Second)
	defer holdApply(x)()
	defer holdApply(l)()
	// Writes enter at the leader: neither it nor x applies them.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _, _ = l.node.Propose(ctx, []byte("stuck=1"))
	}()
	within := 10 * testTimingFast().ElectionTimeoutMax
	deadline := time.Now().Add(within)
	for y.node.Status().Role != coreconsensus.RoleLeader {
		if time.Now().After(deadline) {
			t.Fatalf("%d, the only voter whose apply moves, does not lead after %v: leader %d", y.id, within, c.leader(time.Second).id)
		}
		time.Sleep(5 * time.Millisecond)
	}
	propose(t, y.node, "b=2")
}

// declines reads, on tn's loop, whether tn declines a transfer to itself:
// it handed its leadership over for a stuck apply that has not moved since
// (#674).
func declines(t *testing.T, tn *testNode) bool {
	t.Helper()
	var ceded bool
	if !tn.node.exec(func() { ceded = tn.node.watch.ceded }) {
		t.Fatal("the node stopped")
	}
	return ceded
}

// firstHandOff is the voter a hand-off on n goes to first, read on n's loop
// as handOff reads it: the voter other than n with the highest match, the
// first of the configuration on a tie. It returns 0 until every voter
// acknowledged n's last entry, so the pick does not depend on an
// acknowledgement still on its way.
func firstHandOff(n *Node) coreconsensus.NodeID {
	var to coreconsensus.NodeID
	n.exec(func() {
		st := n.r.Status()
		var best uint64
		for _, m := range st.Conf.Members {
			if !m.Voter || m.ID == n.stamp.ID {
				continue
			}
			match, _, ok := n.r.Progress(m.ID)
			if !ok || match != st.Last {
				to = 0
				return
			}
			if to == 0 || match > best {
				to, best = m.ID, match
			}
		}
	})
	return to
}

// TestRemovingTheLeaderOnItselfHandsOverPastAVoterThatDeclines is #681:
// four voters, the first of the configuration, s, leads, its apply gets
// stuck, and it hands its leadership over to x (#554), then declines every
// transfer while its apply does not move (#674). Remove(x) submitted on x
// moves the leadership away first (#529), and the hand-off went to the
// most caught-up voter, the first of the configuration on a tie: s, which
// declined. The transfer expired, x still led and refused to remove itself
// MEMBERSHIP_INVALID, at every attempt for as long as s's apply stayed
// stuck. The hand-off now goes on to the next voter.
func TestRemovingTheLeaderOnItselfHandsOverPastAVoterThatDeclines(t *testing.T) {
	removeSelfPastADecliner(t, coreconsensus.ClusterID{1, 2, 6}, 0)
}

// removeSelfPastADecliner is #681's scenario: four voters, s, the first of
// the configuration, leads, its apply gets stuck, it hands its leadership
// over to x and declines every transfer, and Remove(x) is submitted on x,
// whose hand-off goes to s first. A factor other than 0 first commits a
// contract whose maximum election timeout is factor times the
// InitialTiming's (#694).
func removeSelfPastADecliner(t *testing.T, cid coreconsensus.ClusterID, factor time.Duration) {
	c := newClusterWith(t, &cluster{
		t: t, net: NewMemoryNetwork(), cid: cid, size: 4,
		configure: func(_ *testNode, cfg *Config) { cfg.MinVoters = 3 },
	})
	l := c.settledLeader(5 * time.Second)
	var byConf []*testNode
	for _, m := range l.node.Membership().Members {
		for _, tn := range c.nodes {
			if tn.id == m.ID {
				byConf = append(byConf, tn)
			}
		}
	}
	s := byConf[0]
	if l != s {
		if refused, err := transferRetried(l.node, s.id); err != nil {
			t.Fatalf("setup: TransferLeadership to %d, after %d refusals: %v", s.id, refused, err)
		}
	}
	if factor != 0 {
		runLonger(t, c, s, factor)
	}
	c.waitApplied(propose(t, s.node, "a=1").Index, 5*time.Second)
	defer holdApply(s)()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _, _ = s.node.Propose(ctx, []byte("stuck=1"))
	}()
	var x *testNode
	waitFor(t, "the stuck leader hands its leadership over", func() bool {
		for _, tn := range c.nodes {
			if tn != s && tn.node.Status().Role == coreconsensus.RoleLeader {
				x = tn
				return true
			}
		}
		return false
	})
	propose(t, x.node, "b=2")
	if !declines(t, s) {
		t.Fatalf("setup: %d, whose apply is stuck, does not decline a transfer", s.id)
	}
	// On a quiet cluster every match is equal: the hand-off goes to s
	// first, which declines.
	waitFor(t, "every voter acknowledges the leader's last entry", func() bool { return firstHandOff(x.node) != 0 })
	if pick := firstHandOff(x.node); pick != s.id {
		t.Fatalf("setup: the hand-off on %d goes to %d first, not to %d, which declines", x.id, pick, s.id)
	}
	removeSelfOnce(t, x, fmt.Sprintf("while %d declines every transfer", s.id))
	var y *testNode
	for _, tn := range byConf[1:] {
		if tn != x {
			y = tn
			break
		}
	}
	waitFor(t, "the old leader removed", func() bool {
		_, member := y.node.Membership().Member(x.id)
		return !member
	})
	if !declines(t, s) {
		t.Fatalf("%d stopped declining while its apply is stuck", s.id)
	}
	if lead := c.leader(time.Second); lead == s || lead == x {
		t.Fatalf("%d leads after the removal, want a voter whose apply moves", lead.id)
	}
}

// TestALocalTransferWaitsOutTheOneUnderWay is #681's other half: raft
// starts no transfer while one runs, and the hand-off's wait for a voter
// that declines ends a tick before raft's deadline, so the transfer to the
// next voter, started in that tick, sent nobody a TimeoutNow and expired.
// A local transfer now waits out the one under way, then starts its own.
func TestALocalTransferWaitsOutTheOneUnderWay(t *testing.T) {
	c := newClusterWith(t, &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 2, 7}, size: 3})
	l := c.settledLeader(5 * time.Second)
	var gone, next *testNode
	for _, tn := range c.nodes {
		switch {
		case tn == l:
		case gone == nil:
			gone = tn
		default:
			next = tn
		}
	}
	c.kill(gone)
	// A transfer to the voter gone runs until its deadline.
	var code errs.Code
	if !l.node.exec(func() {
		code = l.node.r.TransferLeadership(l.node.clk.Now(), gone.id)
		l.node.handleReady()
	}) {
		t.Fatal("the leader stopped")
	}
	if code != 0 {
		t.Fatalf("setup: the transfer to %d refused: %v", gone.id, code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := l.node.transferLocal(ctx, next.id); err != nil {
		t.Fatalf("transferLocal(%d) while a transfer to %d runs: %v %v", next.id, gone.id, err, errs.FieldsOf(err))
	}
	propose(t, next.node, "a=1")
}

// runLonger commits, through the leader l, a timing contract whose maximum
// election timeout is factor times the nodes' InitialTiming's, as after a
// ProposeTimingContract or on a node that joined with the default
// InitialTiming into a cluster founded with longer timeouts, and waits
// until every node runs on it.
func runLonger(t *testing.T, c *cluster, l *testNode, factor time.Duration) {
	t.Helper()
	initial := l.node.cfg.InitialTiming
	next := coreconsensus.TimingContractSpec{
		HeartbeatInterval: initial.HeartbeatInterval, ElectionTimeoutMin: factor * initial.ElectionTimeoutMax / 2,
		ElectionTimeoutMax: factor * initial.ElectionTimeoutMax, MaxClockDrift: initial.MaxClockDrift,
	}
	pre := coreconsensus.PreconditionSpec{Log: l.node.lineage(), Generation: l.node.Status().Timing.Generation}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := l.node.ProposeTimingContract(ctx, "op:test", pre, next); err != nil {
		t.Fatalf("setup: ProposeTimingContract: %v", err)
	}
	waitFor(t, "every node runs on the longer contract", func() bool {
		for _, tn := range c.nodes {
			if tn.node != nil && tn.node.Status().Timing.ElectionTimeoutMax != next.ElectionTimeoutMax {
				return false
			}
		}
		return true
	})
}

// removeSelfOnce submits Remove of the leader x on x, once, and fails the
// test unless it went through.
func removeSelfOnce(t *testing.T, x *testNode, why string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if err := x.node.Remove(ctx, "op:test", x.id); err != nil && !errs.HasCode(err, coreconsensus.CodeOutcomeUnknown) {
		t.Fatalf("Remove(%d) on itself %s, after %v: %v %v", x.id, why, time.Since(start).Round(time.Millisecond), err, errs.FieldsOf(err))
	}
}

// TestRemovingTheLeaderOnItselfHandsOverPastAVoterThatDeclinesUnderALongerContract
// is #694: #681's scenario under a committed contract whose maximum
// election timeout is four times the InitialTiming's. Each try of the
// hand-off was bounded by the InitialTiming's, raft's transfer to the voter
// that declines by the contract's: the tries to the next voters, which wait
// that transfer out (#681), expired before it ended, no voter past the
// first was ever sent a TimeoutNow, and the Remove failed NOT_LEADER at
// every attempt. Each try now lasts the committed contract's maximum,
// raft's own deadline.
func TestRemovingTheLeaderOnItselfHandsOverPastAVoterThatDeclinesUnderALongerContract(t *testing.T) {
	removeSelfPastADecliner(t, coreconsensus.ClusterID{0x69, 0x94, 1}, 4)
}

// TestRemovingTheLeaderOnItselfHandsOverPastADeadVoterUnderALongerContract
// is #694 with every apply moving: four voters, the first of the
// configuration, d, acknowledges the last entry and dies, so on a quiet
// cluster the hand-off picks it first. Its TimeoutNow is lost and raft's
// transfer to it runs for the contract's maximum election timeout, four
// times the InitialTiming's: the tries to the live voters, bounded by the
// InitialTiming's, expired while they waited it out.
func TestRemovingTheLeaderOnItselfHandsOverPastADeadVoterUnderALongerContract(t *testing.T) {
	c := newClusterWith(t, &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{0x69, 0x94, 2}, size: 4,
		configure: func(_ *testNode, cfg *Config) { cfg.MinVoters = 3 },
	})
	l := c.settledLeader(5 * time.Second)
	var byConf []*testNode
	for _, m := range l.node.Membership().Members {
		for _, tn := range c.nodes {
			if tn.id == m.ID {
				byConf = append(byConf, tn)
			}
		}
	}
	d, x := byConf[0], byConf[1]
	if l != x {
		if refused, err := transferRetried(l.node, x.id); err != nil {
			t.Fatalf("setup: TransferLeadership to %d, after %d refusals: %v", x.id, refused, err)
		}
	}
	runLonger(t, c, x, 4)
	propose(t, x.node, "a=1")
	waitFor(t, "every voter acknowledges the leader's last entry", func() bool { return firstHandOff(x.node) != 0 })
	c.kill(d)
	if pick := firstHandOff(x.node); pick != d.id {
		t.Fatalf("setup: the hand-off on %d goes to %d first, not to %d, which is dead", x.id, pick, d.id)
	}
	removeSelfOnce(t, x, fmt.Sprintf("while %d, the first pick, is dead", d.id))
	waitFor(t, "the old leader removed", func() bool {
		_, member := byConf[2].node.Membership().Member(x.id)
		return !member
	})
}

// leaderSilence loses, on every node's memory transport, the appends and
// heartbeats sent to one node: no leader reaches it, while the votes it is
// asked for and its own frames still go through.
type leaderSilence struct {
	// to is the node no leader reaches; 0 lets every frame through.
	to atomic.Uint64
}

// silencedTransport is one node's memory transport under a leaderSilence.
type silencedTransport struct {
	*MemoryTransport
	silence *leaderSilence
}

// Send drops an append or a heartbeat to the node the silence names, and
// sends the rest.
func (t *silencedTransport) Send(to coreconsensus.NodeID, frame []byte) {
	if uint64(to) == t.silence.to.Load() {
		var m coreconsensus.MessageValue
		if err := wire.ParseMessage(append([]byte(nil), frame...), &m); err == nil &&
			(m.Kind == coreconsensus.MsgApp || m.Kind == coreconsensus.MsgHeartbeat) {
			return
		}
	}
	t.MemoryTransport.Send(to, frame)
}

// handOffEnd runs ended, once, when a node logs that a try of its
// leadership hand-off did not complete.
type handOffEnd struct {
	once  sync.Once
	ended func()
}

func (l *handOffEnd) Log(_ context.Context, _ level.Level, msg string, _ ...corelogger.AttrValue) {
	if msg == "consensus: leadership hand-off did not complete" {
		l.once.Do(l.ended)
	}
}
func (l *handOffEnd) With(...corelogger.AttrValue) corelogger.Logger { return l }
func (l *handOffEnd) WithGroup(string) corelogger.Logger             { return l }
func (l *handOffEnd) Enabled(context.Context, level.Level) bool      { return true }

// TestRemovingTheLeaderOnItselfWaitsForTheLeaderItsHandOffElected is #701:
// Remove(x) submitted on the leader x hands the leadership over first
// (#529), and the TimeoutNow x sends makes the target campaign at once. Its
// vote request deposes x, a follower that knows no leader until the
// winner's first append reaches it. A try of the hand-off that expired in
// that gap ended the hand-off, x no longer leading, and the removal,
// submitted at once, was refused NOT_LEADER: x had no leader to forward it
// to. Nothing was appended, and the leadership had moved as the call asked;
// on a slow runner the gap outlasted the try, and #681's test failed
// NOT_LEADER on NetBSD. Here no leader reaches x until a few heartbeats
// after its try expired, and the removal goes through.
func TestRemovingTheLeaderOnItselfWaitsForTheLeaderItsHandOffElected(t *testing.T) {
	silence := new(leaderSilence)
	beat := testTimingFast().HeartbeatInterval
	end := &handOffEnd{ended: func() { time.AfterFunc(3*beat, func() { silence.to.Store(0) }) }}
	c := newClusterWith(t, &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{0x70, 0x01, 1}, size: 3,
		configure: func(_ *testNode, cfg *Config) {
			cfg.Transport = &silencedTransport{MemoryTransport: cfg.Transport.(*MemoryTransport), silence: silence}
			cfg.Logger = end
		},
	})
	x := c.settledLeader(5 * time.Second)
	propose(t, x.node, "a=1")
	waitFor(t, "every voter acknowledges the leader's last entry", func() bool { return firstHandOff(x.node) != 0 })
	// From here no leader reaches x: the voter its hand-off goes to wins
	// the term x's TimeoutNow starts, and x does not hear of it.
	silence.to.Store(uint64(x.id))
	removeSelfOnce(t, x, "before it heard of the leader its hand-off elected")
	other := c.otherThan(x)
	waitFor(t, "the old leader removed", func() bool {
		_, member := other.node.Membership().Member(x.id)
		return !member
	})
}
