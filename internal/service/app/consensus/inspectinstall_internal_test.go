package consensus

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// #746, what #534 and #506 (PRs #568, #555) left out: an install a crash
// interrupted after its publication, whose snapshot lies INSIDE the log, at
// an index where the log holds another term. The start (finishInstall) and
// Recover (interruptedInstall) drop the whole log and keep the snapshot's
// position; Inspect reported the log's last index and the log's term there,
// so the runbook's rule, the highest (LastTerm, Last) Inspect reports, chose
// another survivor whose stale tail was as long or longer, and Recover there
// lost the acknowledged writes this one held. Every directory, the runtime
// one included, is in t.TempDir().

// inspectEnv is a bench of data directories on 16 KiB segments, opened
// with a runtime directory of the test's own.
type inspectEnv struct {
	t   *testing.T
	run string
}

func (e *inspectEnv) open(dir string) DataDir {
	e.t.Helper()
	return silenceOpen(e.t, dir, e.run)
}

// start opens dn's directory when it is closed and starts a node over it,
// with minVoters when it is not 0.
func (e *inspectEnv) start(c *cluster, dn *diskNode, minVoters int) {
	e.t.Helper()
	if dn.dd.d == nil {
		dn.dd = e.open(dn.dir)
	}
	dn.sm = newKV()
	cfg := diskConfig(c, dn)
	if minVoters > 0 {
		cfg.MinVoters = minVoters
	}
	n, err := NewNode(cfg)
	if err != nil {
		e.t.Fatalf("NewNode %d: %v %v", dn.id, err, errs.FieldsOf(err))
	}
	dn.node = n
}

// three founds three voters, starts them and commits three writes.
func (e *inspectEnv) three(c *cluster) []*diskNode {
	t := e.t
	var nodes []*diskNode
	var members []coreconsensus.MemberValue
	for i := range 3 {
		dn := &diskNode{dir: t.TempDir()}
		dn.dd = e.open(dn.dir)
		slot := fmt.Sprintf("s%d", i)
		id, inc, err := Init(dn.dd, c.cid, slot)
		if err != nil {
			t.Fatal(err)
		}
		dn.id = id
		nodes = append(nodes, dn)
		members = append(members, coreconsensus.MemberValue{ID: id, Incarnation: inc, Slot: slot, SPKI: MemorySPKI(id), Voter: true})
	}
	// Registered after the TempDirs, so it runs before they are removed,
	// which Windows refuses while a LOCK in them is held.
	t.Cleanup(func() {
		for _, dn := range nodes {
			if dn.node != nil {
				dn.crash(t, c)
			}
		}
	})
	for _, dn := range nodes {
		dn.sm = newKV()
		if err := Bootstrap(context.Background(), new(diskConfig(c, dn)), members); err != nil {
			t.Fatal(err)
		}
		if err := dn.dd.Close(); err != nil {
			t.Fatal(err)
		}
		dn.dd = DataDir{}
		e.start(c, dn, 0)
	}
	var tok coreconsensus.TokenValue
	for i := range 3 {
		tok = proposeOnDisk(t, nodes, fmt.Sprintf("k%d=v%d", i, i))
	}
	c.waitAppliedDisk(nodes, tok.Index)
	return nodes
}

// survivorBefore orders two Inspect reports by the runbook's rule: the
// highest (LastTerm, Last), compared in that order.
func survivorBefore(a, b *ReportValue) bool {
	return a.LastTerm > b.LastTerm || (a.LastTerm == b.LastTerm && a.Last > b.Last)
}

// recoverAndCount runs Recover on dn alone into next, starts it as the
// sole voter of the new history on a network of its own, and counts the
// keys of acked its state holds. It returns the index Recover kept.
func (e *inspectEnv) recoverAndCount(c *cluster, fenced []*diskNode, dn *diskNode, next coreconsensus.ClusterID, acked map[string]uint64) (held int, kept uint64) {
	t := e.t
	t.Helper()
	members, opt := recoverArgs(t, c, fenced, dn, next)
	if err := Recover(dn.dd, next, members, opt); err != nil {
		t.Fatalf("Recover %s: %v %v", dn.dir, err, errs.FieldsOf(err))
	}
	kept = must(dn.dd.Log().Stamp())(t).Log.KeptIndex
	rc := &cluster{t: t, net: NewMemoryNetwork(), cid: next}
	e.start(rc, dn, 1)
	propose(t, dn.node, "after=recover")
	for k := range acked {
		if _, ok := dn.sm.get(k); ok {
			held++
		}
	}
	dn.crash(t, rc)
	return held, kept
}

// TestInspectRanksAnInterruptedInstallInsideTheLogByItsSnapshot: A, the
// leader of term 2, is cut off and appends a long tail no majority holds;
// M, partitioned with it, holds that tail and one entry more. The two
// others elect a leader of term 3, commit and acknowledge 160 writes and
// compact past A's commit. A comes back, is caught up by an install, and
// crashes once the snapshot is published, before its log is moved onto it:
// the snapshot lies inside A's log, under term 3 where the log holds term
// 2. The two others are lost. Inspect must report A as what the start and
// Recover keep, the snapshot's term and index, and the survivor the rule
// picks must hold, once recovered, every acknowledged write the snapshot
// holds.
func TestInspectRanksAnInterruptedInstallInsideTheLogByItsSnapshot(t *testing.T) {
	e := &inspectEnv{t: t, run: t.TempDir()}
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 0x5d, 0x17}}
	nodes := e.three(c)
	a := leaderOf(t, nodes)
	var others []*diskNode
	for _, dn := range nodes {
		if dn != a {
			others = append(others, dn)
		}
	}

	// 1. A, the leader of term T, is cut off and appends a long tail of
	// its own term that no majority ever holds.
	c.net.Isolate(a.id, true)
	var wg sync.WaitGroup
	for i := range 400 {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, _, _ = a.node.Propose(ctx, fmt.Appendf(nil, "stale%d=x", i))
		})
	}
	wg.Wait()
	a.crash(t, c)

	// M stands for a survivor partitioned with A that durably holds A's
	// tail plus one more entry of it, which A's power cut before its own
	// fdatasync: a copy of A's directory, with that entry appended.
	m := &diskNode{dir: t.TempDir(), id: a.id}
	copyDir(t, a.dir, m.dir)
	m.dd = e.open(m.dir)
	t.Cleanup(func() {
		if err := m.dd.Close(); err != nil {
			t.Errorf("close M's data directory: %v", err)
		}
	})
	_, mLast := m.dd.Log().Bounds()
	tailTerm := must(m.dd.Log().Term(mLast))(t)
	prev := must(m.dd.Log().Entries(mLast, mLast+1, 1<<20, nil))(t)[0]
	data := []byte("stale-extra=x")
	extra := coreconsensus.EntryValue{
		Position: coreconsensus.PositionValue{Term: tailTerm, Index: mLast + 1},
		Kind:     coreconsensus.EntryCommand, BodySum: wire.BodySum(data), Data: data,
	}
	extra.ChainSum = wire.ChainSum(prev.ChainSum, &extra)
	if err := m.dd.Log().Append([]coreconsensus.EntryValue{extra}, nil); err != nil {
		t.Fatal(err)
	}
	mustSync(t, m.dd.Log())
	aHS := must(m.dd.Log().HardState())(t)
	if mLast < aHS.Commit+300 {
		t.Fatalf("setup: the isolated leader appended only to %d past commit %d", mLast, aHS.Commit)
	}

	// 2. The two others elect a leader of term T+1 and commit writes that
	// are acknowledged; the leader cuts and compacts past A's commit.
	acked := map[string]uint64{}
	for i := range 160 {
		k := fmt.Sprintf("w%d", i)
		acked[k] = proposeOnDisk(t, others, k+"=y").Index
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if first, _ := leaderOf(t, others).dd.Log().Bounds(); first > aHS.Commit+1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("setup: the new leader never compacted past A's commit")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 3. A comes back; the leader can only catch it up by an install, and
	// A crashes right after the snapshot is published (D7's commit point),
	// before its log is moved onto it, as a power cut during Restore does.
	a.dd = DataDir{}
	e.start(c, a, 0)
	a.node.crashAfterPublish.Store(true)
	c.net.Isolate(a.id, false)
	for deadline = time.Now().Add(15 * time.Second); !a.node.stopped.Load(); {
		if time.Now().After(deadline) {
			t.Fatalf("setup: no install reached publication on A: %+v", a.node.Status())
		}
		time.Sleep(5 * time.Millisecond)
	}
	// 4. The disaster: every node stops; the two others are lost for good.
	for _, dn := range nodes {
		if dn.node != nil {
			dn.crash(t, c)
		}
	}

	// What the start keeps of A's directory: a copy, started under the old
	// cluster, moves its log onto the snapshot.
	s := &diskNode{dir: t.TempDir(), id: a.id}
	copyDir(t, a.dir, s.dir)
	a.dd = e.open(a.dir)
	t.Cleanup(func() {
		if a.node == nil {
			if err := a.dd.Close(); err != nil {
				t.Errorf("close A's data directory: %v", err)
			}
		}
	})
	metas := must(a.dd.Snapshots().List())(t)
	first, last := a.dd.Log().Bounds()
	if len(metas) == 0 {
		t.Fatal("setup: A published no snapshot")
	}
	snap := metas[0].Position
	logTermAtSnap, _ := a.dd.Log().Term(snap.Index)
	t.Logf("A on disk: log [%d, %d], last term %d; newest snapshot at (index %d, term %d), where the log holds term %d",
		first, last, must(a.dd.Log().Term(last))(t), snap.Index, snap.Term, logTermAtSnap)
	if snap.Index > last || snap.Index < first || logTermAtSnap == snap.Term {
		t.Fatal("setup: the snapshot is not inside the log under another term")
	}
	sc := &cluster{t: t, net: NewMemoryNetwork(), cid: c.cid}
	e.start(sc, s, 0)
	_, startedLast := s.dd.Log().Bounds()
	s.crash(t, sc)
	var ackedInSnap int
	for _, idx := range acked {
		if idx <= snap.Index {
			ackedInSnap++
		}
	}

	ra := must(Inspect(a.dd))(t)
	rm := must(Inspect(m.dd))(t)
	t.Logf("Inspect A: LastTerm %d Last %d; started, A's log ends at %d", ra.LastTerm, ra.Last, startedLast)
	t.Logf("Inspect M: LastTerm %d Last %d", rm.LastTerm, rm.Last)
	if ra.LastTerm != snap.Term || ra.Last != snap.Index || ra.Last != startedLast {
		t.Errorf("Inspect reports A as (LastTerm %d, Last %d), while the start keeps its log through %d and the snapshot at (term %d, index %d)",
			ra.LastTerm, ra.Last, startedLast, snap.Term, snap.Index)
	}
	pick, name := a, "A"
	if survivorBefore(&rm, &ra) {
		pick, name = m, "M"
	}
	held, kept := e.recoverAndCount(c, []*diskNode{pick, others[0], others[1]}, pick, coreconsensus.ClusterID{6, 1}, acked)
	t.Logf("the rule picks %s; Recover there kept %d and holds %d of the %d acknowledged writes at or below A's snapshot",
		name, kept, held, ackedInSnap)
	if held < ackedInSnap {
		t.Fatalf("the survivor the rule picks from Inspect, recovered, holds %d acknowledged writes, %d lie in A's snapshot", held, ackedInSnap)
	}
	if pick == a && kept != ra.Last {
		t.Fatalf("Recover kept %d, Inspect reported Last %d", kept, ra.Last)
	}
}

// TestATermZeroSnapshotInsideTheLogIsSetAsideByInspectAndRecover: a
// snapshot a cut before #544 published at term 0 lies inside the log,
// where the log holds another term. The start sets it aside as unreadable
// and keeps the log (restoreLatest). Taken by Inspect and Recover for an
// interrupted install, it ranked the survivor at term 0 by its index, and
// Recover dropped the log to move it onto a position with no term. Both
// now set it aside as the start does: Inspect reports the log's tail, and
// Recover keeps the log and every acknowledged write in it.
func TestATermZeroSnapshotInsideTheLogIsSetAsideByInspectAndRecover(t *testing.T) {
	c, nodes := threeOnDisk(t, 20)
	for _, dn := range nodes {
		dn.crash(t, c)
	}
	keep := nodes[0]
	keep.dd = openDir(t, keep.dir, 16<<10)
	first, last := keep.dd.Log().Bounds()
	lastTerm := must(keep.dd.Log().Term(last))(t)
	if first != 1 || last < 12 {
		t.Fatalf("setup: the log is [%d, %d], want it to start at 1 and hold more than 12 entries", first, last)
	}
	publishAt(t, keep.dd, coreconsensus.PositionValue{Term: 0, Index: 10}, false)
	r := must(Inspect(keep.dd))(t)
	if r.Last != last || r.LastTerm != lastTerm {
		t.Errorf("Inspect reports (LastTerm %d, Last %d) over a term-0 snapshot at 10, want the log's tail (%d, %d)", r.LastTerm, r.Last, lastTerm, last)
	}
	next := coreconsensus.ClusterID{7, 5, 7}
	members, opt := recoverArgs(t, c, nodes, keep, next)
	if err := Recover(keep.dd, next, members, opt); err != nil {
		t.Fatalf("Recover over a term-0 snapshot inside the log: %v %v", err, errs.FieldsOf(err))
	}
	if kept := must(keep.dd.Log().Stamp())(t).Log.KeptIndex; kept != last {
		t.Fatalf("Recover kept %d, want the log's last index %d", kept, last)
	}
	c.cid = next
	keep.sm = newKV()
	cfg := diskConfig(c, keep)
	cfg.MinVoters = 1
	keep.node = must(NewNode(cfg))(t)
	defer keep.crash(t, c)
	propose(t, keep.node, "after=recover")
	for i := range 20 {
		if v, _ := keep.sm.get(fmt.Sprintf("k%d", i)); v != fmt.Sprintf("v%d", i) {
			t.Fatalf("the recovered state lost k%d: %q", i, v)
		}
	}
}
