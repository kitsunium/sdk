package consensus

import (
	"context"
	"fmt"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestWaitAppliedComparesTheTermRecoverKept is #509: Recover adopts the
// verified suffix past the commit the survivor persisted, and an entry it
// keeps there may be of another term than the write the old cluster
// committed and acknowledged at that index while the survivor was cut off.
// That write is lost: WaitApplied with its token must not report it
// applied, as it did by comparing the lineage the index alone named. The
// kept entry's own token, and the one of an entry at or below the persisted
// commit, are still applied.
func TestWaitAppliedComparesTheTermRecoverKept(t *testing.T) {
	c, nodes := threeOnDisk(t, 30)
	for _, dn := range nodes {
		dn.crash(t, c)
	}
	keep := nodes[0]
	keep.dd = openDir(t, keep.dir, 16<<10)
	old := must(keep.dd.Log().Stamp())(t).Log
	last, commit := appendPastCommit(t, keep.dd, []string{"stale0=a", "stale1=b"}, -1)
	keptTerm := must(keep.dd.Log().Term(last))(t)
	committedTerm := must(keep.dd.Log().Term(commit))(t)
	recoverAlone(t, c, nodes, keep, coreconsensus.ClusterID{9})
	defer keep.crash(t, c)

	wait := func(tok coreconsensus.TokenValue) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return keep.node.WaitApplied(ctx, tok)
	}
	lost := coreconsensus.TokenValue{Cluster: old.Cluster, Epoch: old.Epoch, Term: keptTerm + 1, Index: last}
	if err := wait(lost); !errs.HasCode(err, coreconsensus.CodeLineageMismatch) {
		t.Fatalf("persisted commit %d, adopted up to %d at term %d: WaitApplied(%+v) = %v, want LINEAGE_MISMATCH; the node applied %q there",
			commit, last, keptTerm, lost, err, "stale1=b")
	}
	for _, tok := range []coreconsensus.TokenValue{
		{Cluster: old.Cluster, Epoch: old.Epoch, Term: keptTerm, Index: last},
		{Cluster: old.Cluster, Epoch: old.Epoch, Term: committedTerm, Index: commit},
	} {
		if err := wait(tok); err != nil {
			t.Fatalf("WaitApplied(%+v), an entry Recover kept: %v", tok, err)
		}
	}

	// Once the log is compacted past the kept range, the term there can
	// no longer be read: a token of the predecessor is not vouched for,
	// one of the current history still is, by Log Matching.
	mine := propose(t, keep.node, "mine=1")
	for i := range 120 {
		propose(t, keep.node, fmt.Sprintf("fill%d=x", i))
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if first, _ := keep.dd.Log().Bounds(); first > mine.Index+1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("setup: the log was never compacted past the kept range")
		}
		time.Sleep(10 * time.Millisecond)
	}
	kept := coreconsensus.TokenValue{Cluster: old.Cluster, Epoch: old.Epoch, Term: keptTerm, Index: last}
	if err := wait(kept); !errs.HasCode(err, coreconsensus.CodeLineageMismatch) {
		t.Fatalf("WaitApplied(%+v) once its term can no longer be read: %v, want LINEAGE_MISMATCH", kept, err)
	}
	if err := wait(mine); err != nil {
		t.Fatalf("WaitApplied(%+v), an entry of the current history compacted since: %v", mine, err)
	}
}
