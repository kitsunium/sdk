package consensus

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// durableKV is a DurableStateMachine over the test kv: the object survives a
// restart of the node, as a machine over its own durable store would.
type durableKV struct {
	*kv
	pmu sync.Mutex
	li  coreconsensus.LogIdentityValue
	pos coreconsensus.PositionValue
}

func (d *durableKV) Apply(e coreconsensus.EntryValue) any {
	r := d.kv.Apply(e)
	d.pmu.Lock()
	d.li = coreconsensus.LogIdentityValue{Cluster: e.Token.Cluster, Epoch: e.Token.Epoch}
	d.pos = e.Position
	d.pmu.Unlock()
	return r
}

func (d *durableKV) Restore(r io.Reader, m coreconsensus.SnapshotMetaValue) error {
	if err := d.kv.Restore(r, m); err != nil {
		return err
	}
	d.pmu.Lock()
	d.li, d.pos = m.Log, m.Position
	d.pmu.Unlock()
	return nil
}

func (d *durableKV) AppliedPosition() (coreconsensus.LogIdentityValue, coreconsensus.PositionValue, error) {
	d.pmu.Lock()
	defer d.pmu.Unlock()
	return d.li, d.pos, nil
}

type durableNode struct {
	dir  string
	dd   DataDir
	id   coreconsensus.NodeID
	sm   *durableKV
	node *Node
}

type durableBench struct {
	t     *testing.T
	cid   coreconsensus.ClusterID
	net   *MemoryNetwork
	nodes []*durableNode
}

func (b *durableBench) conf(x *durableNode) Config {
	return Config{
		Cluster: b.cid, Machine: x.sm, Durability: DurabilityDurable, Log: x.dd.Log(), Snapshots: x.dd.Snapshots(),
		Transport: NewMemoryTransport(b.net, x.id), InitialTiming: testTimingDisk(), MaxSnapshotBytes: 1 << 30,
		SnapshotEveryBytes: 1 << 20, MaxLogBytes: 1 << 30, LogReserveBytes: 1 << 20, MinVoters: 3,
		SnapshotEvery: 100000, SnapshotTrailing: 2,
	}
}

func (b *durableBench) open(x *durableNode) error {
	x.dd = openDir(b.t, x.dir, 16<<10)
	n, err := NewNode(b.conf(x))
	if err != nil {
		_ = x.dd.Close()
		return err
	}
	x.node = n
	return nil
}

func (b *durableBench) crash(x *durableNode) {
	if err := x.node.HardClose(); err != nil {
		b.t.Errorf("hard close: %v", err)
	}
	_ = NewMemoryTransport(b.net, x.id).Close()
	if err := x.dd.Close(); err != nil {
		b.t.Errorf("close dir: %v", err)
	}
	x.node = nil
}

func (b *durableBench) leader() *durableNode {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, x := range b.nodes {
			if x.node != nil && x.node.Status().Role == coreconsensus.RoleLeader {
				return x
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	b.t.Fatal("no leader")
	return nil
}

func (b *durableBench) waitMachine(x *durableNode, idx uint64) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, p, _ := x.sm.AppliedPosition(); p.Index >= idx {
			return
		}
		if time.Now().After(deadline) {
			b.t.Fatalf("node %d never applied %d", x.id, idx)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (b *durableBench) save(x *durableNode) coreconsensus.SnapshotMetaValue {
	meta, _ := b.saveBytes(x)
	return meta
}

// saveBytes is save with the encoded stream SaveSnapshot wrote.
func (b *durableBench) saveBytes(x *durableNode) (coreconsensus.SnapshotMetaValue, []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out bytes.Buffer
	for {
		out.Reset()
		meta, err := x.node.SaveSnapshot(ctx, "operator:backup", &out)
		if err == nil {
			return meta, out.Bytes()
		}
		if ctx.Err() != nil {
			b.t.Fatalf("SaveSnapshot: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func newDurableBench(t *testing.T, cid coreconsensus.ClusterID) *durableBench {
	b := &durableBench{t: t, cid: cid, net: NewMemoryNetwork()}
	var members []coreconsensus.MemberValue
	for i := range 3 {
		x := &durableNode{dir: t.TempDir(), sm: &durableKV{kv: newKV()}}
		x.dd = openDir(t, x.dir, 16<<10)
		slot := fmt.Sprintf("z%d", i)
		id, inc, err := Init(x.dd, cid, slot)
		if err != nil {
			t.Fatal(err)
		}
		x.id = id
		b.nodes = append(b.nodes, x)
		members = append(members, coreconsensus.MemberValue{ID: id, Incarnation: inc, Slot: slot, SPKI: MemorySPKI(id), Voter: true})
	}
	for _, x := range b.nodes {
		if err := Bootstrap(context.Background(), new(b.conf(x)), members); err != nil {
			t.Fatal(err)
		}
		if err := x.dd.Close(); err != nil {
			t.Fatal(err)
		}
		if err := b.open(x); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, x := range b.nodes {
			if x.node != nil {
				b.crash(x)
			}
		}
	})
	return b
}

// After a restart over a DurableStateMachine whose applied index is past
// its snapshot, the node resumes with the term of that position: it used to
// resume at term 0, a SaveSnapshot before the next apply recorded the
// snapshot at term 0, and the node's next start failed STORAGE_FAILED in
// finishInstall's compaction, then every start after (#544).
func TestADurableRestartResumesWithTheAppliedTerm(t *testing.T) {
	b := newDurableBench(t, coreconsensus.ClusterID{9, 5, 1})
	var tok coreconsensus.TokenValue
	for i := range 30 {
		tok = propose(t, b.leader().node, fmt.Sprintf("k%d=v%d", i, i))
	}
	l := b.leader()
	var f *durableNode
	for _, x := range b.nodes {
		if x != l {
			f = x
			break
		}
	}
	b.waitMachine(f, tok.Index)
	b.crash(f)
	if err := b.open(f); err != nil {
		t.Fatalf("first restart: %v", err)
	}
	f.node.am.Lock()
	ra, rt := f.node.applied, f.node.appliedTerm
	f.node.am.Unlock()
	_, mpos, _ := f.sm.AppliedPosition()
	if ra != mpos.Index || rt != mpos.Term || rt == 0 {
		t.Fatalf("after restart: applied %d at term %d, the machine holds %+v", ra, rt, mpos)
	}
	meta := b.save(f)
	if logTerm, err := f.dd.Log().Term(meta.Position.Index); err != nil || meta.Position.Term != logTerm {
		t.Fatalf("snapshot %s at %+v, the log's term there %d (%v)", meta.ID, meta.Position, logTerm, err)
	}
	tok2 := propose(t, b.leader().node, "after=1")
	b.waitMachine(f, tok2.Index)
	b.crash(f)
	if err := b.open(f); err != nil {
		t.Fatalf("second restart: %v %v", err, errs.FieldsOf(err))
	}
}

// A snapshot already published at term 0, by a binary before #544, is set
// aside as unreadable at the start instead of truncating the log and
// stopping the node, and is never sent to a peer.
func TestASnapshotPublishedAtTermZeroIsSetAside(t *testing.T) {
	b := newDurableBench(t, coreconsensus.ClusterID{9, 5, 3})
	var tok coreconsensus.TokenValue
	for i := range 30 {
		tok = propose(t, b.leader().node, fmt.Sprintf("k%d=v%d", i, i))
	}
	l := b.leader()
	var f *durableNode
	for _, x := range b.nodes {
		if x != l {
			f = x
			break
		}
	}
	b.waitMachine(f, tok.Index)
	meta, stream := b.saveBytes(f)
	b.crash(f)
	f.dd = openDir(t, f.dir, 16<<10)
	bad := meta
	bad.Position.Term = 0
	bad.ID = fmt.Sprintf("%020d-%020d", bad.Position.Index, 0)
	sink, _, err := f.dd.Snapshots().Create(bad.ID, bad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Write(stream); err != nil {
		t.Fatal(err)
	}
	if err := sink.Commit(bad); err != nil {
		t.Fatal(err)
	}
	if err := f.dd.Snapshots().Remove(meta.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.dd.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.open(f); err != nil {
		t.Fatalf("a start over a term-0 snapshot: %v %v", err, errs.FieldsOf(err))
	}
	if _, ok := f.node.snapshotToSend(); ok {
		t.Fatal("a term-0 snapshot is offered to peers")
	}
	tok2 := propose(t, b.leader().node, "after=1")
	b.waitMachine(f, tok2.Index)
}

// A leader restarted past its snapshot cuts at the right term, so a peer
// behind its compacted log installs that snapshot and catches up: it used
// to install a term-0 one and stop STORAGE_FAILED (#544).
func TestAPeerInstallsTheSnapshotOfADurableLeaderRestarted(t *testing.T) {
	b := newDurableBench(t, coreconsensus.ClusterID{9, 5, 2})
	var tok coreconsensus.TokenValue
	for i := range 5 {
		tok = propose(t, b.leader().node, fmt.Sprintf("e%d=v%d", i, i))
	}
	l := b.leader()
	var f, g *durableNode
	for _, x := range b.nodes {
		if x == l {
			continue
		}
		if f == nil {
			f = x
		} else {
			g = x
		}
	}
	b.waitMachine(g, tok.Index)
	b.crash(g)
	for i := range 30 {
		tok = propose(t, l.node, fmt.Sprintf("k%d=v%d", i, i))
	}
	b.waitMachine(f, tok.Index)
	b.crash(f)
	if err := b.open(f); err != nil {
		t.Fatalf("restart f: %v", err)
	}
	if meta := b.save(f); meta.Position.Term == 0 {
		t.Fatalf("f's snapshot %s recorded at term 0", meta.ID)
	}
	tctx, tcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer tcancel()
	for {
		err := l.node.transferLocal(tctx, f.id)
		if err == nil {
			break
		}
		if tctx.Err() != nil {
			t.Fatalf("transfer to f: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := b.open(g); err != nil {
		t.Fatalf("restart g: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if r, stopped := g.node.StopReason(); stopped {
			code, _ := errs.CodeOf(r.Err)
			t.Fatalf("g stopped (code %v): %v %v", code, r.Err, errs.FieldsOf(r.Err))
		}
		if _, p, _ := g.sm.AppliedPosition(); p.Index >= tok.Index {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("g neither caught up nor stopped: %+v", g.node.Status())
}
