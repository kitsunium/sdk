package consensus

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// syncFailingSnapshots is a snapshot store whose every Commit fails as a
// failed fsync does: the spool was written, and the disk would not make it
// durable.
type syncFailingSnapshots struct{ *MemorySnapshots }

func (s syncFailingSnapshots) Create(id string, meta coreconsensus.SnapshotMetaValue) (coreconsensus.SnapshotSink, int64, error) {
	sink, off, err := s.MemorySnapshots.Create(id, meta)
	if err != nil {
		return nil, 0, err
	}
	return syncFailingSink{sink}, off, nil
}

// syncFailingSink is a spool whose Commit reports STORAGE_FAILED.
type syncFailingSink struct{ coreconsensus.SnapshotSink }

func (syncFailingSink) Commit(coreconsensus.SnapshotMetaValue) error {
	return storageFailed("sync snapshot", errImpossible)
}

// removeFailingSnapshots is a snapshot store whose every Remove fails as a
// failed unlink or directory sync does.
type removeFailingSnapshots struct{ *MemorySnapshots }

func (removeFailingSnapshots) Remove(string) error {
	return storageFailed("remove", errImpossible)
}

// TestARetentionWhoseRemoveFailsStopsTheNode pins D4 on retention (#422): a
// cut that leaves three snapshots removes the oldest, and a failed unlink or
// directory sync there is this node's disk failing, which stops the node as
// a failed cut does. retain used to log it and go on.
func TestARetentionWhoseRemoveFailsStopsTheNode(t *testing.T) {
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{4, 5, 10}, every: 5, size: 1}
	c.configure = func(tn *testNode, cfg *Config) { cfg.Snapshots = removeFailingSnapshots{tn.snaps} }
	newClusterWith(t, c)
	n := c.leader(5 * time.Second).node
	for i := 0; i < 40; i++ {
		if _, stopped := n.StopReason(); stopped {
			break
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		// Only the cuts these entries trigger matter: an entry the stop
		// refuses is no failure of the test.
		_, _, _ = n.Propose(ctx, fmt.Appendf(nil, "k%d=%d", i, i))
		cancel()
	}
	waitFor(t, "the node stops on the failed removal", func() bool {
		_, ok := n.StopReason()
		return ok
	})
	if r, _ := n.StopReason(); !errs.HasCode(r.Err, coreconsensus.CodeStorageFailed) || r.ExitCode() != 74 {
		t.Fatalf("stopped on %v (exit %d), want STORAGE_FAILED (exit 74)", r.Err, r.ExitCode())
	}
}

// gatedSnapshots is a snapshot store whose Create waits for gate: a cut stays
// in flight until the test lets it be written.
type gatedSnapshots struct {
	*MemorySnapshots
	gate chan struct{}
}

func (s gatedSnapshots) Create(id string, meta coreconsensus.SnapshotMetaValue) (coreconsensus.SnapshotSink, int64, error) {
	<-s.gate
	return s.MemorySnapshots.Create(id, meta)
}

// TestACoalescedCutIsMade pins D7's "a trigger during one is coalesced": the
// entries applied while a cut is in flight cross SnapshotEvery again, and the
// cut they trigger is made once the first is written, without one more entry
// applied. It used to be dropped: a node that applied nothing more kept the
// tail uncut, and TestAnInstallKeepsTheTwoNewestSnapshots waited in vain for
// a follower's second snapshot (#375).
func TestACoalescedCutIsMade(t *testing.T) {
	cid := coreconsensus.ClusterID{4, 5, 7}
	log, net := NewMemoryLog(), NewMemoryNetwork()
	id, inc, err := InitLog(log, cid, "slot-0")
	if err != nil {
		t.Fatal(err)
	}
	members := []coreconsensus.MemberValue{{ID: id, Incarnation: inc, Slot: "slot-0", SPKI: MemorySPKI(id), Voter: true}}
	snaps := gatedSnapshots{NewMemorySnapshots(), make(chan struct{})}
	cfg := Config{
		Cluster: cid, Machine: newKV(), Durability: DurabilityVolatile, Log: log,
		Snapshots: snaps, Transport: NewMemoryTransport(net, id),
		InitialTiming: testTimingFast(), MaxSnapshotBytes: 1 << 30, SnapshotEveryBytes: 1 << 20,
		MaxLogBytes: 1 << 30, LogReserveBytes: 1 << 20, MinVoters: 1, SnapshotEvery: 10,
		SnapshotTrailing: 1, JoinAuthorizer: allowJoin{},
	}
	if err := Bootstrap(context.Background(), &cfg, members); err != nil {
		t.Fatal(err)
	}
	n, err := NewNode(cfg)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	release := func() {
		if !released {
			released = true
			close(snaps.gate)
		}
	}
	// Registered after the node's, so it runs first: a cut still waiting
	// would hold HardClose.
	t.Cleanup(func() {
		if err := n.HardClose(); err != nil {
			t.Error(err)
		}
	})
	t.Cleanup(release)
	var last uint64
	for i := range 30 {
		last = propose(t, n, string([]byte{'k', '=', byte('a' + i%26)})).Index
	}
	deadline := time.Now().Add(10 * time.Second)
	for n.Status().Applied < last {
		if time.Now().After(deadline) {
			t.Fatalf("applied %d < %d", n.Status().Applied, last)
		}
		time.Sleep(time.Millisecond)
	}
	// Every entry is applied behind the cut in flight; from now on nothing
	// is proposed.
	release()
	deadline = time.Now().Add(10 * time.Second)
	for {
		metas, err := snaps.List()
		if err != nil {
			t.Fatal(err)
		}
		newest := uint64(0)
		if len(metas) > 0 {
			newest = metas[0].Position.Index
		}
		if newest == last {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the node holds %d snapshots, the newest at %d, none at the applied index %d", len(metas), newest, last)
		}
		time.Sleep(time.Millisecond)
	}
}

// failingCommitSnapshots is a gated snapshot store whose Commit fails, not
// as a failed sync: the node logs it and goes on.
type failingCommitSnapshots struct{ gatedSnapshots }

func (s failingCommitSnapshots) Create(id string, meta coreconsensus.SnapshotMetaValue) (coreconsensus.SnapshotSink, int64, error) {
	sink, off, err := s.gatedSnapshots.Create(id, meta)
	if err != nil {
		return nil, 0, err
	}
	return failingCommitSink{sink}, off, nil
}

// failingCommitSink is a spool whose Commit reports a corrupt snapshot.
type failingCommitSink struct{ coreconsensus.SnapshotSink }

func (failingCommitSink) Commit(coreconsensus.SnapshotMetaValue) error {
	return snapshotCorrupt("refused by the test")
}

// A trigger coalesced during a cut whose write then fails is cleared with
// the in-flight flag: the next trigger tries again, and the failure does not
// leave a stale one that would ask for a cut nothing triggered since.
func TestAFailedCutClearsTheCoalescedTrigger(t *testing.T) {
	cid := coreconsensus.ClusterID{4, 5, 8}
	log, net := NewMemoryLog(), NewMemoryNetwork()
	id, inc, err := InitLog(log, cid, "slot-0")
	if err != nil {
		t.Fatal(err)
	}
	members := []coreconsensus.MemberValue{{ID: id, Incarnation: inc, Slot: "slot-0", SPKI: MemorySPKI(id), Voter: true}}
	snaps := failingCommitSnapshots{gatedSnapshots{NewMemorySnapshots(), make(chan struct{})}}
	cfg := Config{
		Cluster: cid, Machine: newKV(), Durability: DurabilityVolatile, Log: log,
		Snapshots: snaps, Transport: NewMemoryTransport(net, id),
		InitialTiming: testTimingFast(), MaxSnapshotBytes: 1 << 30, SnapshotEveryBytes: 1 << 20,
		MaxLogBytes: 1 << 30, LogReserveBytes: 1 << 20, MinVoters: 1, SnapshotEvery: 10,
		SnapshotTrailing: 1, JoinAuthorizer: allowJoin{},
	}
	if err := Bootstrap(context.Background(), &cfg, members); err != nil {
		t.Fatal(err)
	}
	n, err := NewNode(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() { once.Do(func() { close(snaps.gate) }) }
	t.Cleanup(func() {
		if err := n.HardClose(); err != nil {
			t.Error(err)
		}
	})
	t.Cleanup(release)
	var last uint64
	for i := range 30 {
		last = propose(t, n, string([]byte{'k', '=', byte('a' + i%26)})).Index
	}
	snapState := func() (inFlight, pending bool) {
		n.snapState.mu.Lock()
		defer n.snapState.mu.Unlock()
		return n.snapState.inFlight, n.snapState.pending
	}
	deadline := time.Now().Add(10 * time.Second)
	for n.Status().Applied < last {
		if time.Now().After(deadline) {
			t.Fatalf("applied %d < %d", n.Status().Applied, last)
		}
		time.Sleep(time.Millisecond)
	}
	if inFlight, pending := snapState(); !inFlight || !pending {
		t.Fatalf("setup: in flight %v, pending %v, want a cut in flight and a trigger coalesced", inFlight, pending)
	}
	release()
	deadline = time.Now().Add(10 * time.Second)
	for {
		inFlight, pending := snapState()
		if !inFlight {
			if pending {
				t.Fatal("the failed cut left its coalesced trigger behind")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the failing cut never ended")
		}
		time.Sleep(time.Millisecond)
	}
	if _, stopped := n.StopReason(); stopped {
		t.Fatal("a commit that is not a failed sync stopped the node")
	}
}

// TestACutWhoseSyncFailsStopsTheNode pins D4 on the snapshot path: a failed
// fsync is never retried, and that holds for a snapshot's spool as for the
// log, since both live on the one disk whose page cache may now lie. The cut
// runs aside, or on the apply goroutine when the machine cannot cut cheaply,
// and either way the node stops with STORAGE_FAILED instead of carrying on
// and cutting again.
func TestACutWhoseSyncFailsStopsTheNode(t *testing.T) {
	for _, pauses := range []bool{false, true} {
		t.Run(map[bool]string{false: "aside", true: "on the apply goroutine"}[pauses], func(t *testing.T) {
			cid := coreconsensus.ClusterID{4, 5, 6}
			log, net := NewMemoryLog(), NewMemoryNetwork()
			id, inc, err := InitLog(log, cid, "slot-0")
			if err != nil {
				t.Fatal(err)
			}
			members := []coreconsensus.MemberValue{{ID: id, Incarnation: inc, Slot: "slot-0", SPKI: MemorySPKI(id), Voter: true}}
			cfg := Config{
				Cluster: cid, Machine: newKV(), Durability: DurabilityVolatile, Log: log,
				Snapshots: syncFailingSnapshots{NewMemorySnapshots()}, Transport: NewMemoryTransport(net, id),
				InitialTiming: testTimingFast(), MaxSnapshotBytes: 1 << 30, SnapshotEveryBytes: 1 << 20,
				MaxLogBytes: 1 << 30, LogReserveBytes: 1 << 20, MinVoters: 1, SnapshotEvery: 2,
				SnapshotTrailing: 1, JoinAuthorizer: allowJoin{}, SnapshotPausesApply: pauses,
			}
			if err := Bootstrap(context.Background(), &cfg, members); err != nil {
				t.Fatal(err)
			}
			n, err := NewNode(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := n.HardClose(); err != nil {
					t.Error(err)
				}
			})
			deadline := time.Now().Add(10 * time.Second)
			for i := 0; ; i++ {
				if r, ok := n.StopReason(); ok {
					if !errs.HasCode(r.Err, coreconsensus.CodeStorageFailed) {
						t.Fatalf("stopped on %v, want STORAGE_FAILED", r.Err)
					}
					return
				}
				if time.Now().After(deadline) {
					t.Fatal("the node kept running after a snapshot's sync failed")
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				_, _, perr := n.Propose(ctx, []byte{'k', '=', byte('a' + i%26)})
				cancel()
				if perr != nil {
					time.Sleep(5 * time.Millisecond)
				}
			}
		})
	}
}

// A cut's metadata carries the engine state at the cut index, not the one
// standing when the cut is made: a timing contract committed after the
// index is absent from a snapshot cut there, and present in one cut at or
// past it (#402).
func TestACutCarriesTheEngineStateAtItsIndex(t *testing.T) {
	c := newCluster(t, 3, 0)
	l := c.leader(5 * time.Second)
	tok := propose(t, l.node, "a=1")
	var old coreconsensus.TimingContractSpec
	if !l.node.exec(func() { old = l.node.r.Status().Timing }) {
		t.Fatal("the node stopped")
	}
	next := testTimingFast()
	next.HeartbeatInterval = 7 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pre := coreconsensus.PreconditionSpec{Log: l.node.ident, Generation: old.Generation}
	if err := l.node.ProposeTimingContract(ctx, "op:test", pre, next); err != nil {
		t.Fatal(err)
	}
	after := propose(t, l.node, "b=2")
	var now coreconsensus.TimingContractSpec
	if !l.node.exec(func() { now = l.node.r.Status().Timing }) {
		t.Fatal("the node stopped")
	}
	if now.Generation != old.Generation+1 {
		t.Fatalf("setup: the contract did not commit: generation %d", now.Generation)
	}
	meta, ok := l.node.metaAt(tok.Index, tok.Term)
	if !ok {
		t.Fatal("the node stopped")
	}
	if meta.Timing != old {
		t.Fatalf("a cut at %d, before the contract, carries timing %+v, want %+v", tok.Index, meta.Timing, old)
	}
	meta, ok = l.node.metaAt(after.Index, after.Term)
	if !ok {
		t.Fatal("the node stopped")
	}
	if meta.Timing.Generation != old.Generation+1 || meta.Timing.HeartbeatInterval != next.HeartbeatInterval {
		t.Fatalf("a cut at %d, past the contract, carries timing %+v", after.Index, meta.Timing)
	}
}

// countingTransport is a node's end of a memory network that counts the
// frames the node sends: votes, acknowledgements, appends, heartbeats.
type countingTransport struct {
	*MemoryTransport
	sent atomic.Int64
}

// Send implements Transport, counting the frame before it leaves.
func (t *countingTransport) Send(to coreconsensus.NodeID, frame []byte) {
	t.sent.Add(1)
	t.MemoryTransport.Send(to, frame)
}

// countedCluster is a cluster of three whose every node sends through a
// countingTransport, and snapshots into a store whose Commit fails as a
// failed fsync does when failing names it.
func countedCluster(t *testing.T, every uint64, pauses bool, failing func(tn *testNode) bool) (*cluster, map[coreconsensus.NodeID]*countingTransport) {
	t.Helper()
	var mu sync.Mutex
	counted := map[coreconsensus.NodeID]*countingTransport{}
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{4, 5, 9}, every: every, size: 3}
	c.configure = func(tn *testNode, cfg *Config) {
		mu.Lock()
		ct := counted[tn.id]
		if ct == nil {
			ct = &countingTransport{MemoryTransport: NewMemoryTransport(c.net, tn.id)}
			counted[tn.id] = ct
		}
		mu.Unlock()
		cfg.Transport = ct
		cfg.SnapshotPausesApply = pauses
		if failing != nil && failing(tn) {
			cfg.Snapshots = syncFailingSnapshots{tn.snaps}
		}
	}
	return newClusterWith(t, c), counted
}

// stoppedForGood waits for tn's node to stop on STORAGE_FAILED, then proves
// the stop final: every goroutine of the node ends, and while the rest of the
// cluster commits past it for several election timeouts, the node sends no
// frame — no vote, no acknowledgement, no append — and its log does not move.
// The node is taken out of the cluster's live set, and closed at cleanup.
func stoppedForGood(t *testing.T, c *cluster, tn *testNode, ct *countingTransport) {
	t.Helper()
	n := tn.node
	waitFor(t, "the node stops on the failed sync", func() bool {
		_, ok := n.StopReason()
		return ok
	})
	r, _ := n.StopReason()
	if !errs.HasCode(r.Err, coreconsensus.CodeStorageFailed) || r.ExitCode() != 74 {
		t.Fatalf("stopped on %v (exit %d), want STORAGE_FAILED (exit 74)", r.Err, r.ExitCode())
	}
	tn.node = nil
	t.Cleanup(func() {
		if err := n.HardClose(); err != nil {
			t.Error(err)
		}
	})
	// The stop alone ends every goroutine: nothing is left to send or write.
	ended := make(chan struct{})
	go func() { n.wg.Wait(); close(ended) }()
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the stopped node's goroutines did not end")
	}
	sent := ct.sent.Load()
	_, last := tn.log.Bounds()
	hs, err := tn.log.HardState()
	if err != nil {
		t.Fatal(err)
	}
	var tok coreconsensus.TokenValue
	for i := range 20 {
		tok = proposeOnLeader(t, c, fmt.Sprintf("after%d=%d", i, i))
	}
	c.waitApplied(tok.Index, 10*time.Second)
	// Past several election timeouts: a node still running would have
	// campaigned by now, or acknowledged the appends above.
	time.Sleep(3 * testTimingFast().ElectionTimeoutMax)
	if got := ct.sent.Load(); got != sent {
		t.Fatalf("the stopped node sent %d frames after its stop", got-sent)
	}
	if _, now := tn.log.Bounds(); now != last {
		t.Fatalf("the stopped node's log moved from %d to %d", last, now)
	}
	if now, err := tn.log.HardState(); err != nil || now != hs {
		t.Fatalf("the stopped node's hard state moved from %+v to %+v (%v)", hs, now, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, err := n.Propose(ctx, []byte("late=1")); !errors.Is(err, coreconsensus.NodeStopped) {
		t.Fatalf("propose after the stop: %v", err)
	}
}

// TestARequestedCutWhoseSyncFailsStopsTheNode pins D4 on the cut a peer asks
// for (#404): a leader with no snapshot to send cuts one through requestCut,
// and that cut's failed fsync stops the leader as a triggered cut's does,
// whether it is written aside or on the apply goroutine. On the apply
// goroutine it used to be dropped, and the leader went on leading over a
// disk that had just failed an fsync.
func TestARequestedCutWhoseSyncFailsStopsTheNode(t *testing.T) {
	type tc struct {
		name   string
		pauses bool
	}
	runCase := func(t *testing.T, c tc) {
		// No cut is ever triggered: the only one is the one requested.
		cl, counted := countedCluster(t, 1<<20, c.pauses, func(*testNode) bool { return true })
		l := cl.leader(5 * time.Second)
		tok := propose(t, l.node, "a=1")
		waitFor(t, "the leader applies its entry", func() bool { return l.node.Status().Applied >= tok.Index })
		l.node.requestCut()
		stoppedForGood(t, cl, l, counted[l.id])
	}
	for _, c := range []tc{{name: "aside"}, {name: "on the apply goroutine", pauses: true}} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestAnInstallWhoseSyncFailsStopsTheNode pins D4 on the receiving side of a
// snapshot (#404): a follower caught up by an install whose spool could not
// be made durable stops with STORAGE_FAILED, instead of answering the leader
// and voting, acknowledging and appending on to the next offer.
func TestAnInstallWhoseSyncFailsStopsTheNode(t *testing.T) {
	var lagID coreconsensus.NodeID
	var mu sync.Mutex
	c, counted := countedCluster(t, 20, false, func(tn *testNode) bool {
		mu.Lock()
		defer mu.Unlock()
		return lagID != 0 && tn.id == lagID
	})
	l := c.leader(5 * time.Second)
	lag := c.otherThan(l)
	// Isolated before it stops, or the memory network delivers at the
	// restart what was sent meanwhile, and appends catch it up (#374).
	c.net.Isolate(lag.id, true)
	c.kill(lag)
	for i := range 200 {
		proposeOnLeader(t, c, fmt.Sprintf("k%d=%d", i, i))
	}
	compactedPast(t, c, 10)
	mu.Lock()
	lagID = lag.id
	mu.Unlock()
	lag.sm = newKV()
	cfg := c.config(lag)
	// Its own cuts never trigger: the stop can only come from the install.
	cfg.SnapshotEvery = 1 << 20
	n, err := NewNode(cfg)
	if err != nil {
		t.Fatal(err)
	}
	lag.node = n
	c.net.Isolate(lag.id, false)
	stoppedForGood(t, c, lag, counted[lag.id])
	if metas := must(lag.snaps.List())(t); len(metas) != 0 {
		t.Fatalf("the failed install published %d snapshots", len(metas))
	}
}
