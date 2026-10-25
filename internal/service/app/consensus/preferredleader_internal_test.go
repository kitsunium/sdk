package consensus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// waitLeaderIs waits until want leads, and fails naming the leader it got.
func waitLeaderIs(t *testing.T, c *cluster, want *testNode, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if want.node != nil && want.node.Status().Role == coreconsensus.RoleLeader {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("leadership stayed on %d for %v; PreferredLeaders names %d", c.leader(time.Second).id, within, want.id)
}

// TestHuntR8ProvePreferredLeaderTransfersAfterElection is #559:
// preferLeader ran once, on the Ready where the node became leader, when
// becomeLeader had just reset every peer's match to 0 below a commit of at
// least 1. The guard never held and nothing called it again, so
// PreferredLeaders never moved leadership. The leader now re-evaluates it
// at its ticks, so leadership reaches the preferred node within a few
// election timeouts whichever node wins.
func TestHuntR8ProvePreferredLeaderTransfersAfterElection(t *testing.T) {
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 5, 9}, size: 3}
	c.configure = func(_ *testNode, cfg *Config) { cfg.PreferredLeaders = []coreconsensus.NodeID{c.nodes[2].id} }
	newClusterWith(t, c)
	preferred := c.nodes[2]
	if c.settledLeader(5*time.Second) == preferred {
		// Make a non-preferred node win: the preferred one is away during
		// the election, then comes back.
		c.kill(preferred)
		c.settledLeader(5 * time.Second)
		c.start(preferred)
	}
	waitLeaderIs(t, c, preferred, 2*time.Second)
	propose(t, preferred.node, "after=transfer")
}

// TestHuntR8ProvePreferredLeaderAfterFullRestart is #559's second proof:
// after a full restart, with the preferred node back last, leadership
// stayed on whichever node the other two elected.
func TestHuntR8ProvePreferredLeaderAfterFullRestart(t *testing.T) {
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 5, 10}, size: 3}
	newClusterWith(t, c)
	propose(t, c.settledLeader(5*time.Second).node, "before=restart")
	preferred := c.otherThan(c.leader(time.Second))
	for _, tn := range c.nodes {
		c.kill(tn)
	}
	c.configure = func(_ *testNode, cfg *Config) { cfg.PreferredLeaders = []coreconsensus.NodeID{preferred.id} }
	for _, tn := range c.nodes {
		if tn != preferred {
			c.start(tn)
		}
	}
	if c.settledLeader(5*time.Second) == preferred {
		t.Fatal("the preferred node led while it was down")
	}
	c.start(preferred)
	waitLeaderIs(t, c, preferred, 2*time.Second)
}

// timeoutNowLoss loses, on every node's memory transport, the TimeoutNow
// frames sent to one node: the preferred node of the tests below, whose
// transfers then never complete. It counts the frames it lost.
type timeoutNowLoss struct {
	// to is the node whose TimeoutNow frames are lost; 0 loses none.
	to atomic.Uint64
	// lost counts the frames lost so far.
	lost atomic.Int64
}

// lossyTimeoutNow is one node's memory transport under a timeoutNowLoss.
type lossyTimeoutNow struct {
	*MemoryTransport
	loss *timeoutNowLoss
}

// Send drops a TimeoutNow to the node the loss names, and sends the rest.
func (t *lossyTimeoutNow) Send(to coreconsensus.NodeID, frame []byte) {
	if uint64(to) == t.loss.to.Load() {
		var m coreconsensus.MessageValue
		if err := wire.ParseMessage(append([]byte(nil), frame...), &m); err == nil && m.Kind == coreconsensus.MsgTimeoutNow {
			t.loss.lost.Add(1)
			return
		}
	}
	t.MemoryTransport.Send(to, frame)
}

// wrap puts tr, a memory transport, under the loss.
func (l *timeoutNowLoss) wrap(tr coreconsensus.Transport) coreconsensus.Transport {
	return &lossyTimeoutNow{MemoryTransport: tr.(*MemoryTransport), loss: l}
}

// waitLost waits until the loss has lost more than past frames: a leader
// started a transfer to the node it names, which will not complete.
func (l *timeoutNowLoss) waitLost(t *testing.T, past int64, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for l.lost.Load() <= past {
		if time.Now().After(deadline) {
			t.Fatalf("no transfer to the preferred node %d was started within %v", l.to.Load(), within)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// preferredCluster starts size voters, each with PreferredLeaders naming
// the nodes at positions prefs, under one timeoutNowLoss that loses
// nothing yet.
func preferredCluster(t *testing.T, cid byte, size int, prefs ...int) (*cluster, *timeoutNowLoss) {
	loss := new(timeoutNowLoss)
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 5, cid}, size: size}
	c.configure = func(_ *testNode, cfg *Config) {
		cfg.PreferredLeaders = nil
		for _, i := range prefs {
			cfg.PreferredLeaders = append(cfg.PreferredLeaders, c.nodes[i].id)
		}
		cfg.Transport = loss.wrap(cfg.Transport)
	}
	return newClusterWith(t, c), loss
}

// leadWithout makes a node other than p lead: p is away during the
// election when it led, then comes back. It returns the leader.
func leadWithout(c *cluster, p *testNode) *testNode {
	if l := c.leader(5 * time.Second); l != p {
		return l
	}
	c.kill(p)
	l := c.leader(5 * time.Second)
	c.start(p)
	return l
}

// otherVoter returns the node of c that is neither a nor p.
func otherVoter(c *cluster, a, p *testNode) *testNode {
	for _, tn := range c.nodes {
		if tn != a && tn != p {
			return tn
		}
	}
	c.t.Fatal("no third node")
	return nil
}

// retryRefused runs op until it is not refused NOT_LEADER, nor
// MEMBERSHIP_CHANGE_PENDING while a new leader's no-op is pending, nor one
// of also, for at most within, the way an operator retries a refusal that
// may pass, and returns the refusals it met and its last error. Before
// #662's fix every call was refused NOT_LEADER until the deadline.
func retryRefused(within time.Duration, op func(ctx context.Context) error, also ...error) (int, error) {
	deadline := time.Now().Add(within)
	refused := 0
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := op(ctx)
		cancel()
		retryable := errors.Is(err, coreconsensus.NotLeader) || errors.Is(err, coreconsensus.MembershipChangePending)
		for _, a := range also {
			retryable = retryable || errors.Is(err, a)
		}
		if !retryable || !time.Now().Before(deadline) {
			return refused, err
		}
		refused++
		time.Sleep(10 * time.Millisecond)
	}
}

// removeRetried removes id through n, retried as retryRefused does. A
// removal is also refused MEMBERSHIP_INVALID until the voters left answer
// the leader by a majority within the minimum election timeout, which a
// loaded runner misses for a moment after a change of leader: a write the
// leader commits proves nothing there, the voter removed may have
// acknowledged it.
func removeRetried(n *Node, id coreconsensus.NodeID) (int, error) {
	return retryRefused(5*time.Second, func(ctx context.Context) error {
		return n.Remove(ctx, "op:test", id)
	}, coreconsensus.MembershipInvalid)
}

// transferRetried moves leadership to id through n, retried as
// retryRefused does, and on TRANSFER_FAILED, which a loaded runner meets
// when the target takes more than twice the maximum election timeout.
func transferRetried(n *Node, id coreconsensus.NodeID) (int, error) {
	return retryRefused(5*time.Second, func(ctx context.Context) error {
		return n.TransferLeadership(ctx, "op:test", id)
	}, coreconsensus.TransferFailed)
}

// TestAPreferredNodeLostBeforeTakingOverLeavesTheMajorityWriting is #662:
// three voters, every one with PreferredLeaders = [P]. A non-preferred
// node A leads, P catches up, and P dies before the transfer to it
// completes (its TimeoutNow is lost, then P is killed for good). The
// leader started a transfer to P again at the very tick the previous one
// expired, on a match that still held the commit since nothing could
// commit, and every proposal is refused NOT_LEADER while a transfer runs:
// no write, not even the Remove of P nor a transfer to the healthy voter,
// went through for as long as A led. A silent preferred node is no target
// any more, so the healthy majority goes on writing.
func TestAPreferredNodeLostBeforeTakingOverLeavesTheMajorityWriting(t *testing.T) {
	c, loss := preferredCluster(t, 0x6b, 3, 2)
	p := c.nodes[2]
	loss.to.Store(uint64(p.id))
	leadWithout(c, p)
	loss.waitLost(t, 0, 10*time.Second)
	// P dies now, for good; the network is honest from here on.
	c.kill(p)
	loss.to.Store(0)
	a := c.leader(time.Second)
	if a == p {
		t.Fatal("the preferred node leads")
	}
	b := otherVoter(c, a, p)
	term0 := a.node.Status().Term
	t.Logf("leader A=%d term %d, B=%d, preferred P=%d down", a.id, term0, b.id, p.id)

	// For ten maximum election timeouts, write through A and through B.
	var ok, notLeader, other, transferSeen, samples int
	until := time.Now().Add(2 * time.Second)
	for i := 0; time.Now().Before(until); i++ {
		n := a.node
		if i%2 == 1 {
			n = b.node
		}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		_, _, err := n.Propose(ctx, []byte("k=v"))
		cancel()
		switch {
		case err == nil:
			ok++
		case errors.Is(err, coreconsensus.NotLeader):
			notLeader++
		default:
			other++
			t.Logf("propose: %v", err)
		}
		var tr coreconsensus.NodeID
		a.node.exec(func() { tr = a.node.r.Status().Transfer })
		samples++
		if tr == p.id {
			transferSeen++
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Logf("2s with P down: %d writes committed, %d refused NOT_LEADER, %d other; A transferring to P in %d/%d samples",
		ok, notLeader, other, transferSeen, samples)
	if ok == 0 {
		t.Fatalf("a healthy majority (A, B) committed no write in 2 s, %d refused NOT_LEADER: A restarts a transfer to the dead preferred node at every expiry", notLeader)
	}
	refused, err := removeRetried(a.node, p.id)
	if err != nil {
		t.Fatalf("Remove of the dead preferred node, after %d refusals: %v", refused, err)
	}
	refused, err = transferRetried(a.node, b.id)
	if err != nil {
		t.Fatalf("TransferLeadership to the healthy voter, after %d refusals: %v", refused, err)
	}
	propose(t, b.node, "after=remove")
}

// TestAPreferredNodeThatCannotTakeOverIsTriedAgainOnlyAfterAPause is
// #662's second half: P stays up and heard, but every TimeoutNow sent to
// it is lost, so no transfer to it completes. The next transfer used to
// start at the tick the previous one expired, leaving no turn of the loop
// in which a proposal could be appended. Now each one waits a pause of at
// least the maximum election timeout after the previous one ended, in
// which the operator's TransferLeadership to a healthy voter, then the
// Remove of P, go through.
func TestAPreferredNodeThatCannotTakeOverIsTriedAgainOnlyAfterAPause(t *testing.T) {
	c, loss := preferredCluster(t, 0x6c, 3, 2)
	p := c.nodes[2]
	loss.to.Store(uint64(p.id))
	leadWithout(c, p)
	loss.waitLost(t, 0, 10*time.Second)
	a := c.leader(time.Second)
	if a == p {
		t.Fatal("the preferred node leads")
	}
	b := otherVoter(c, a, p)
	st0 := a.node.Status()
	etMax := st0.Timing.ElectionTimeoutMax

	// Watch A's transfers for 1.5 s, sampled on its loop and timed there:
	// a sample that sees a transfer runs before the tick that ends it, and
	// the first that sees the next one after the tick that starts it, so
	// each gap measured is longer than the real one.
	var gaps []time.Duration
	var lastSeen time.Time
	inGap := false
	until := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(until) {
		var rs struct {
			at       time.Time
			role     coreconsensus.Role
			term     uint64
			transfer coreconsensus.NodeID
		}
		a.node.exec(func() {
			s := a.node.r.Status()
			rs.at, rs.role, rs.term, rs.transfer = time.Now(), s.Role, s.Term, s.Transfer
		})
		now := rs.at
		if rs.role != coreconsensus.RoleLeader || rs.term != st0.Term {
			t.Fatalf("A lost its leadership while watching its transfers: %v at term %d", rs.role, rs.term)
		}
		switch {
		case rs.transfer == p.id && inGap:
			gaps = append(gaps, now.Sub(lastSeen))
			inGap = false
			lastSeen = now
		case rs.transfer == p.id:
			lastSeen = now
		case !lastSeen.IsZero():
			inGap = true
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Logf("A=%d, B=%d, P=%d heard but deaf to TimeoutNow: gaps between transfers %v, %d TimeoutNow lost", a.id, b.id, p.id, gaps, loss.lost.Load())
	if len(gaps) == 0 {
		t.Fatalf("A ran transfers to P back to back for 1.5 s: no pause between two of them")
	}
	for _, g := range gaps {
		if g < etMax {
			t.Errorf("a transfer to P started %v after the previous one ended, under the maximum election timeout %v", g, etMax)
		}
	}

	refused, err := transferRetried(a.node, b.id)
	if err != nil {
		t.Fatalf("TransferLeadership to the healthy voter, after %d refusals: %v", refused, err)
	}
	t.Logf("TransferLeadership to B went through after %d refusals", refused)
	// B transfers to P in turn; a write and the Remove of P go through
	// between two of its transfers.
	propose(t, b.node, "on=b")
	refused, err = removeRetried(b.node, p.id)
	if err != nil {
		t.Fatalf("Remove of the preferred node, after %d refusals: %v", refused, err)
	}
	t.Logf("Remove of P went through after %d refusals", refused)
	propose(t, b.node, "after=remove")
}

// TestAPreferredNodeTakesOverOnceATransferToItWasLost keeps #559 under
// #662's pause: a transfer to a healthy preferred node whose TimeoutNow was
// lost once is tried again, and the node takes over.
func TestAPreferredNodeTakesOverOnceATransferToItWasLost(t *testing.T) {
	c, loss := preferredCluster(t, 0x6d, 3, 2)
	p := c.nodes[2]
	loss.to.Store(uint64(p.id))
	leadWithout(c, p)
	loss.waitLost(t, 0, 10*time.Second)
	loss.to.Store(0)
	waitLeaderIs(t, c, p, 3*time.Second)
	propose(t, p.node, "after=transfer")
}

// TestAPreferredNodeThatCannotTakeOverYieldsToTheNextOne is #662 with two
// preferred nodes, P then Q: the leader used to pick the first one that
// held the commit, P, again at every expiry, so the healthy Q never led.
// Whether P died after catching up or stays up but deaf to TimeoutNow, Q
// now takes over.
func TestAPreferredNodeThatCannotTakeOverYieldsToTheNextOne(t *testing.T) {
	type tc struct {
		name string
		// cid is the case's own cluster id.
		cid byte
		// dies is whether P dies after catching up; otherwise it stays up,
		// deaf to TimeoutNow.
		dies bool
	}
	runCase := func(t *testing.T, c tc) {
		cl, loss := preferredCluster(t, c.cid, 5, 3, 4)
		p, q := cl.nodes[3], cl.nodes[4]
		cl.settledLeader(5 * time.Second)
		// A non-preferred node leads, and its commit passes what P and Q
		// hold: neither is a target until it catches up.
		cl.kill(p)
		cl.kill(q)
		a := cl.settledLeader(5 * time.Second)
		loss.to.Store(uint64(p.id))
		cl.start(p)
		loss.waitLost(t, 0, 10*time.Second)
		if c.dies {
			cl.kill(p)
			loss.to.Store(0)
		}
		cl.start(q)
		t.Logf("leader A=%d, P=%d (dies %v), Q=%d", a.id, p.id, c.dies, q.id)
		waitLeaderIs(t, cl, q, 5*time.Second)
		propose(t, q.node, "after=transfer")
	}
	for _, c := range []tc{
		{name: "dead", cid: 0x6f, dies: true},
		{name: "deaf", cid: 0x6e},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestAStuckPreferredLeaderIsNotHandedLeadershipBack is #674: three voters,
// every one with PreferredLeaders = [L], and L's Apply blocks while its log
// and its loop go on. #554 hands L's leadership to the most caught-up voter
// X; X's preferred watch, started over with the leadership, saw L hold the
// commit (a stuck apply still acknowledges) and transferred back at its
// first tick, and L led about 99 % of the time: every write entering at it
// hung, and once its apply lag passed MaxApplyLagBytes every write of the
// cluster was refused OVERLOADED. A node whose apply makes no progress now
// declines a transfer to itself, so X keeps the leadership and the others'
// writes commit.
func TestAStuckPreferredLeaderIsNotHandedLeadershipBack(t *testing.T) {
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 17, 11}, size: 3}
	c.configure = func(_ *testNode, cfg *Config) {
		cfg.MaxApplyLagBytes = 16 << 10
		cfg.PreferredLeaders = []coreconsensus.NodeID{c.nodes[0].id}
	}
	newClusterWith(t, c)
	stuck := c.nodes[0]
	waitLeaderIs(t, c, stuck, 5*time.Second)
	c.settledLeader(5 * time.Second)
	propose(t, stuck.node, "a=1")
	// The stuck node's machine blocks in Apply from now on.
	stuck.sm.mu.Lock()
	var once sync.Once
	release := func() { once.Do(stuck.sm.mu.Unlock) }
	defer release()
	etMax := testTimingFast().ElectionTimeoutMax
	// Let #554's watch act once.
	time.Sleep(4 * etMax)
	var mu sync.Mutex
	samples, stuckLeads := 0, 0
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
			mu.Lock()
			samples++
			if stuck.node.Status().Role == coreconsensus.RoleLeader {
				stuckLeads++
			}
			mu.Unlock()
		}
	})
	// Writes enter at the other nodes only, 1 KiB each.
	ok, codes := 0, map[string]int{}
	payload := bytes.Repeat([]byte{'x'}, 1<<10)
	end := time.Now().Add(10 * etMax)
	var others []*testNode
	for _, tn := range c.nodes {
		if tn != stuck {
			others = append(others, tn)
		}
	}
	for i := 0; time.Now().Before(end); i++ {
		tn := others[i%2]
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		_, _, err := tn.node.Propose(ctx, append(fmt.Appendf(nil, "k%d=", i), payload...))
		cancel()
		if err == nil {
			ok++
		} else {
			code, _ := errs.CodeOf(err)
			codes[fmt.Sprint(code)]++
			time.Sleep(5 * time.Millisecond)
		}
	}
	close(stop)
	wg.Wait()
	mu.Lock()
	share := float64(stuckLeads) / float64(max(samples, 1))
	mu.Unlock()
	t.Logf("over %v: the stuck node led %.0f%% of %d samples; writes at the others: %d ok, refusals %v",
		10*etMax, 100*share, samples, ok, codes)
	if share > 0.5 {
		t.Errorf("the node whose apply is stuck led %.0f%% of the time after #554's hand-over: %d writes ok, refusals %v",
			100*share, ok, codes)
	}
	if ok == 0 {
		t.Errorf("no write entering at the healthy nodes committed: refusals %v", codes)
	}
}

// TestAPreferredNodeHandedOverByATransferLeadsAgainAfterTheWindow keeps
// #559 under #676's pause: an operator moves leadership off the healthy
// preferred node P. The node that took over by that transfer hands nothing
// back for the hand-off's window, in which a Remove of P would go through,
// and P leads again once the window ended.
func TestAPreferredNodeHandedOverByATransferLeadsAgainAfterTheWindow(t *testing.T) {
	c, _ := preferredCluster(t, 0x70, 3, 0)
	p := c.nodes[0]
	waitLeaderIs(t, c, p, 5*time.Second)
	c.settledLeader(5 * time.Second)
	x := c.otherThan(p)
	if refused, err := transferRetried(p.node, x.id); err != nil {
		t.Fatalf("TransferLeadership to %d, after %d refusals: %v", x.id, refused, err)
	}
	var tookOver time.Time
	x.node.exec(func() { tookOver = time.Now() })
	window := handOffWindow * testTimingFast().ElectionTimeoutMax
	waitLeaderIs(t, c, p, window+5*time.Second)
	back := time.Since(tookOver)
	t.Logf("%d took over from the preferred node %d, which led again %v later (window %v)", x.id, p.id, back, window)
	// The transfer back starts at the first tick past the window, and the
	// wait above sees its end; the slack covers a tick and the sampling.
	if back < window-20*time.Millisecond {
		t.Errorf("leadership went back to the preferred node %v after the transfer, inside the hand-off's window %v", back, window)
	}
	propose(t, p.node, "after=window")
}
