package consensus

import (
	"bytes"
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

// A snapshot an interrupted install published — past the log, or inside it
// under another term — holds the state there: its index is the last index
// and its term the last term, the ones a start or Recover keeps (#506,
// #534, #746). One inside the log under the log's own term, or behind the
// log, is a cut's: the log's tail is kept. Only one that verifies counts,
// as only one that verifies is finished by Recover, and none a cut before
// #544 published at term 0, which the start and Recover set aside.
func TestInspectReportsTheTermOfASnapshotAtLast(t *testing.T) {
	type tc struct {
		name    string
		logLast uint64
		snap    coreconsensus.PositionValue
		corrupt bool
		want    coreconsensus.PositionValue
	}
	cases := []tc{
		{"past the log", 5, coreconsensus.PositionValue{Term: 4, Index: 10}, false, coreconsensus.PositionValue{Term: 4, Index: 10}},
		{"at the log's last index, another term", 10, coreconsensus.PositionValue{Term: 4, Index: 10}, false, coreconsensus.PositionValue{Term: 4, Index: 10}},
		{"inside the log, another term", 12, coreconsensus.PositionValue{Term: 4, Index: 10}, false, coreconsensus.PositionValue{Term: 4, Index: 10}},
		{"inside the log, the log's term", 12, coreconsensus.PositionValue{Term: 1, Index: 10}, false, coreconsensus.PositionValue{Term: 1, Index: 12}},
		{"past the log, failing its checksums", 5, coreconsensus.PositionValue{Term: 4, Index: 10}, true, coreconsensus.PositionValue{Term: 1, Index: 5}},
		{"inside the log, another term, failing its checksums", 12, coreconsensus.PositionValue{Term: 4, Index: 10}, true, coreconsensus.PositionValue{Term: 1, Index: 12}},
		{"past the log at term 0, a cut before #544", 5, coreconsensus.PositionValue{Term: 0, Index: 10}, false, coreconsensus.PositionValue{Term: 1, Index: 5}},
		{"inside the log at term 0, a cut before #544", 12, coreconsensus.PositionValue{Term: 0, Index: 10}, false, coreconsensus.PositionValue{Term: 1, Index: 12}},
	}
	runCase := func(t *testing.T, c tc) {
		d := openDir(t, t.TempDir(), 1<<16)
		defer d.Close()
		if err := d.Log().Append(ents(1, c.logLast, 1), nil); err != nil {
			t.Fatal(err)
		}
		mustSync(t, d.Log())
		publishAt(t, d, c.snap, c.corrupt)
		r := must(Inspect(d))(t)
		if r.Last != c.want.Index || r.LastTerm != c.want.Term {
			t.Fatalf("Last %d LastTerm %d, want %d and %d", r.Last, r.LastTerm, c.want.Index, c.want.Term)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// publishAt publishes in d a snapshot at pos, of the version this build
// writes and with a stream that verifies, or, with corrupt, a stream whose
// digest does not match its metadata.
func publishAt(t *testing.T, d DataDir, pos coreconsensus.PositionValue, corrupt bool) {
	t.Helper()
	codec, ok := defaultCodec()
	if !ok {
		t.Fatal("no default codec")
	}
	var body bytes.Buffer
	cw := newChunkWriter(&body, codec, defaultChunkSize)
	if _, err := cw.Write([]byte("the state at " + snapshotID(pos.Index, pos.Term))); err != nil {
		t.Fatal(err)
	}
	if err := cw.Close(); err != nil {
		t.Fatal(err)
	}
	meta := coreconsensus.SnapshotMetaValue{
		ID: snapshotID(pos.Index, pos.Term), Version: 1, Position: pos,
		Codec: string(codec.Algorithm()), ChunkSize: defaultChunkSize,
		EncodedSize: cw.encoded, DecodedSize: cw.decoded,
	}
	copy(meta.SHA256[:], cw.sum.Sum(nil))
	if corrupt {
		meta.SHA256[0] ^= 0xff
	}
	sink, _, err := d.Snapshots().Create(meta.ID, meta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Write(body.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := sink.Commit(meta); err != nil {
		t.Fatalf("publish: %v %v", err, errs.FieldsOf(err))
	}
}
