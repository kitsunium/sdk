package consensus

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

func ents(from, to, term uint64) []coreconsensus.Entry {
	var out []coreconsensus.Entry
	for i := from; i <= to; i++ {
		d := []byte(fmt.Sprintf("entry-%d-%d", i, term))
		out = append(out, coreconsensus.Entry{Position: coreconsensus.Position{Term: term, Index: i},
			Kind: coreconsensus.EntryCommand, BodySum: wire.BodySum(d), Data: d})
	}
	return out
}

func openDir(t *testing.T, dir string, seg int64) DataDir {
	t.Helper()
	d, err := OpenDataDirWith(dir, DataDirOptions{SegmentBytes: seg})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return d
}

func mustSync(t *testing.T, l coreconsensus.LogStore) {
	t.Helper()
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}
}

func checkEntries(t *testing.T, l coreconsensus.LogStore, lo, hi, term uint64) {
	t.Helper()
	got, err := l.Entries(lo, hi+1, 1<<30, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != int(hi-lo+1) {
		t.Fatalf("got %d entries, want %d", len(got), hi-lo+1)
	}
	for _, e := range got {
		if want := fmt.Sprintf("entry-%d-%d", e.Position.Index, term); string(e.Data) != want || e.Position.Term != term {
			t.Fatalf("entry %d: %q term %d", e.Position.Index, e.Data, e.Position.Term)
		}
	}
}

func TestFileLogPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	d := openDir(t, dir, 4096)
	l := d.Log()
	hs := coreconsensus.HardState{Term: 3, Vote: 7, Commit: 5}
	if err := l.Append(ents(1, 40, 3), &hs); err != nil {
		t.Fatal(err)
	}
	// Readers see appended entries before the sync (D4).
	checkEntries(t, l, 1, 40, 3)
	mustSync(t, l)
	if err := l.TruncateSuffix(31); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(ents(31, 60, 4), nil); err != nil {
		t.Fatal(err)
	}
	mustSync(t, l)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d = openDir(t, dir, 4096)
	defer d.Close()
	l = d.Log()
	if f, la := l.Bounds(); f != 1 || la != 60 {
		t.Fatalf("bounds %d %d", f, la)
	}
	checkEntries(t, l, 1, 30, 3)
	checkEntries(t, l, 31, 60, 4)
	if got, _ := l.HardState(); got != hs {
		t.Fatalf("hard state %+v", got)
	}
	segs, _ := os.ReadDir(filepath.Join(dir, "log"))
	if len(segs) < 2 {
		t.Fatalf("no rotation with 4 KiB segments: %d segment(s)", len(segs))
	}
	// Compaction drops whole segments, and a compaction past the end leaves
	// the empty log (s+1, s).
	if err := l.CompactPrefix(50); err != nil {
		t.Fatal(err)
	}
	mustSync(t, l)
	if f, _ := l.Bounds(); f != 51 {
		t.Fatalf("first %d after compaction", f)
	}
	if tm, err := l.Term(50); err != nil || tm != 4 {
		t.Fatalf("term at first-1: %d %v", tm, err)
	}
	if _, err := l.Entries(10, 11, 100, nil); !errors.Is(err, coreconsensus.Compacted) {
		t.Fatalf("compacted read: %v", err)
	}
	after, _ := os.ReadDir(filepath.Join(dir, "log"))
	if len(after) >= len(segs) {
		t.Fatalf("no segment dropped: %d then %d", len(segs), len(after))
	}
	if err := l.CompactPrefix(90); err != nil {
		t.Fatal(err)
	}
	mustSync(t, l)
	if f, la := l.Bounds(); f != 91 || la != 90 {
		t.Fatalf("bounds after compacting past the end: %d %d", f, la)
	}
	d.Close()
	d = openDir(t, dir, 4096)
	defer d.Close()
	if f, la := d.Log().Bounds(); f != 91 || la != 90 {
		t.Fatalf("bounds after reopen: %d %d (%v)", f, la, errs.FieldsOf(d.d.log.refuse))
	}
}

// tail writes raw bytes at the log's current write offset, as a crash would
// have left them.
func tail(t *testing.T, d DataDir, b []byte, at int64) {
	t.Helper()
	l := d.d.log
	seg := l.segs[len(l.segs)-1]
	if _, err := seg.f.WriteAt(b, l.off+at); err != nil {
		t.Fatal(err)
	}
}

func rec(kind uint8, batch, index uint64, end bool) []byte {
	return appendRecord(nil, &record{kind: kind, batch: batch, index: index, term: 1, end: end,
		ekind: uint8(coreconsensus.EntryCommand), data: []byte("x")})
}

func reopenExpect(t *testing.T, dir string, d DataDir, quarantine bool) DataDir {
	t.Helper()
	d.d.log.Close()
	_ = d.Close()
	nd := openDir(t, dir, 1<<16)
	err := nd.d.log.checkMarkers()
	if quarantine != errs.HasCode(err, coreconsensus.CodeLogQuarantined) {
		t.Fatalf("quarantine=%v, got %v", quarantine, err)
	}
	return nd
}

func base(t *testing.T) (string, DataDir) {
	dir := t.TempDir()
	d := openDir(t, dir, 1<<16)
	if err := d.Log().Append(ents(1, 3, 1), nil); err != nil {
		t.Fatal(err)
	}
	mustSync(t, d.Log())
	return dir, d
}

// An unfinished highest batch is a tear: never synced, never acknowledged.
func TestRecoveryTruncatesTheUnfinishedBatch(t *testing.T) {
	dir, d := base(t)
	b := d.d.log.batch
	r1 := rec(recEntry, b, 4, false)
	tail(t, d, r1, 0)
	tail(t, d, []byte{0xde, 0xad, 0xbe, 0xef, 1, 2, 3, 4}, int64(len(r1)))
	nd := reopenExpect(t, dir, d, false)
	defer nd.Close()
	if _, la := nd.Log().Bounds(); la != 3 {
		t.Fatalf("last %d, want 3", la)
	}
	// The log keeps working after the repair.
	if err := nd.Log().Append(ents(4, 5, 1), nil); err != nil {
		t.Fatal(err)
	}
	mustSync(t, nd.Log())
}

// Garbage after the last complete batch is a torn next batch.
func TestRecoveryTruncatesGarbageAfterACompleteBatch(t *testing.T) {
	dir, d := base(t)
	tail(t, d, []byte("garbage-garbage-garbage-garbage!"), 0)
	nd := reopenExpect(t, dir, d, false)
	defer nd.Close()
	if _, la := nd.Log().Bounds(); la != 3 {
		t.Fatalf("last %d", la)
	}
}

// R9-01: the header of batch k, which holds one acknowledged entry, is
// zeroed, and a batch k+1 holding only a hard state follows: quarantine.
func TestRecoveryQuarantinesAHoleBeforeALaterBatch(t *testing.T) {
	dir, d := base(t)
	b := d.d.log.batch
	k := rec(recEntry, b, 4, true)
	hs := appendRecord(nil, &record{kind: recHardState, batch: b + 1, end: true, data: encodeHS(coreconsensus.HardState{Term: 1})})
	tail(t, d, k, 0)
	tail(t, d, hs, int64(len(k)))
	tail(t, d, make([]byte, 8), 0) // zero the header of batch k
	nd := reopenExpect(t, dir, d, true)
	defer nd.Close()
	if _, err := os.Stat(filepath.Join(dir, markerQuarantine)); err != nil {
		t.Fatal("no quarantine marker")
	}
}

// R9-03: a bad CRC inside a highest batch whose end record IS valid may be
// acknowledged data: quarantine, never truncate.
func TestRecoveryQuarantinesDamageInACompletedBatch(t *testing.T) {
	dir, d := base(t)
	b := d.d.log.batch
	r1 := rec(recEntry, b, 4, false)
	r2 := rec(recEntry, b, 5, true)
	tail(t, d, r1, 0)
	tail(t, d, r2, int64(len(r1)))
	tail(t, d, []byte{0xff}, int64(recHeader+recFixed)) // flip a payload byte of r1
	nd := reopenExpect(t, dir, d, true)
	r, err := Inspect(nd)
	if err != nil || r.Quarantine == "" {
		t.Fatalf("inspect: %+v %v", r, err)
	}
	// WipeAndRejoin is the exit; the directory is then empty and unstamped.
	if err := ClearQuarantine(nd, WipeAndRejoin, "op:test"); err != nil {
		t.Fatal(err)
	}
	if err := ClearQuarantine(nd, TruncateAndStaySilent, "op:test"); !errors.Is(err, coreconsensus.RecoverRefused) {
		t.Fatalf("truncate exit: %v", err)
	}
	if f, la := nd.Log().Bounds(); la >= f {
		t.Fatal("wipe left entries")
	}
	if st, _ := nd.Log().Stamp(); st.State != 0 {
		t.Fatal("wipe left the stamp")
	}
	nd.Close()
}

func TestDataDirLockAndStopMarkers(t *testing.T) {
	dir := t.TempDir()
	d := openDir(t, dir, 1<<16)
	defer d.Close()
	if _, err := OpenDataDir(dir); !errors.Is(err, coreconsensus.DataDirLocked) {
		t.Fatalf("second open: %v", err)
	}
	l := d.d.log
	if !l.markStop(coreconsensus.CodeStorageFailed, "fdatasync: EIO") {
		t.Fatal("no marker written")
	}
	defer os.Remove(l.runtimeMarker("STORAGE_FAILED"))
	if err := l.checkMarkers(); !errors.Is(err, coreconsensus.StopMarkerPresent) {
		t.Fatalf("same boot: %v", err)
	}
	// Another boot_id in both markers: the node starts (R8-05).
	for _, p := range []string{filepath.Join(dir, markerStopPrefix+"STORAGE_FAILED"), l.runtimeMarker("STORAGE_FAILED")} {
		if err := os.WriteFile(p, []byte("boot=another\nx\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.checkMarkers(); err != nil {
		t.Fatalf("another boot: %v", err)
	}
	l.markStop(coreconsensus.CodeStateMachineFailed, "panic")
	if err := l.checkMarkers(); !errors.Is(err, coreconsensus.StopMarkerPresent) {
		t.Fatalf("final stop marker: %v", err)
	}
}
