package consensus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
)

// engineThenCommand appends, on the leader l, an engine entry (engine
// proposes it) then a command, while the followers' acknowledgements are
// held back, so that both commit in one step: on l that step is a Ready
// carrying the commit alone, written lazily. It returns the engine entry's
// index and the command's token once l's machine applied the command.
func engineThenCommand(t *testing.T, b *durableBench, l *durableNode, engine func(ctx context.Context) error) (uint64, coreconsensus.TokenValue) {
	t.Helper()
	_, start := l.dd.Log().Bounds()
	for _, x := range b.nodes {
		if x != l {
			b.net.Cut(x.id, l.id, true)
		}
	}
	engineDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		engineDone <- engine(ctx)
	}()
	waitLogLast(t, l, start+1)
	cmdDone := make(chan coreconsensus.TokenValue, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		tok, _, err := l.node.Propose(ctx, []byte("after=engine"))
		if err != nil {
			t.Errorf("propose after the engine entry: %v", err)
		}
		cmdDone <- tok
	}()
	waitLogLast(t, l, start+2)
	for _, x := range b.nodes {
		if x != l {
			b.net.Cut(x.id, l.id, false)
		}
	}
	if err := <-engineDone; err != nil {
		t.Fatalf("engine entry: %v", err)
	}
	tok := <-cmdDone
	b.waitMachine(l, tok.Index)
	return start + 1, tok
}

// waitLogLast waits until x's log holds index last.
func waitLogLast(t *testing.T, x *durableNode, last uint64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for _, l := x.dd.Log().Bounds(); l < last; _, l = x.dd.Log().Bounds() {
		if time.Now().After(deadline) {
			t.Fatalf("node %d's log never reached %d", x.id, last)
		}
		time.Sleep(time.Millisecond)
	}
}

// crashAndCut crashes l, checks that its persisted commit stayed below the
// engine entry at j while its machine holds the command after it, restarts
// it isolated and has it cut a snapshot at its applied index, as forceCut
// does for a leader with none it may send, a peer whose log starts past the
// one sent, or a held copy found corrupt (requestCut). It returns the
// snapshot published.
func crashAndCut(t *testing.T, b *durableBench, l *durableNode, j uint64) coreconsensus.SnapshotMetaValue {
	t.Helper()
	b.crash(l)
	dd := b.openDD(l.dir)
	rep, err := Inspect(dd)
	if err != nil {
		t.Fatal(err)
	}
	if err := dd.Close(); err != nil {
		t.Fatal(err)
	}
	_, mpos, _ := l.sm.AppliedPosition()
	t.Logf("after the crash: persisted commit %d, log last %d, machine at %d", rep.HardState.Commit, rep.Last, mpos.Index)
	if rep.HardState.Commit >= j {
		t.Skipf("the commit record of %d reached the disk before the crash (persisted %d): this run did not reproduce the window", j, rep.HardState.Commit)
	}
	b.net.Isolate(l.id, true)
	if err := b.open(l); err != nil {
		t.Fatalf("restart: %v", err)
	}
	rs := l.node.Status()
	l.node.am.Lock()
	applied := l.node.applied
	l.node.am.Unlock()
	t.Logf("restarted: raft commit %d, node applied %d, contract generation %d, cluster version %d", rs.Commit, applied, rs.Timing.Generation, rs.Versions.Cluster)
	l.node.requestCut()
	deadline := time.Now().Add(10 * time.Second)
	for {
		metas, err := l.dd.Snapshots().List()
		if err == nil && len(metas) > 0 {
			return metas[0]
		}
		if time.Now().After(deadline) {
			t.Fatal("no snapshot was cut")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// restartFromTheCut lets l rejoin and catch up past tok, then restarts it
// once more, from the snapshot crashAndCut published, and returns its status
// and that of another node.
func restartFromTheCut(t *testing.T, b *durableBench, l *durableNode, tok coreconsensus.TokenValue) (mine, theirs coreconsensus.StatusValue) {
	t.Helper()
	b.net.Isolate(l.id, false)
	propose(t, b.leader().node, "later=1")
	deadline := time.Now().Add(10 * time.Second)
	for l.node.Status().Commit < tok.Index+1 {
		if time.Now().After(deadline) {
			t.Fatalf("the node never caught up: %+v", l.node.Status())
		}
		time.Sleep(5 * time.Millisecond)
	}
	b.crash(l)
	if err := b.open(l); err != nil {
		t.Fatalf("second restart: %v", err)
	}
	ref := b.leader()
	if ref == l {
		for _, x := range b.nodes {
			if x != l {
				ref = x
			}
		}
	}
	return l.node.Status(), ref.node.Status()
}

// #707: a leader commits a timing contract and a command in one step. The
// commit is written lazily and rides the next sync (#558), so a crash leaves
// its log with the persisted commit from before both, while its
// DurableStateMachine holds the command applied. Restarted, the step
// function resumed from the persisted commit and replayed the engine effects
// through it alone, below the applied index the node resumes at; a cut there
// took the engine state from EngineAt, which knew no entry past that commit,
// and recorded the old contract at an index past the new one. Restarted from
// that snapshot, the node ran the old contract for good, the rest of the
// cluster the new one. The start now takes the applied index the log holds
// as committed.
func TestADurableRestartPastItsPersistedCommitCutsTheContractItApplied(t *testing.T) {
	b := newDurableBenchIn(t, coreconsensus.ClusterID{0x70, 0x07, 1}, t.TempDir())
	for i := range 5 {
		propose(t, b.leader().node, fmt.Sprintf("k%d=v%d", i, i))
	}
	l := b.leader()
	gen := l.node.Status().Timing.Generation
	next := testTimingDisk()
	next.ElectionTimeoutMax = 1100 * time.Millisecond
	pre := coreconsensus.PreconditionSpec{Log: l.node.lineage(), Generation: gen}
	j, tok := engineThenCommand(t, b, l, func(ctx context.Context) error {
		return l.node.ProposeTimingContract(ctx, "op:test", pre, next)
	})
	meta := crashAndCut(t, b, l, j)
	t.Logf("snapshot %s: contract generation %d; the contract at %d it covers is generation %d", meta.ID, meta.Timing.Generation, j, gen+1)
	if meta.Position.Index < tok.Index || meta.Timing.Generation != gen+1 {
		t.Errorf("the snapshot cut at %d records contract generation %d, though it covers the generation %d entry at %d",
			meta.Position.Index, meta.Timing.Generation, gen+1, j)
	}
	mine, theirs := restartFromTheCut(t, b, l, tok)
	if mine.Timing.Generation != gen+1 || mine.Timing != theirs.Timing {
		t.Fatalf("restarted from that snapshot, the node runs contract generation %d (%+v), the cluster %d (%+v)",
			mine.Timing.Generation, mine.Timing, theirs.Timing.Generation, theirs.Timing)
	}
}

// The same with the raise of the cluster version: the snapshot recorded the
// version before the raise it covers, and the node restarted from it ran
// that version for good, writing the formats of a version the cluster had
// left, while every other member ran the raised one (#707).
func TestADurableRestartPastItsPersistedCommitCutsTheVersionItApplied(t *testing.T) {
	b := newDurableBenchIn(t, coreconsensus.ClusterID{0x70, 0x07, 2}, t.TempDir())
	for i := range 5 {
		propose(t, b.leader().node, fmt.Sprintf("k%d=v%d", i, i))
	}
	l := b.leader()
	st := l.node.Status()
	to := max(raft.Protocol2, st.Versions.Cluster+1)
	j, tok := engineThenCommand(t, b, l, func(ctx context.Context) error {
		return l.node.RaiseClusterVersion(ctx, "op:raise", coreconsensus.PreconditionSpec{Log: st.Log, Generation: st.Versions.ClusterGeneration}, to)
	})
	meta := crashAndCut(t, b, l, j)
	t.Logf("snapshot %s: cluster version %d generation %d; the raise at %d it covers is to %d", meta.ID, meta.ClusterVersion, meta.ClusterGeneration, j, to)
	if meta.Position.Index < tok.Index || meta.ClusterVersion != to {
		t.Errorf("the snapshot cut at %d records cluster version %d, though it covers the raise to %d at %d",
			meta.Position.Index, meta.ClusterVersion, to, j)
	}
	mine, theirs := restartFromTheCut(t, b, l, tok)
	if mine.Versions.Cluster != to || mine.Versions.Cluster != theirs.Versions.Cluster || mine.Versions.ClusterGeneration != theirs.Versions.ClusterGeneration {
		t.Fatalf("restarted from that snapshot, the node runs cluster version %d (generation %d), the cluster %d (generation %d)",
			mine.Versions.Cluster, mine.Versions.ClusterGeneration, theirs.Versions.Cluster, theirs.Versions.ClusterGeneration)
	}
}

// #707, the start's other side: a DurableStateMachine may stand past the
// end of its log. A follower F applied a timing contract at j and a command
// after it; the synced record of the contract rots, F is quarantined and
// exits by TruncateAndStaySilent, which truncates its log below j. F
// restarts with its machine at j+1 and a log that ends at j-1, so its step
// function's commit stays below j and the start cannot take the applied
// index as committed: the log holds nothing at it. A cut at the applied
// index took the engine state of that commit and recorded the old contract
// past the new one. It is now refused until the commit reaches the index,
// and F, back under its leader, then cuts the contract it applied.
func TestACutPastTheCommitTheNodeKnowsWaitsForIt(t *testing.T) {
	b := newDurableBenchIn(t, coreconsensus.ClusterID{0x70, 0x07, 3}, t.TempDir())
	for i := range 5 {
		propose(t, b.leader().node, fmt.Sprintf("k%d=v%d", i, i))
	}
	l := b.leader()
	var f *durableNode
	for _, x := range b.nodes {
		if x != l {
			f = x
			break
		}
	}
	gen := l.node.Status().Timing.Generation
	next := testTimingDisk()
	next.ElectionTimeoutMax = 1100 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, j := l.dd.Log().Bounds()
	j++
	if err := l.node.ProposeTimingContract(ctx, "op:test", coreconsensus.PreconditionSpec{Log: l.node.lineage(), Generation: gen}, next); err != nil {
		t.Fatal(err)
	}
	if es, err := l.dd.Log().Entries(j, j+1, 1<<20, nil); err != nil || len(es) != 1 || es[0].Kind != coreconsensus.EntryTimingContract {
		t.Fatalf("setup: the entry at %d is not the contract: %v %v", j, es, err)
	}
	tok := propose(t, l.node, "after=contract")
	b.waitMachine(f, tok.Index)
	b.net.Isolate(f.id, true)
	b.crash(f)

	// The synced record of the contract rots; quarantine, then the
	// TruncateAndStaySilent exit.
	dd := b.openDD(f.dir)
	fl := dd.d.log
	fl.mu.RLock()
	s := fl.slots[j-fl.first]
	fl.mu.RUnlock()
	if err := dd.Close(); err != nil {
		t.Fatal(err)
	}
	segPath := filepath.Join(f.dir, "log", segName(s.seg))
	raw := must(os.ReadFile(segPath))(t)
	raw[s.off+int64(recHeader+recFixed)] ^= 0xff
	if err := os.WriteFile(segPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	f.dd = b.openDD(f.dir)
	if _, err := NewNode(b.conf(f)); !errors.Is(err, coreconsensus.LogQuarantined) {
		t.Fatalf("damaged log: %v", err)
	}
	if err := ClearQuarantine(f.dd, TruncateAndStaySilent, "op:silent"); err != nil {
		t.Fatal(err)
	}
	if _, last := f.dd.Log().Bounds(); last >= j {
		t.Fatalf("setup: the truncated log ends at %d, at or past the contract at %d", last, j)
	}
	if err := f.dd.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.open(f); err != nil {
		t.Fatalf("restart: %v %v", err, errs.FieldsOf(err))
	}
	f.node.am.Lock()
	applied := f.node.applied
	f.node.am.Unlock()
	if rs := f.node.Status(); applied != tok.Index || rs.Commit >= j {
		t.Fatalf("setup: restarted with applied %d and commit %d, want %d over a commit below %d", applied, rs.Commit, tok.Index, j)
	}

	f.node.requestCut()
	ss := &f.node.snapState
	for deadline := time.Now().Add(10 * time.Second); ; {
		if metas := must(f.dd.Snapshots().List())(t); len(metas) > 0 && metas[0].Position.Index >= j {
			t.Fatalf("a cut at %d past the commit %d was published: %s records contract generation %d, though it covers the generation %d entry at %d",
				applied, f.node.Status().Commit, metas[0].ID, metas[0].Timing.Generation, gen+1, j)
		}
		ss.mu.Lock()
		refused := ss.backoff != 0
		ss.mu.Unlock()
		if refused {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the cut was neither refused nor published")
		}
		time.Sleep(5 * time.Millisecond)
	}

	b.net.Isolate(f.id, false)
	propose(t, b.leader().node, "later=1")
	for deadline := time.Now().Add(10 * time.Second); f.node.Status().Commit < tok.Index; {
		if time.Now().After(deadline) {
			t.Fatalf("the node never caught up: %+v", f.node.Status())
		}
		time.Sleep(5 * time.Millisecond)
	}
	meta, err := f.node.metaAt(tok.Index, tok.Term)
	if err != nil {
		t.Fatalf("a cut at %d once the commit passed it: %v", tok.Index, err)
	}
	if meta.Timing.Generation != gen+1 {
		t.Fatalf("a cut at %d records contract generation %d, want %d", tok.Index, meta.Timing.Generation, gen+1)
	}
}

// #707, the start's other guard: only the machine's term proves the log
// holds, at the index the machine applied, the entry it applied. A leader
// cut off from its quorum appends two commands no one commits while the
// others elect a leader that commits other entries at those indexes; the
// old leader's machine then stands, with no term, on the second of them —
// as one that restored an install before its log was reset, the start
// falling back below that snapshot. Taking that index as committed took the
// stale entries for committed ones: the new leader's appends could never
// truncate them, and the node never caught up. The start holds such an
// index at the persisted commit, and the node is rewritten and catches up.
func TestATermlessDurableMachinePastTheCommitLeavesTheStaleTailUncommitted(t *testing.T) {
	b := newDurableBenchIn(t, coreconsensus.ClusterID{0x70, 0x07, 4}, t.TempDir())
	for i := range 5 {
		propose(t, b.leader().node, fmt.Sprintf("k%d=v%d", i, i))
	}
	l := b.leader()
	_, start := l.dd.Log().Bounds()
	b.net.Isolate(l.id, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stale := make(chan struct{}, 2)
	for i := range 2 {
		go func() {
			_, _, _ = l.node.Propose(ctx, fmt.Appendf(nil, "stale%d=1", i))
			stale <- struct{}{}
		}()
	}
	waitLogLast(t, l, start+2)
	var nl *durableNode
	for deadline := time.Now().Add(10 * time.Second); nl == nil; {
		for _, x := range b.nodes {
			if x != l && x.node.Status().Role == coreconsensus.RoleLeader {
				nl = x
			}
		}
		if nl == nil && time.Now().After(deadline) {
			t.Fatal("the other nodes elected no leader")
		}
		time.Sleep(5 * time.Millisecond)
	}
	var tok coreconsensus.TokenValue
	for i := range 3 {
		tok = propose(t, nl.node, fmt.Sprintf("new%d=1", i))
	}
	b.crash(l)
	cancel()
	<-stale
	<-stale

	dd := b.openDD(l.dir)
	rep := must(Inspect(dd))(t)
	staleTerm := must(dd.Log().Term(start + 2))(t)
	if err := dd.Close(); err != nil {
		t.Fatal(err)
	}
	if newTerm := must(nl.dd.Log().Term(start + 2))(t); rep.HardState.Commit > start || staleTerm == newTerm {
		t.Fatalf("setup: persisted commit %d past %d, or the entry at %d is the new leader's (term %d, the new leader's %d)",
			rep.HardState.Commit, start, start+2, staleTerm, newTerm)
	}
	l.sm.pmu.Lock()
	l.sm.pos = coreconsensus.PositionValue{Index: start + 2}
	l.sm.pmu.Unlock()
	if err := b.open(l); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if rs := l.node.Status(); rs.Commit > start {
		t.Errorf("a machine with no term at %d over a persisted commit of %d restarted at commit %d: the stale entries after %d are taken as committed",
			start+2, rep.HardState.Commit, rs.Commit, start)
	}
	b.net.Isolate(l.id, false)
	for deadline := time.Now().Add(10 * time.Second); ; {
		got, gerr := l.dd.Log().Term(start + 2)
		want, werr := nl.dd.Log().Term(start + 2)
		if l.node.Status().Commit >= tok.Index && gerr == nil && werr == nil && got == want {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the node never caught up: %+v, its entry at %d at term %d, the leader's at %d", l.node.Status(), start+2, got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
