package consensus

import (
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// survivor is a stopped data directory whose log holds entries 1..30 of
// term 1, then tail entries of term tailTerm through last, under hs.
func survivor(t *testing.T, tailTerm, last uint64, hs coreconsensus.HardState) DataDir {
	t.Helper()
	d := openDir(t, t.TempDir(), 1<<16)
	if err := d.Log().Append(ents(1, 30, 1), nil); err != nil {
		t.Fatal(err)
	}
	if err := d.Log().Append(ents(31, last, tailTerm), &hs); err != nil {
		t.Fatal(err)
	}
	mustSync(t, d.Log())
	return d
}

// The survivor of a Recover is the one with the highest (lastLogTerm,
// lastLogIndex) (D17), and Inspect did not report the first half: X, a
// deposed leader's longer tail no majority held, and Y, shorter but holding
// the newer term a majority committed, showed the same HardState.Term, and
// an operator comparing what Inspect showed picked X and lost Y's committed
// writes (#534).
func TestInspectReportsTheTermOfTheLastEntry(t *testing.T) {
	x := survivor(t, 2, 52, coreconsensus.HardState{Term: 3, Commit: 32})
	defer x.Close()
	y := survivor(t, 3, 42, coreconsensus.HardState{Term: 3, Vote: 7, Commit: 37})
	defer y.Close()
	rx := must(Inspect(x))(t)
	ry := must(Inspect(y))(t)
	if rx.Last != 52 || rx.LastTerm != 2 {
		t.Errorf("X: Last %d LastTerm %d, want 52 and 2", rx.Last, rx.LastTerm)
	}
	if ry.Last != 42 || ry.LastTerm != 3 {
		t.Errorf("Y: Last %d LastTerm %d, want 42 and 3", ry.Last, ry.LastTerm)
	}
	if rx.HardState.Term != ry.HardState.Term {
		t.Fatalf("setup: the two hard states must not tell them apart: %d %d", rx.HardState.Term, ry.HardState.Term)
	}
	better := func(a, b ReportValue) bool {
		return a.LastTerm > b.LastTerm || (a.LastTerm == b.LastTerm && a.Last > b.Last)
	}
	if !better(ry, rx) {
		t.Errorf("the rule (LastTerm, Last) read from Inspect does not choose Y: X %+v, Y %+v", rx, ry)
	}
}

// A snapshot an interrupted install published past the log holds Last, and
// its term is the last term (#506, #534).
func TestInspectReportsTheTermOfASnapshotPastTheLog(t *testing.T) {
	d := openDir(t, t.TempDir(), 1<<16)
	defer d.Close()
	if err := d.Log().Append(ents(1, 5, 1), nil); err != nil {
		t.Fatal(err)
	}
	mustSync(t, d.Log())
	id := "00000000000000000010-00000000000000000004"
	meta := coreconsensus.SnapshotMetaValue{ID: id, Position: coreconsensus.PositionValue{Term: 4, Index: 10}}
	sink, _, err := d.Snapshots().Create(id, meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Commit(meta); err != nil {
		t.Fatalf("publish: %v %v", err, errs.FieldsOf(err))
	}
	r := must(Inspect(d))(t)
	if r.Last != 10 || r.LastTerm != 4 {
		t.Fatalf("Last %d LastTerm %d, want the snapshot's 10 and 4", r.Last, r.LastTerm)
	}
}
