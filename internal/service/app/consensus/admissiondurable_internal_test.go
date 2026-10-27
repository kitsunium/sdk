package consensus

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// admissionLog is a joiner's file log that records, when the node writes a
// member stamp, how far the log is durable then: one past its last synced
// index, 0 while no member stamp was written. It writes nothing of its own
// and reorders nothing the node asks.
type admissionLog struct {
	*fileLog
	durableAtMember atomic.Uint64
}

// WriteStamp implements LogStore.
func (l *admissionLog) WriteStamp(s coreconsensus.StampValue) error {
	if s.State == coreconsensus.StampMember {
		l.mu.RLock()
		d := l.dur.prevIndex
		l.mu.RUnlock()
		l.durableAtMember.CompareAndSwap(0, d+1)
	}
	return l.fileLog.WriteStamp(s)
}

// admissionConfig is a joiner's configuration, as cluster.config builds a
// member's, over its own stores.
func admissionConfig(c *cluster, id coreconsensus.NodeID, store coreconsensus.LogStore, snaps coreconsensus.SnapshotStore) Config {
	return Config{
		Cluster: c.cid, Machine: newKV(), Durability: DurabilityVolatile, Log: store, Snapshots: snaps,
		Transport: NewMemoryTransport(c.net, id), InitialTiming: testTimingFast(), MaxSnapshotBytes: 1 << 30,
		SnapshotEveryBytes: 1 << 20, MaxLogBytes: 1 << 30, LogReserveBytes: 1 << 20, MinVoters: 1,
		SnapshotTrailing: 4,
	}
}

// joinUntilAnswered sends id's JOIN until a member answers it.
func joinUntilAnswered(t *testing.T, net *MemoryNetwork, id coreconsensus.NodeID, st coreconsensus.StampValue) coreconsensus.PositionValue {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		pos, err := NewMemoryTransport(net, id).Join(ctx, coreconsensus.JoinConfig{Stamp: st, Address: "mem"})
		if err == nil {
			return pos
		}
		if ctx.Err() != nil {
			t.Fatalf("join: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// #695: a joiner J on disk, admitted by three voters under the admission
// model, the TCP transport's. J's first catch-up brings the entry that
// admits it and the leader's commit past it, in one append. checkAdmission
// read raft's commit, in memory, and wrote the member stamp on the loop in
// the turn that handed those entries to the storage actor, before it wrote
// them. The directory is copied at the first fdatasync of the batch that
// carries entry 1, its end record not yet written, and J is cut off and
// stopped before it acknowledges anything: a power cut at that instant.
// Restarted from the copy, J was a member with an empty configuration: its
// transport admitted no peer, nothing sent its JOIN again, and the leader
// never fed it. The stamp now turns a member's once the log is durable
// through the admission: the copy is still joining, J restarted from it
// joins again (#533), is fed, and writes its member stamp only then.
func TestAJoinerBecomesAMemberOnDiskOnlyOnceTheEntriesThatAdmitItAreDurable(t *testing.T) {
	net := NewMemoryNetwork()
	net.admission = true
	run := t.TempDir()
	dir := filepath.Join(t.TempDir(), "joiner")
	crashDir := filepath.Join(t.TempDir(), "crash")
	c := newClusterWith(t, &cluster{t: t, net: net, cid: coreconsensus.ClusterID{0x69, 0x5}})
	l := c.settledLeader(5 * time.Second)

	dd := silenceOpen(t, dir, run)
	jid, _, err := Init(dd, c.cid, "slot-j")
	if err != nil {
		t.Fatal(err)
	}
	st := must(dd.Log().Stamp())(t)
	live := &admissionLog{fileLog: dd.d.log}
	// The crash point: the storage actor waits there, so that J
	// acknowledges nothing the copy lacks, until J is cut off.
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var copied atomic.Bool
	var stampAtCrash atomic.Int32
	copyDone := make(chan error, 1)
	dd.d.log.duringSync = func() {
		if copied.Load() {
			return
		}
		if lo, _ := pendingRange(dd.d.log); lo != 1 {
			return
		}
		copied.Store(true)
		s, _ := readStamp(dir)
		stampAtCrash.Store(int32(s.State))
		copyDone <- copyTree(dir, crashDir)
		<-release
	}
	var n, restarted *Node
	var dd2 DataDir
	t.Cleanup(func() {
		unblock()
		for _, x := range []*Node{n, restarted} {
			if x != nil {
				_ = x.HardClose()
			}
		}
		_ = NewMemoryTransport(net, jid).Close()
		_ = dd.Close()
		_ = dd2.Close()
	})
	n = must(NewNode(admissionConfig(c, jid, live, dd.Snapshots())))(t)
	admitted := joinUntilAnswered(t, net, jid, st)
	// A write after the admission: the leader probes a new learner with
	// the entries past its last index at admission.
	go func() { _, _, _ = l.node.Propose(context.Background(), []byte("kick=1")) }()
	select {
	case err := <-copyDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("no sync of the joiner's first entries: %+v", n.Status())
	}
	joining := coreconsensus.StampState(stampAtCrash.Load()) == coreconsensus.StampJoining
	if !joining {
		t.Errorf("at the first fdatasync of the batch of entry 1, J's admission at %d among them, the stamp on disk is state %d, a member's, written with the log durable through %d only",
			admitted.Index, stampAtCrash.Load(), live.durableAtMember.Load()-1)
	}

	// The power cut: J is cut off before its storage actor goes on, so the
	// leader never hears of the batch the copy lacks; it restarts from the
	// copy, in a new process whose transport admits nobody yet.
	net.Isolate(jid, true)
	unblock()
	if err := n.HardClose(); err != nil {
		t.Fatal(err)
	}
	n = nil
	tr := NewMemoryTransport(net, jid)
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	tr.mu.Lock()
	tr.groups, tr.members = nil, nil
	tr.mu.Unlock()
	tr.hold.mu.Lock()
	tr.hold.cfg, tr.hold.released = nil, false
	tr.hold.mu.Unlock()
	net.Isolate(jid, false)

	dd2 = silenceOpen(t, crashDir, run)
	crashFirst, crashLast := dd2.Log().Bounds()
	crashed := &admissionLog{fileLog: dd2.d.log}
	restarted = must(NewNode(admissionConfig(c, jid, crashed, dd2.Snapshots())))(t)
	// What the driver of a joining directory does at start, and of a
	// member's never: its JOIN, answered by a member already listing it,
	// admits the members (#533).
	if joining {
		joinUntilAnswered(t, net, jid, must(dd2.Log().Stamp())(t))
	}
	tok := proposeOnLeader(t, c, "b=2")
	for deadline := time.Now().Add(5 * time.Second); restarted.Status().Applied < tok.Index; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the joiner restarted from the crash state (stamp state %d, log [%d,%d]) was never fed: %+v, its configuration lists %d member(s)",
				stampAtCrash.Load(), crashFirst, crashLast, restarted.Status(), len(restarted.Membership().Members))
		}
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if s, err := readStamp(crashDir); err == nil && s.State == coreconsensus.StampMember {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the restarted joiner, fed, never wrote its member stamp")
		}
	}
	d := crashed.durableAtMember.Load()
	if d <= admitted.Index {
		t.Fatalf("the member stamp was written with the log durable through %d, below J's admission at %d", d-1, admitted.Index)
	}
	t.Logf("crash state: joining, log [%d,%d]; restarted, J applied %d and wrote its member stamp with the log durable through %d (admission at %d)",
		crashFirst, crashLast, restarted.Status().Applied, d-1, admitted.Index)
}
