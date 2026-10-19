package consensus

import (
	"context"
	"fmt"
	"sync"
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

// TestARequestedCutWhoseSyncFailsStopsTheNode pins that a cut requested by
// a peer that needs a snapshot (forceCut) obeys D4 like a triggered one:
// written on the apply goroutine, its failed sync stopped nothing, and the
// node carried on over a disk that had just failed an fsync.
func TestARequestedCutWhoseSyncFailsStopsTheNode(t *testing.T) {
	cid := coreconsensus.ClusterID{4, 5, 8}
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
		MaxLogBytes: 1 << 30, LogReserveBytes: 1 << 20, MinVoters: 1, SnapshotEvery: 1 << 20,
		SnapshotTrailing: 1, JoinAuthorizer: allowJoin{}, SnapshotPausesApply: true,
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
	tok := propose(t, n, "a=1")
	waitFor(t, "the entry is applied", func() bool { return n.Status().Applied >= tok.Index })
	n.requestCut()
	waitFor(t, "the node stops on the failed sync of a requested cut", func() bool {
		_, ok := n.StopReason()
		return ok
	})
	if r, _ := n.StopReason(); !errs.HasCode(r.Err, coreconsensus.CodeStorageFailed) {
		t.Fatalf("stopped on %v, want STORAGE_FAILED", r.Err)
	}
}

// TestAnInstallWhoseSyncFailsStopsTheNode pins D4 on the receiving side: an
// installed snapshot whose spool could not be made durable is a failed
// fsync of this node's disk, and the node stops instead of answering the
// leader and waiting for the next offer.
func TestAnInstallWhoseSyncFailsStopsTheNode(t *testing.T) {
	c := newCluster(t, 3, 20)
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
	lag.sm = newKV()
	cfg := c.config(lag)
	cfg.Snapshots = syncFailingSnapshots{lag.snaps}
	// Its own cuts never trigger: the stop must come from the install.
	cfg.SnapshotEvery = 1 << 20
	n, err := NewNode(cfg)
	if err != nil {
		t.Fatal(err)
	}
	lag.node = n
	c.net.Isolate(lag.id, false)
	waitFor(t, "the node stops on the failed sync of an install", func() bool {
		_, ok := n.StopReason()
		return ok
	})
	if r, _ := n.StopReason(); !errs.HasCode(r.Err, coreconsensus.CodeStorageFailed) {
		t.Fatalf("stopped on %v, want STORAGE_FAILED", r.Err)
	}
}
