package consensus

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
)

// #720: a node with no table and a DurableStateMachine starts the step
// function at the machine's applied index, which #707 takes as committed
// past the persisted commit. A node with no table bounded what Ready may
// evict by that index, so the first Ready dropped the payloads of the
// engine entries through it; the install of the leader's older snapshot
// then took the snapshot's engine state and replayed the entries past it
// from payloads that were gone. The node ran the cluster version of the
// snapshot while the cluster ran the raise's, its next cut recorded it,
// and a restart from that cut kept it.

// openDurableWith opens x's data directory and starts its node with the
// bench's configuration as mod changes it.
func openDurableWith(b *durableBench, x *durableNode, mod func(*Config)) error {
	x.dd = b.openDD(x.dir)
	cfg := b.conf(x)
	mod(&cfg)
	n, err := NewNode(cfg)
	if err != nil {
		_ = x.dd.Close()
		return err
	}
	x.node = n
	return nil
}

// corruptPublishedSnapshots flips one byte in the middle of every published
// snapshot file of the data directory dir, a directory of t.TempDir(), and
// returns how many it changed: each then fails its checksums at the next
// start (restoreFrom). A symbolic link is never followed.
func corruptPublishedSnapshots(t *testing.T, dir string) int {
	t.Helper()
	snapDir := filepath.Join(dir, "snap")
	ents, err := os.ReadDir(snapDir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".snap") || !e.Type().IsRegular() {
			continue
		}
		p := filepath.Join(snapDir, e.Name())
		raw := must(os.ReadFile(p))(t)
		if len(raw) == 0 {
			continue
		}
		raw[len(raw)/2] ^= 0xff
		if err := os.WriteFile(p, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		n++
	}
	return n
}

// waitsForInstall reports whether x's step function still has no table.
func waitsForInstall(x *durableNode) bool {
	noTable := false
	x.node.exec(func() { noTable = x.node.r.NoTable() })
	return noTable
}

// TestANoTableDurableFollowerKeepsTheRaiseThroughAnOlderInstall: follower
// F, with a DurableStateMachine, applies a raise of the cluster version at
// j and a command after it whose commit record is written lazily
// (engineThenCommand), and crashes. It restarts with no table, every
// snapshot of its own failing its checksums, and with RecentEntriesBytes
// of 1, so that each Ready evicts what it may. The leader's newest
// snapshot is older than j: F installs it, the restore skipped since its
// machine is past it, and must still run the raise's version.
func TestANoTableDurableFollowerKeepsTheRaiseThroughAnOlderInstall(t *testing.T) {
	b := newDurableBenchIn(t, coreconsensus.ClusterID{0x5d, 0x1a, 0x02}, t.TempDir())
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
	// F cuts first, so that its log starts past 1 and a start without a
	// usable snapshot has no table; the leader cuts later, at another
	// index: its newest is the one it offers F.
	sF := b.save(f)
	var tok coreconsensus.TokenValue
	for i := range 3 {
		tok = propose(t, l.node, fmt.Sprintf("m%d=v%d", i, i))
	}
	b.waitMachine(f, tok.Index)
	sL := b.save(l)
	ls := l.node.Status()
	to := max(raft.Protocol2, ls.Versions.Cluster+1)
	j, cmd := engineThenCommand(t, b, l, func(ctx context.Context) error {
		return l.node.RaiseClusterVersion(ctx, "op:raise", coreconsensus.PreconditionSpec{Log: ls.Log, Generation: ls.Versions.ClusterGeneration}, to)
	})
	b.waitMachine(f, cmd.Index)
	b.crash(f)

	dd := b.openDD(f.dir)
	rep := must(Inspect(dd))(t)
	first, _ := dd.Log().Bounds()
	persisted := rep.HardState.Commit
	if persisted >= j {
		// The commit record of j reached F's disk before the crash, as it
		// does when another write synced it: the hard state is written
		// again with the commit before j, the state a crash leaves when
		// that record had not (#558), so that every run opens the window.
		hs := rep.HardState
		hs.Commit = j - 1
		if err := dd.Log().Append(nil, &hs); err != nil {
			t.Fatal(err)
		}
		if err := dd.Log().Sync(); err != nil {
			t.Fatal(err)
		}
	}
	if err := dd.Close(); err != nil {
		t.Fatal(err)
	}
	_, mpos, _ := f.sm.AppliedPosition()
	t.Logf("F cut %s, the leader %s; after the crash F's persisted commit is %d (the crash left %d), its log [%d, %d], its machine at %+v; the raise to %d at %d",
		sF.ID, sL.ID, min(persisted, j-1), persisted, first, rep.Last, mpos, to, j)
	if first <= 1 || sL.Position.Index >= j || sL.Position.Index+1 < first {
		t.Fatalf("setup: F's log starts at %d, the leader's snapshot is at %d, the raise at %d", first, sL.Position.Index, j)
	}
	if corruptPublishedSnapshots(t, f.dir) == 0 {
		t.Fatal("setup: F publishes no snapshot")
	}
	if err := openDurableWith(b, f, func(c *Config) { c.RecentEntriesBytes = 1 }); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if !waitsForInstall(f) {
		t.Fatal("setup: F restarted with a table")
	}
	for deadline := time.Now().Add(15 * time.Second); waitsForInstall(f); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("F never installed a snapshot: %+v", f.node.Status())
		}
	}
	// The status is published after the turn that installed: give it one
	// second to say what the step function holds.
	theirs := b.leader().node.Status().Versions
	mine := f.node.Status().Versions
	for deadline := time.Now().Add(time.Second); mine.Cluster != theirs.Cluster && time.Now().Before(deadline); mine = f.node.Status().Versions {
		time.Sleep(10 * time.Millisecond)
	}
	if mine.Cluster != theirs.Cluster || mine.ClusterGeneration != theirs.ClusterGeneration {
		t.Fatalf("after installing the leader's snapshot at %d, below the raise at %d, F runs cluster version %d generation %d, the cluster %d generation %d",
			sL.Position.Index, j, mine.Cluster, mine.ClusterGeneration, theirs.Cluster, theirs.ClusterGeneration)
	}
	// What F cuts now records the raise.
	if meta := b.save(f); meta.ClusterVersion != theirs.Cluster || meta.ClusterGeneration != theirs.ClusterGeneration {
		t.Fatalf("F's next cut %s records cluster version %d generation %d, the cluster runs %d generation %d",
			meta.ID, meta.ClusterVersion, meta.ClusterGeneration, theirs.Cluster, theirs.ClusterGeneration)
	}
}
