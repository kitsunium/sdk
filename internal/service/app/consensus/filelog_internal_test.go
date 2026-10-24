package consensus

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/fs/flock"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

func ents(from, to, term uint64) []coreconsensus.EntryValue {
	var out []coreconsensus.EntryValue
	for i := from; i <= to; i++ {
		d := []byte(fmt.Sprintf("entry-%d-%d", i, term))
		out = append(out, coreconsensus.EntryValue{
			Position: coreconsensus.PositionValue{Term: term, Index: i},
			Kind:     coreconsensus.EntryCommand, BodySum: wire.BodySum(d), Data: d,
		})
	}
	return out
}

func openDir(t *testing.T, dir string, seg int64) DataDir {
	t.Helper()
	d, err := NewDataDirWith(dir, DataDirOptions{SegmentBytes: seg, RuntimeDir: testRuntimeDir()})
	//: no flock(2), no data directory (illumos, Solaris): the refusal is
	//: asserted, then the case skips — it needs a directory that exists.
	if !flock.Native {
		if !errors.Is(err, coreproc.UnsupportedPlatform) {
			t.Fatalf("NewDataDirWith on %s = %v, want UNSUPPORTED_PLATFORM", runtime.GOOS, err)
		}
		if _, statErr := os.Stat(dir + "/" + fileLock); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("a refused data directory was written to: %v", statErr)
		}
		t.Skipf("%s has no flock(2), so a data directory is refused there (ADR 0144); the refusal is asserted above", runtime.GOOS)
	}
	if err != nil {
		t.Fatalf("open: %v %v", err, errs.FieldsOf(err))
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
	if got := must(l.HardState())(t); got != hs {
		t.Fatalf("hard state %+v", got)
	}
	segs := must(filepath.Glob(filepath.Join(dir, "log", "seg-????????????????")))(t)
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
	// Published segments only: the next one is being prepared as a .tmp.
	after := must(filepath.Glob(filepath.Join(dir, "log", "seg-????????????????")))(t)
	if len(after) >= len(segs) {
		t.Fatalf("no segment dropped: %d then %d", len(segs), len(after))
	}
	// Past the end there is no entry to read the term from: refused
	// without one, recorded with the one the engine gives (#415).
	if err := l.CompactPrefix(90); !errors.Is(err, coreconsensus.StorageFailed) {
		t.Fatalf("a compaction past the end with no term: %v", err)
	}
	if err := d.d.log.compactPrefixAt(90, 4); err != nil {
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
	return appendRecord(nil, &record{
		kind: kind, batch: batch, index: index, term: 1, end: end,
		ekind: uint8(coreconsensus.EntryCommand), data: []byte("x"),
	})
}

func reopenExpect(t *testing.T, dir string, d DataDir, quarantine bool) DataDir {
	t.Helper()
	d.d.log.Close()
	if err := d.Close(); err != nil {
		t.Fatalf("close before reopening: %v", err)
	}
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
	if st := must(nd.Log().Stamp())(t); st.State != 0 {
		t.Fatal("wipe left the stamp")
	}
	nd.Close()
}

// A crash after the seal of segment N and before the rename of N+1: the
// first restart finishes the rotation, and the log it leaves must open
// again, then keep its entries across a third start (review H2).
func TestRecoveryAfterASealSurvivesTwoRestarts(t *testing.T) {
	dir, d := base(t)
	l := d.d.log
	tail(t, d, appendRecord(nil, &record{kind: recSeal, end: true, batch: l.batch, index: uint64(l.off)}), 0)
	d = reopenExpect(t, dir, d, false)
	if n := len(d.d.log.segs); n != 2 {
		t.Fatalf("%d segments after the first restart, want 2", n)
	}
	if err := d.Log().Append(ents(4, 6, 1), nil); err != nil {
		t.Fatal(err)
	}
	mustSync(t, d.Log())
	d = reopenExpect(t, dir, d, false)
	checkEntries(t, d.Log(), 1, 6, 1)
	d = reopenExpect(t, dir, d, false)
	checkEntries(t, d.Log(), 1, 6, 1)
	d.Close()
}

func TestDataDirLockAndStopMarkers(t *testing.T) {
	dir := t.TempDir()
	d := openDir(t, dir, 1<<16)
	defer d.Close()
	if _, err := NewDataDir(dir); !errors.Is(err, coreconsensus.DataDirLocked) {
		t.Fatalf("second open: %v", err)
	}
	l := d.d.log
	// The boot-scoped place is /run on Linux, which only root may create
	// into: the suite points it at a directory of its own, so the second
	// marker is asserted rather than silently absent.
	l.runDir = filepath.Join(t.TempDir(), "run")
	if dirErr, runErr := l.markStopEach(coreconsensus.CodeStorageFailed, "fdatasync: EIO"); dirErr != nil || runErr != nil {
		t.Fatalf("markers not written: %v, %v", dirErr, runErr)
	}
	inDir := filepath.Join(dir, markerStopPrefix+"STORAGE_FAILED")
	for _, p := range []string{inDir, l.runtimeMarker("STORAGE_FAILED")} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("STORAGE_FAILED marker missing: %v", err)
		}
	}
	if err := l.checkMarkers(); !errors.Is(err, coreconsensus.StopMarkerPresent) {
		t.Fatalf("same boot: %v", err)
	}
	// EITHER marker refuses on its own (R9-05): with the data directory's
	// gone, the boot-scoped one still holds the node in this boot.
	body, err := os.ReadFile(inDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(inDir); err != nil {
		t.Fatal(err)
	}
	if err := l.checkMarkers(); !errors.Is(err, coreconsensus.StopMarkerPresent) {
		t.Fatalf("boot-scoped marker alone: %v", err)
	}
	if err := os.WriteFile(inDir, body, 0o600); err != nil {
		t.Fatal(err)
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
	// A final stop writes no boot-scoped marker: runErr is nil by contract.
	if dirErr, runErr := l.markStopEach(coreconsensus.CodeStateMachineFailed, "panic"); dirErr != nil || runErr != nil {
		t.Fatal(dirErr, runErr)
	}
	if err := l.checkMarkers(); !errors.Is(err, coreconsensus.StopMarkerPresent) {
		t.Fatalf("final stop marker: %v", err)
	}
}

// R3-24, R4-01: the next segment is prepared in the background as a .tmp,
// never published early; a crash with it present loses nothing and
// publishes nothing, and the rotation that takes it writes the current
// hard state into its checkpoint.
func TestPreparedSegmentIsNeverPublishedEarly(t *testing.T) {
	dir := t.TempDir()
	d := openDir(t, dir, 4096)
	l := d.Log()
	if err := l.Append(ents(1, 5, 1), &coreconsensus.HardState{Term: 1, Vote: 2}); err != nil {
		t.Fatal(err)
	}
	mustSync(t, l)
	fl := d.d.log
	fl.mu.Lock()
	p := fl.prep
	fl.mu.Unlock()
	if p == nil {
		t.Fatal("no segment being prepared")
	}
	<-p.done
	tmps := must(filepath.Glob(filepath.Join(dir, "log", "*.tmp")))(t)
	if len(tmps) != 1 {
		t.Fatalf("prepared files: %v", tmps)
	}
	// Rotate onto it with a newer hard state.
	if err := l.Append(ents(6, 60, 3), &coreconsensus.HardState{Term: 3, Vote: 1}); err != nil {
		t.Fatal(err)
	}
	mustSync(t, l)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d = openDir(t, dir, 4096)
	defer d.Close()
	checkEntries(t, d.Log(), 1, 5, 1)
	checkEntries(t, d.Log(), 6, 60, 3)
	if hs := must(d.Log().HardState())(t); hs.Term != 3 || hs.Vote != 1 {
		t.Fatalf("hard state %+v", hs)
	}
}

// A crash between a snapshot's rename and its .meta leaves a .snap that is
// not a snapshot: the next open removes it (review of PR #301, L4).
func TestOpenRemovesASnapshotWithoutItsMeta(t *testing.T) {
	dir := t.TempDir()
	d := openDir(t, dir, 1<<16)
	d.Close()
	orphan := filepath.Join(dir, "snap", "orphan.snap")
	if err := os.WriteFile(orphan, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	d = openDir(t, dir, 1<<16)
	defer d.Close()
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan .snap kept: %v", err)
	}
}

// countingSink counts how often its handle is released.
type countingSink struct {
	coreconsensus.SnapshotSink
	released *int
}

func (k countingSink) release() { *k.released++ }

// A second offer for a spool still open releases the first handle instead
// of leaking it; the spool itself stays, to be resumed (review of PR #301,
// L4).
func TestSecondOfferReleasesTheFirstSpoolHandle(t *testing.T) {
	var n Node
	released := 0
	n.snapState.mu.Lock()
	n.keepSpool("s1", countingSink{released: &released})
	n.keepSpool("s1", countingSink{released: &released})
	n.snapState.mu.Unlock()
	if released != 1 {
		t.Fatalf("released %d handles, want 1", released)
	}
}

// A reader is not held behind a batch's write and fdatasync: the entries
// already durable, and the pending ones in memory, stay readable while the
// sync runs (review of PR #301, L6).
func TestReadersDoNotWaitForASync(t *testing.T) {
	_, d := base(t)
	defer d.Close()
	l := d.d.log
	if err := l.Append(ents(4, 6, 1), nil); err != nil {
		t.Fatal(err)
	}
	in, out := make(chan struct{}), make(chan struct{})
	var first sync.Once
	l.duringSync = func() {
		first.Do(func() { close(in) })
		<-out
	}
	done := make(chan error, 1)
	go func() { done <- l.Sync() }()
	<-in
	read := make(chan error, 1)
	go func() {
		_, err := l.Entries(1, 7, 1<<20, nil)
		read <- err
	}()
	select {
	case err := <-read:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		close(out)
		t.Fatal("a reader waited for the sync")
	}
	close(out)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	checkEntries(t, d.Log(), 1, 6, 1)
}

// copyTree copies every regular file under src to dst, as a restart reads
// them: through the page cache, synced or not. The lock file is left out:
// Windows refuses to read a file another handle locks, and the copy takes
// its own lock.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if e.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o700)
		}
		if rel == fileLock {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o600)
	})
}

// A process killed between a batch's write and its fdatasync leaves the
// batch in the page cache, end record included: a restart in the same boot
// reads it as complete. Recovery makes what it trusts durable before the
// node acknowledges anything from it, or a power cut within the write-back
// delay loses entries already acknowledged (#414).
func TestRecoverySyncsWhatItTrustsBeforeServing(t *testing.T) {
	_, d := base(t)
	defer d.Close()
	l := d.d.log
	if err := l.Append(ents(4, 6, 1), nil); err != nil {
		t.Fatal(err)
	}
	killed := t.TempDir()
	var copied error
	l.duringSync = func() { copied = copyTree(d.Dir(), killed) }
	mustSync(t, l)
	if copied != nil {
		t.Fatal(copied)
	}
	nd := openDir(t, killed, 1<<16)
	defer nd.Close()
	if f, la := nd.Log().Bounds(); f != 1 || la != 6 {
		t.Fatalf("setup: the unsynced batch was not read back: bounds %d %d", f, la)
	}
	got := nd.d.log
	if got.syncedAtOpen != len(got.segs) || got.syncedAtOpen == 0 {
		t.Fatalf("recovery trusted %d segment(s) and synced %d before serving them", len(got.segs), got.syncedAtOpen)
	}
}

// An install past the end of the log truncates it and compacts it to the
// snapshot's index. The store holds no entry there to read the term from:
// it used to record 0, and a node that restarted with no table then based
// its vote on a last term of 0 and granted it to a stale candidate. The
// term comes from the engine, and a compaction past the end without one is
// refused (#415).
func TestACompactionPastTheEndRecordsTheTermItIsGiven(t *testing.T) {
	dir, d := base(t)
	l := d.d.log
	if err := l.compactPrefixAt(2, 7); !errors.Is(err, coreconsensus.StorageFailed) {
		t.Fatalf("a term that disagrees with the log's: %v", err)
	}
	if err := l.TruncateSuffix(1); err != nil {
		t.Fatal(err)
	}
	if err := l.CompactPrefix(100); !errors.Is(err, coreconsensus.StorageFailed) {
		t.Fatalf("a compaction past the end with no term: %v", err)
	}
	if err := l.compactPrefixAt(100, 5); err != nil {
		t.Fatal(err)
	}
	mustSync(t, l)
	nd := reopenExpect(t, dir, d, false)
	defer nd.Close()
	if f, la := nd.Log().Bounds(); f != 101 || la != 100 {
		t.Fatalf("bounds %d %d, want 101 100", f, la)
	}
	if tm, err := nd.Log().Term(100); err != nil || tm != 5 {
		t.Fatalf("Term(100) = %d %v, want 5", tm, err)
	}
	base, err := noTableBase(nd.Log(), nil, 101)
	if err != nil || base.Position != (coreconsensus.PositionValue{Term: 5, Index: 100}) {
		t.Fatalf("no-table base %+v %v, want (100, term 5)", base.Position, err)
	}
}

// #516: a compaction past the log's end moves the log's start past it, and
// the next index is appended after it — the state of a node that installed
// a snapshot and caught up. Replay applies the compact record where it
// stands: noted, and applied only once the whole replay was done, it left
// the log starting below the appended entries, and the restart quarantined
// a sound disk, "index out of sequence".
func TestAnAppendAfterACompactionPastTheEndSurvivesARestart(t *testing.T) {
	type tc struct {
		name string
		// truncated takes the install path: the suffix goes, then the
		// compaction.
		truncated bool
	}
	runCase := func(t *testing.T, c tc) {
		dir, d := base(t)
		l := d.d.log
		if c.truncated {
			if err := l.TruncateSuffix(1); err != nil {
				t.Fatal(err)
			}
		}
		if err := l.compactPrefixAt(100, 5); err != nil {
			t.Fatal(err)
		}
		mustSync(t, l)
		if err := l.Append(ents(101, 103, 5), nil); err != nil {
			t.Fatal(err)
		}
		mustSync(t, l)
		nd := reopenExpect(t, dir, d, false)
		defer nd.Close()
		if f, la := nd.Log().Bounds(); f != 101 || la != 103 {
			t.Fatalf("bounds %d %d, want 101 103", f, la)
		}
		checkEntries(t, nd.Log(), 101, 103, 5)
		if tm, err := nd.Log().Term(100); err != nil || tm != 5 {
			t.Fatalf("Term(100) = %d %v, want 5", tm, err)
		}
	}
	for _, c := range []tc{{name: "compacted"}, {name: "truncated then compacted", truncated: true}} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// #516, across segments: the appends after a compaction past the end rotate
// into later segments, whose checkpoints must continue the replayed log.
func TestAppendsAfterACompactionPastTheEndRotateAndSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	d := openDir(t, dir, 4096)
	l := d.d.log
	if err := l.Append(ents(1, 3, 1), nil); err != nil {
		t.Fatal(err)
	}
	mustSync(t, l)
	if err := l.compactPrefixAt(100, 5); err != nil {
		t.Fatal(err)
	}
	mustSync(t, l)
	for i := uint64(101); i <= 160; i++ {
		if err := l.Append(ents(i, i, 5), nil); err != nil {
			t.Fatal(err)
		}
		mustSync(t, l)
	}
	if segs := must(filepath.Glob(filepath.Join(dir, "log", "seg-????????????????")))(t); len(segs) < 2 {
		t.Fatalf("setup: %d segment(s), want a rotation", len(segs))
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	nd := openDir(t, dir, 4096)
	defer nd.Close()
	if err := nd.d.log.checkMarkers(); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if f, la := nd.Log().Bounds(); f != 101 || la != 160 {
		t.Fatalf("bounds %d %d, want 101 160", f, la)
	}
	checkEntries(t, nd.Log(), 101, 160, 5)
}

// A log compacted past its end before #415 answers a term of 0 at its base.
// A node with no table takes the term of the snapshot metadata at that
// index, which stays readable when the bytes do not, and refuses to start
// when there is none: a base term of 0 grants a vote to any candidate.
func TestANoTableBaseNeverTakesATermOfZero(t *testing.T) {
	log := NewMemoryLog()
	if err := log.Append(ents(1, 3, 1), nil); err != nil {
		t.Fatal(err)
	}
	if err := log.TruncateSuffix(1); err != nil {
		t.Fatal(err)
	}
	if err := log.CompactPrefix(100); err != nil {
		t.Fatal(err)
	}
	if tm, _ := log.Term(100); tm != 0 {
		t.Fatalf("setup: the base term is %d, want the 0 a compaction with no term leaves", tm)
	}
	at := func(index, term uint64) coreconsensus.SnapshotMetaValue {
		return coreconsensus.SnapshotMetaValue{ID: fmt.Sprintf("s%d", index), Position: coreconsensus.PositionValue{Term: term, Index: index}}
	}
	got, err := noTableBase(log, []coreconsensus.SnapshotMetaValue{at(100, 5), at(60, 4)}, 101)
	if err != nil || got.Position != (coreconsensus.PositionValue{Term: 5, Index: 100}) {
		t.Fatalf("base %+v %v, want the metadata's term 5", got.Position, err)
	}
	if got, err := noTableBase(log, []coreconsensus.SnapshotMetaValue{at(60, 4)}, 101); err == nil {
		t.Fatalf("a base of term 0 at index 100 was accepted: %+v", got.Position)
	}
	if got, err := noTableBase(log, nil, 101); err == nil {
		t.Fatalf("a base of term 0 at index 100 was accepted: %+v", got.Position)
	}
	if _, err := noTableBase(NewMemoryLog(), nil, 1); err != nil {
		t.Fatalf("an empty log's base (0, 0): %v", err)
	}
	// The engine gives the term: the base answers it.
	given := NewMemoryLog()
	if err := given.compactPrefixAt(100, 5); err != nil {
		t.Fatal(err)
	}
	if got, err := noTableBase(given, nil, 101); err != nil || got.Position.Term != 5 {
		t.Fatalf("base %+v %v, want term 5", got.Position, err)
	}
}

// A power cut during a batch's fdatasync may persist any subset of its
// pages, the end record's without an earlier one's. With one sync per batch
// a batch never acknowledged then read as damage inside a completed batch,
// and was quarantined. The end record is written only once the rest of its
// batch is durable, so a valid end record after a hole is damage to synced
// data and nothing else (#417).
func TestTheEndRecordIsWrittenOnceTheRestOfItsBatchIsDurable(t *testing.T) {
	_, d := base(t)
	defer d.Close()
	l := d.d.log
	if err := l.Append(ents(4, 6, 1), nil); err != nil {
		t.Fatal(err)
	}
	batch, off := l.batch, l.off
	seg := filepath.Join(l.logDir(), segName(l.segs[len(l.segs)-1].n))
	type seen struct{ recs, ends int }
	var syncs []seen
	l.duringSync = func() {
		b, err := os.ReadFile(seg)
		if err != nil {
			t.Error(err)
			return
		}
		var s seen
		for o := int(off); o+recHeader+recFixed <= len(b); {
			r, ok := parseRecord(b[o:])
			if !ok || r.batch != batch {
				break
			}
			s.recs++
			if r.end {
				s.ends++
			}
			o += r.length
		}
		syncs = append(syncs, s)
	}
	mustSync(t, l)
	if len(syncs) != 2 {
		t.Fatalf("%d fdatasync(s) for a batch of three records (%+v), want the body's then the end record's", len(syncs), syncs)
	}
	if syncs[0].ends != 0 || syncs[0].recs != 2 {
		t.Fatalf("before the body was durable the disk held %+v, want its two records and no end record", syncs[0])
	}
	if syncs[1].ends != 1 || syncs[1].recs != 3 {
		t.Fatalf("the end record's write left %+v, want three records, the last ending the batch", syncs[1])
	}
	checkEntries(t, d.Log(), 1, 6, 1)
}

// A snapshot pinned by a transfer is a benign refusal, retried at the next
// cut: it is not STORAGE_FAILED, which stops the node (#422).
func TestRemovingAPinnedSnapshotIsNotAStorageFailure(t *testing.T) {
	_, d := base(t)
	defer d.Close()
	body := []byte("snapshot body")
	meta := coreconsensus.SnapshotMetaValue{ID: "s1", Position: coreconsensus.PositionValue{Term: 1, Index: 3}, EncodedSize: int64(len(body))}
	mem := NewMemorySnapshots()
	for _, s := range []coreconsensus.SnapshotStore{d.Snapshots(), mem} {
		sink, _, err := s.Create(meta.ID, meta)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sink.Write(body); err != nil {
			t.Fatal(err)
		}
		if err := sink.Commit(meta); err != nil {
			t.Fatal(err)
		}
		h, err := s.Open(meta.ID)
		if err != nil {
			t.Fatal(err)
		}
		err = s.Remove(meta.ID)
		if err == nil || errors.Is(err, coreconsensus.StorageFailed) || !errors.Is(err, coreconsensus.AdminPreconditionFailed) {
			t.Fatalf("%T: removing a pinned snapshot: %v, want ADMIN_PRECONDITION_FAILED", s, err)
		}
		h.Close()
		if err := s.Remove(meta.ID); err != nil {
			t.Fatalf("%T: removing it once unpinned: %v", s, err)
		}
	}
}

// WipeAndRejoin starts a new member: a silence condition an earlier
// TruncateAndStaySilent left must go with the stamp, or the new member
// starts silent at the old log's index (#422).
func TestWipeAndRejoinRemovesTheSilenceCondition(t *testing.T) {
	dir, d := base(t)
	defer d.Close()
	if err := writeFileSync(filepath.Join(dir, fileSilent), silenceRecord(9, 4)); err != nil {
		t.Fatal(err)
	}
	if err := ClearQuarantine(d, WipeAndRejoin, "op:test"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, fileSilent)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the silence condition survived the wipe: %v", err)
	}
}

// A spool resumes at its last chunk boundary. A region of zeros, a spool a
// crash left unsynced, read as a run of empty chunks whose CRC of nothing is
// 0: the resume continued past it, and only the final digest refused the
// install. The writer never emits an empty chunk, so one ends the prefix
// (#422).
func TestASpoolResumesBeforeARegionOfZeros(t *testing.T) {
	payload := []byte("abc")
	var h [chunkHeaderLen]byte
	binary.LittleEndian.PutUint32(h[0:], uint32(len(payload)))
	binary.LittleEndian.PutUint32(h[8:], crc32.Checksum(payload, castagnoli))
	prefix := append(append(h[:], payload...), make([]byte, 4*chunkHeaderLen)...)
	got, err := alignedOffsetAt(bytes.NewReader(prefix), int64(len(prefix)), maxEncodedChunk(defaultChunkSize))
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(chunkHeaderLen + len(payload)); got != want {
		t.Fatalf("resume offset %d over a chunk then zeros, want %d", got, want)
	}
}

// fillLog appends synced batches of about 512 KiB of entries of payload
// bytes to l until its segments occupy size bytes, and returns the last
// index.
func fillLog(t *testing.T, l *fileLog, payload int, size int64) uint64 {
	t.Helper()
	data := make([]byte, payload)
	per := max(1, (512<<10)/(payload+recHeader+recFixed))
	var idx uint64
	for l.logBytes() < size {
		es := make([]coreconsensus.EntryValue, 0, per)
		for range per {
			idx++
			es = append(es, coreconsensus.EntryValue{
				Position: coreconsensus.PositionValue{Term: 1, Index: idx},
				Kind:     coreconsensus.EntryCommand, BodySum: wire.BodySum(data), Data: data,
			})
		}
		if err := l.Append(es, nil); err != nil {
			t.Fatal(err)
		}
		if err := l.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	return idx
}

// A reopen of the file log read every segment whole and kept them all, with
// a record struct per record pointing into them, until recover returned:
// the transient heap was one to three times the log on disk, and a node
// whose log grew while its compaction stalled was OOM-killed at every
// restart on a small host, Inspect and WipeAndRejoin with it (#630). The
// open reads one segment at a time into the same buffer and keeps 40 bytes
// per record past it, beside the index's 32 per entry: a 32 MiB log in
// 4 MiB segments, of entries of 4 KiB, where the open costs about a
// segment, and of 64 B, where the per-record cost dominates. What the
// reopen allocates in all bounds its peak from above, whenever the
// collector ran.
func TestReopeningTheLogHoldsOneSegmentAtATime(t *testing.T) {
	const segBytes, logSize = 4 << 20, 32 << 20
	for _, payload := range []int{4096, 64} {
		dir := t.TempDir()
		d := openDir(t, dir, segBytes)
		last := fillLog(t, d.d.log, payload, logSize)
		onDisk := d.d.log.logBytes()
		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
		// The closed log's index goes too: the heap starts without a log.
		d = DataDir{}
		var before, opened, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		d = openDir(t, dir, segBytes)
		runtime.ReadMemStats(&opened)
		runtime.GC()
		runtime.ReadMemStats(&after)
		allocated := int64(opened.TotalAlloc - before.TotalAlloc)
		retained := int64(after.HeapAlloc) - int64(before.HeapAlloc)
		if f, la := d.Log().Bounds(); f != 1 || la != last {
			t.Fatalf("bounds %d..%d after the reopen, want 1..%d", f, la, last)
		}
		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
		// Three segments and 160 bytes an entry, about twice what the open
		// allocates. Reading every segment whole took the eight segments,
		// and a record of 88 bytes an entry, grown by append, several
		// times over: about three times this bound.
		bound := 3*segBytes + 160*int64(last)
		t.Logf("entries of %d B: %d of them, a log of %.1f MiB on disk; the reopen allocated %.1f MiB, bound %.1f MiB, and retained %.1f MiB",
			payload, last, mib(onDisk), mib(allocated), mib(bound), mib(retained))
		if allocated > bound {
			t.Errorf("entries of %d B: reopening a log of %.1f MiB allocated %.1f MiB, over %.1f MiB: the open holds the log, not a segment at a time",
				payload, mib(onDisk), mib(allocated), mib(bound))
		}
	}
}

// A sealed segment is read up to its seal (R9-02), not to its
// preallocated size: the scan read every segment whole (#630). Segment 1,
// of 4 MiB, sealed after three entries, as a crash after a rotation's seal
// leaves it; the open that follows finishes the rotation on the records it
// read (review H2).
func TestASealedSegmentIsReadUpToItsSeal(t *testing.T) {
	const segBytes = 4 << 20
	dir := t.TempDir()
	d := openDir(t, dir, segBytes)
	l := d.d.log
	if err := l.Append(ents(1, 3, 1), nil); err != nil {
		t.Fatal(err)
	}
	mustSync(t, l)
	seal := appendRecord(nil, &record{kind: recSeal, end: true, batch: l.batch, index: uint64(l.off)})
	sealEnd := l.off + int64(len(seal))
	tail(t, d, seal, 0)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := (&fileLog{dir: dir}).scanSegment(1, &scanBufs{})
	if err != nil {
		t.Fatal(err)
	}
	if !s.sealed || s.size != segBytes {
		t.Fatalf("segment 1: sealed %v, size %d, want sealed, of %d", s.sealed, s.size, segBytes)
	}
	if read := int64(len(s.data)); read >= sealEnd+scanChunk || read >= s.size {
		t.Fatalf("a segment of %d sealed at %d read %d bytes, want at most a chunk past its seal", s.size, sealEnd, read)
	}
	d = openDir(t, dir, segBytes)
	defer d.Close()
	checkEntries(t, d.Log(), 1, 3, 1)
	if n := len(d.d.log.segs); n != 2 {
		t.Fatalf("%d segments after the open, want the next one published", n)
	}
}

// mib is n bytes in MiB.
func mib(n int64) float64 { return float64(n) / (1 << 20) }
