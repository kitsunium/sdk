package consensus

import (
	"context"
	"encoding/base64"
	"fmt"
	"math/rand/v2"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
)

// tcpNodes are the engines of ns that run.
func tcpNodes(ns []*tcpNode) []*Node {
	var out []*Node
	for _, tn := range ns {
		if tn.node != nil {
			out = append(out, tn.node)
		}
	}
	return out
}

// restartTCP starts tn again on its log and its address.
func restartTCP(t *testing.T, ca *testCA, cid coreconsensus.ClusterID, tn *tcpNode) {
	t.Helper()
	tn.listen(t, ca)
	tn.start(t, cid)
}

// staleVoterSetup is #542's setup over the real mTLS transport: voters A,
// B and C at protocol 2, A stopped, D joined, promoted and handed the
// leadership. It returns A (stopped), the founders and D.
func staleVoterSetup(t *testing.T) (*testCA, coreconsensus.ClusterID, []*tcpNode, *tcpNode) {
	t.Helper()
	return staleVoterSetupWith(t, nil)
}

// staleVoterSetupWith is staleVoterSetup with tweak applied to the
// configuration of every founder and of D.
func staleVoterSetupWith(t *testing.T, tweak func(*Config)) (*testCA, coreconsensus.ClusterID, []*tcpNode, *tcpNode) {
	t.Helper()
	ca, cid, ns := newTCPClusterWith(t, tweak)
	raiseNodes(t, tcpNodes(ns))
	for i := range 5 {
		proposeOnTCPLeader(t, ns, fmt.Sprintf("k%d=%d", i, i))
	}
	a := ns[0]
	if tcpLeader(t, ns) == a {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := a.node.TransferLeadership(ctx, "op:test", ns[1].id)
		cancel()
		if err != nil {
			t.Fatalf("setup: move the leadership off A: %v", err)
		}
	}
	a.stop(t)
	live := ns[1:]
	d := joinTCPWith(t, ca, cid, live, "site-d", tweak)
	proposeOnTCPLeader(t, live, "d=1")
	if !waitKV(t, d, "d", "1", 10*time.Second) {
		t.Fatalf("setup: D never caught up: %+v", d.node.Status())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := tcpLeader(t, live).node.Promote(ctx, "op:test", d.id); err != nil {
		t.Fatalf("setup: promote D: %v", err)
	}
	if err := tcpLeader(t, live).node.TransferLeadership(ctx, "op:test", d.id); err != nil {
		t.Fatalf("setup: transfer to D: %v", err)
	}
	if l := tcpLeader(t, append(append([]*tcpNode(nil), live...), d)); l != d {
		t.Fatalf("setup: D does not lead: %d does", l.id)
	}
	return ca, cid, ns, d
}

// TestHuntR4ProveStaleVoterNewLeader is #542: a voter that was down while
// D was admitted, promoted and handed the leadership comes back with a
// configuration that does not list D. Its transport cut D's appends, B and
// C were followers and sent it nothing, and it stayed pre-candidate for as
// long as D led. B now answers its PreVote with D's record, and A applies a
// write D commits within a bounded time.
func TestHuntR4ProveStaleVoterNewLeader(t *testing.T) {
	ca, cid, ns, d := staleVoterSetup(t)
	a := ns[0]
	restartTCP(t, ca, cid, a)
	all := append(append([]*tcpNode(nil), ns...), d)
	proposeOnTCPLeader(t, all, "b=2")
	if !waitKV(t, a, "b", "2", 4*testTimingFast().ElectionTimeoutMax+5*time.Second) {
		t.Fatalf("the returned voter never applied D's write: %+v", a.node.Status())
	}
	if st := a.node.Status(); st.Leader != d.id {
		t.Fatalf("the returned voter follows %d, want D %d", st.Leader, d.id)
	}
}

// TestHuntR4ProveStaleVoterReplacementThenFailure is #542's strong variant:
// A stopped, D admitted and promoted, C removed and stopped, the leadership
// handed to D, A restarted, then B fails. The configuration is {A, B, D}:
// A and D, two running voters of three, must elect a leader and commit.
// Before the fix A could not hear D and D could not get A's vote: no
// leader, no commit.
func TestHuntR4ProveStaleVoterReplacementThenFailure(t *testing.T) {
	ca, cid, ns, d := staleVoterSetup(t)
	a, b, c := ns[0], ns[1], ns[2]
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	retryAdmin(t, "remove C", func() error { return d.node.Remove(ctx, "op:test", c.id) })
	c.stop(t)
	restartTCP(t, ca, cid, a)
	// B fails a few election timeouts after A's return, without waiting
	// for A to have caught up: what A learns, it learns meanwhile.
	time.Sleep(3 * testTimingFast().ElectionTimeoutMax)
	b.stop(t)
	two := []*tcpNode{a, d}
	proposeOnTCPLeader(t, two, "after=1")
	for _, tn := range two {
		if !waitKV(t, tn, "after", "1", 10*time.Second) {
			t.Fatalf("%d never applied the write of the two running voters: %+v", tn.id, tn.node.Status())
		}
	}
}

// slowSpool is a snapshot store whose spools take per for every write: the
// install of a large state over a slow disk or link, read from the stream
// no faster than it is spooled. streamed is how long the last committed
// spool took from its first write to its commit.
type slowSpool struct {
	coreconsensus.SnapshotStore
	per      time.Duration
	streamed *atomic.Int64
}

func (s slowSpool) Create(id string, meta coreconsensus.SnapshotMetaValue) (coreconsensus.SnapshotSink, int64, error) {
	sink, off, err := s.SnapshotStore.Create(id, meta)
	if err != nil {
		return nil, off, err
	}
	return &slowSink{SnapshotSink: sink, s: s}, off, nil
}

// slowSink is a spool of slowSpool.
type slowSink struct {
	coreconsensus.SnapshotSink
	s     slowSpool
	first time.Time
}

func (k *slowSink) Write(p []byte) (int, error) {
	if k.first.IsZero() {
		k.first = time.Now()
	}
	time.Sleep(k.s.per)
	return k.SnapshotSink.Write(p)
}

func (k *slowSink) Commit(final coreconsensus.SnapshotMetaValue) error {
	k.s.streamed.Store(int64(time.Since(k.first)))
	return k.SnapshotSink.Commit(final)
}

// writeOnTCPLeader proposes small writes to whichever of ns leads, one
// every few milliseconds, until stop is closed, ignoring their outcome:
// the write rate a cluster keeps while a member catches up.
func writeOnTCPLeader(ns []*tcpNode, stop <-chan struct{}) {
	for i := 0; ; i++ {
		select {
		case <-stop:
			return
		case <-time.After(5 * time.Millisecond):
		}
		for _, tn := range ns {
			if n := tn.node; n != nil && n.Status().Role == coreconsensus.RoleLeader {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				_, _, _ = n.Propose(ctx, fmt.Appendf(nil, "w%d=v", i))
				cancel()
				break
			}
		}
	}
}

// TestAStaleVoterWhoseInstallOutlastsTheRecordCatchesUp is #618 over the
// real mTLS transport. The returned voter of #542 admits D on a member's
// record, and D, whose log is compacted past the voter's end, streams it a
// snapshot that takes longer to spool than the record's four maximum
// election timeouts, while the cluster keeps writing and D keeps cutting.
// The record used to end at that delay while D was heard: the voter's
// transport cut D's connections, the snapshot lane among them, the next
// window offered a newer snapshot, which started over, and the voter, a
// voter of D's configuration, never installed. The record now lasts while
// D is heard, and the voter applies a write made after its return.
func TestAStaleVoterWhoseInstallOutlastsTheRecordCatchesUp(t *testing.T) {
	compact := func(c *Config) { c.SnapshotEvery, c.SnapshotTrailing = 8, 1 }
	ca, cid, ns, d := staleVoterSetupWith(t, compact)
	a := ns[0]
	all := append(append([]*tcpNode(nil), ns...), d)
	// About 380 KiB of state once compressed: incompressible values, in
	// base64 so the kv's "key=value" form holds them.
	rng := rand.New(rand.NewPCG(618, 618))
	raw := make([]byte, 24<<10)
	for i := range 16 {
		for k := range raw {
			raw[k] = byte(rng.Uint32())
		}
		proposeOnTCPLeader(t, all, fmt.Sprintf("big%02d=%s", i, base64.StdEncoding.EncodeToString(raw)))
	}
	_, end := a.log.Bounds()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		proposeOnTCPLeader(t, all, "fill=1")
		if first, _ := d.log.Bounds(); first > end+1 {
			break
		}
		if time.Now().After(deadline) {
			first, _ := d.log.Bounds()
			t.Fatalf("setup: D's log still starts at %d, not past A's end %d", first, end)
		}
	}
	// A spools at most 16 KiB, one TLS record, per write: 80 ms each puts
	// the install near 3 s, past the record's 4 x 200 ms.
	streamed := new(atomic.Int64)
	a.tweak = func(c *Config) {
		c.Snapshots = slowSpool{SnapshotStore: c.Snapshots, per: 80 * time.Millisecond, streamed: streamed}
	}
	restartTCP(t, ca, cid, a)
	// The writes run from A's return on, so D cuts a newer snapshot
	// within every window of the record; started after the restart, the
	// writer reads the nodes the test no longer changes.
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		writeOnTCPLeader(all, stop)
	}()
	defer func() {
		close(stop)
		<-done
	}()
	proposeOnTCPLeader(t, all, "b=2")
	if !waitKV(t, a, "b", "2", 20*time.Second) {
		t.Fatalf("the returned voter never applied D's write while its install outlasted the record (%v): %+v",
			4*testTimingFast().ElectionTimeoutMax, a.node.Status())
	}
	if got, bound := time.Duration(streamed.Load()), 4*testTimingFast().ElectionTimeoutMax; got <= bound {
		t.Fatalf("the install took %v, not past the record's delay %v: the test proves nothing", got, bound)
	}
	t.Logf("the install streamed for %v, past the record's delay %v", time.Duration(streamed.Load()), 4*testTimingFast().ElectionTimeoutMax)
}

// The memory double's admission model refuses what the TCP transport
// refuses: a frame from a node its receiver's configuration does not
// list. Under it, the returned voter of #542 still learns the leader.
func TestTheMemoryAdmissionModelRefusesAnUnknownSender(t *testing.T) {
	net := NewMemoryNetwork()
	net.admission = true
	a, b := NewMemoryTransport(net, 1), NewMemoryTransport(net, 2)
	got := make(chan coreconsensus.NodeID, 2)
	if err := a.Bind(1, func(p coreconsensus.AuthenticatedPeerValue, _ []byte) { got <- p.ID }, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	frame := []byte{14, 0, 0, 0, 1, byte(coreconsensus.MsgHeartbeat), 1, 0, 0, 0, 0, 0, 0, 0}
	b.SetMembership(1, []coreconsensus.MemberValue{{ID: 1, SPKI: MemorySPKI(1)}})
	b.Send(1, frame)
	select {
	case id := <-got:
		t.Fatalf("a frame from %d reached a node that does not admit it", id)
	case <-time.After(100 * time.Millisecond):
	}
	a.SetMembership(1, []coreconsensus.MemberValue{{ID: 2, SPKI: MemorySPKI(2)}})
	b.Send(1, frame)
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("a frame from an admitted member was not delivered")
	}
}

// TestHuntR4ProveStaleVoterNewLeaderInMemory is the base case of #542 on
// the memory double with its admission model on, the setup the existing
// suites could not express.
func TestHuntR4ProveStaleVoterNewLeaderInMemory(t *testing.T) {
	net := NewMemoryNetwork()
	net.admission = true
	c := newClusterWith(t, &cluster{t: t, net: net, cid: coreconsensus.ClusterID{5, 4, 2}, size: 3})
	raiseToProtocol2(t, c)
	l := c.settledLeader(5 * time.Second)
	a := c.otherThan(l)
	c.kill(a)
	d := c.join(t, "slot-d")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	retryAdmin(t, "promote D", func() error { return c.leader(5*time.Second).node.Promote(ctx, "op:test", d.id) })
	retryAdmin(t, "transfer to D", func() error { return c.leader(5*time.Second).node.TransferLeadership(ctx, "op:test", d.id) })
	if l := c.leader(5 * time.Second); l != d {
		t.Fatalf("setup: D does not lead: %d does", l.id)
	}
	c.start(a)
	tok := propose(t, d.node, "b=2")
	for deadline := time.Now().Add(4*testTimingFast().ElectionTimeoutMax + 5*time.Second); a.node.Status().Applied < tok.Index; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the returned voter never applied D's write: %+v", a.node.Status())
		}
	}
	if st := a.node.Status(); st.Leader != d.id {
		t.Fatalf("the returned voter follows %d, want D %d", st.Leader, d.id)
	}
}

// raiseOn proposes the raise to protocol 2 on n with n's own precondition.
func raiseOn(n *Node) error {
	st := n.Status()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return n.RaiseClusterVersion(ctx, "op:raise", coreconsensus.PreconditionSpec{Log: st.Log, Generation: st.Versions.ClusterGeneration}, raft.Protocol2)
}

// #638: #542's stale voter in a cluster that never raised its version
// (Forgejo's, kodflow/forgejo-raft#170). A was down while D was admitted,
// promoted and handed the leadership, and comes back with a configuration
// that does not list D. The leader record went out only at cluster version
// 2, and D refused the raise VERSION_UNSUPPORTED for as long as it led:
// checkAnnounced wants every member heard in the term, and A, whose
// transport refused D's key, never answered D. The record now follows the
// PreVote's own announcement of protocol 2: A hears D at cluster version 0,
// applies its writes, answers it, and the raise passes while D leads.
func TestAStaleVoterHearsTheNewLeaderBeforeTheRaise(t *testing.T) {
	net := NewMemoryNetwork()
	net.admission = true
	c := newClusterWith(t, &cluster{t: t, net: net, cid: coreconsensus.ClusterID{5, 4, 9}, size: 3})
	l := c.settledLeader(5 * time.Second)
	a := c.otherThan(l)
	c.kill(a)
	net.Isolate(a.id, true)
	d := c.join(t, "slot-d")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	retryAdmin(t, "promote D", func() error { return c.leader(5*time.Second).node.Promote(ctx, "op:test", d.id) })
	retryAdmin(t, "transfer to D", func() error { return c.leader(5*time.Second).node.TransferLeadership(ctx, "op:test", d.id) })
	if ld := c.leader(5 * time.Second); ld != d {
		t.Fatalf("setup: D does not lead: %d does", ld.id)
	}
	if v := d.node.Status().Versions.Cluster; v >= raft.Protocol2 {
		t.Fatalf("setup: cluster version %d, want below 2", v)
	}
	net.Isolate(a.id, false)
	c.start(a)
	tok := propose(t, d.node, "b=2")
	for deadline := time.Now().Add(4*testTimingFast().ElectionTimeoutMax + 5*time.Second); a.node.Status().Applied < tok.Index; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Errorf("the returned voter never applied D's write at cluster version %d: %+v", d.node.Status().Versions.Cluster, a.node.Status())
			break
		}
	}
	var err error
	tries, refused := 0, 0
	for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if d.node.Status().Role != coreconsensus.RoleLeader {
			continue
		}
		tries++
		if err = raiseOn(d.node); err == nil {
			break
		}
		refused++
	}
	if tries == 0 || refused == tries {
		t.Fatalf("the raise was refused at every one of %d tries for 4s while D leads: %v; A: %+v", tries, err, a.node.Status())
	}
}

// join starts a new node on slot and sends its JOIN through the memory
// network, retried until a member admits it as a learner.
func (c *cluster) join(t *testing.T, slot string) *testNode {
	t.Helper()
	tn := &testNode{log: NewMemoryLog(), snaps: NewMemorySnapshots(), sm: newKV()}
	id, _, err := InitLog(tn.log, c.cid, slot)
	if err != nil {
		t.Fatal(err)
	}
	tn.id = id
	c.nodes = append(c.nodes, tn)
	c.start(tn)
	st := must(tn.log.Stamp())(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		_, err := NewMemoryTransport(c.net, id).Join(ctx, coreconsensus.JoinConfig{Stamp: st, Address: "mem"})
		if err == nil {
			return tn
		}
		if ctx.Err() != nil {
			t.Fatalf("join %s: %v", slot, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// retryAdmin runs op until it succeeds, for up to ten seconds: a change
// is refused while the previous one, or a new leader's no-op, is pending,
// and a promotion while the learner has not caught up.
func retryAdmin(t *testing.T, what string, op func() error) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := op()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("setup: %s: %v", what, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// #639: a learner down while D was admitted, promoted and handed the
// leadership comes back with a configuration that does not list D, and its
// transport refuses D's key. #542's record went out only in answer to a
// PreVote, and a learner never campaigns: nothing vouched for D, and the
// learner was never fed while D led, at cluster version 2 too. A learner
// that hears no leader now asks the members of its configuration for the
// record of the leader they follow, with a PreVote it never counts. The
// control, without the admission model, feeds it either way.
func TestALearnerDownWhileANewLeaderWasAdmittedIsFed(t *testing.T) {
	type tc struct {
		name             string
		admission, raise bool
		cid              coreconsensus.ClusterID
	}
	runCase := func(t *testing.T, c tc) {
		if fed, st := staleLearner(t, c.admission, c.raise, c.cid); !fed {
			t.Fatalf("the returned learner never applied D's write: %+v", st)
		}
	}
	for _, c := range []tc{
		{"cluster version 2", true, true, coreconsensus.ClusterID{5, 4, 10}},
		{"cluster version 0", true, false, coreconsensus.ClusterID{5, 4, 12}},
		{"control: every sender admitted", false, true, coreconsensus.ClusterID{5, 4, 11}},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// staleLearner runs #639's scenario and reports whether the learner applied
// D's write within four maximum election timeouts and five seconds.
func staleLearner(t *testing.T, admission, raise bool, cid coreconsensus.ClusterID) (bool, coreconsensus.StatusValue) {
	t.Helper()
	net := NewMemoryNetwork()
	net.admission = admission
	c := newClusterWith(t, &cluster{t: t, net: net, cid: cid, size: 3})
	if raise {
		raiseToProtocol2(t, c)
	}
	l := c.settledLeader(5 * time.Second)
	x := c.join(t, "slot-x")
	tok := propose(t, l.node, "a=1")
	for deadline := time.Now().Add(10 * time.Second); x.node.Status().Applied < tok.Index; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("setup: the learner never caught up: %+v", x.node.Status())
		}
	}
	c.kill(x)
	net.Isolate(x.id, true)
	d := c.join(t, "slot-d")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	retryAdmin(t, "promote D", func() error { return c.leader(5*time.Second).node.Promote(ctx, "op:test", d.id) })
	retryAdmin(t, "transfer to D", func() error { return c.leader(5*time.Second).node.TransferLeadership(ctx, "op:test", d.id) })
	if ld := c.leader(5 * time.Second); ld != d {
		t.Fatalf("setup: D does not lead: %d does", ld.id)
	}
	net.Isolate(x.id, false)
	c.start(x)
	tok = propose(t, d.node, "b=2")
	wait := 4*testTimingFast().ElectionTimeoutMax + 5*time.Second
	for deadline := time.Now().Add(wait); x.node.Status().Applied < tok.Index; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			return false, x.node.Status()
		}
	}
	return true, x.node.Status()
}
