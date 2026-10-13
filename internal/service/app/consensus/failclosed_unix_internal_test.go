//go:build !windows

package consensus

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestADroppedSegmentIsSyncedWhenTheNextOneIsRetained pins D4's lifecycle
// step 6 on compaction: an unlink is published only once its directory is
// synced. A pass that removes the first segment and then finds the new first
// one still holding a retained entry must sync before it returns, or the
// unlink it already made is not durable.
//
// The sync is observed by its failure: the log directory is left writable
// (the unlink succeeds) but not readable (opening it for the sync fails), so
// a pass that syncs reports the failure and one that skips the sync reports
// nothing.
func TestADroppedSegmentIsSyncedWhenTheNextOneIsRetained(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory's permission bits")
	}
	dir := t.TempDir()
	logDir := filepath.Join(dir, "log")
	if err := os.Mkdir(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const compacted uint64 = 10
	l := &FileLog{dir: dir, compact: compacted}
	for n := uint64(1); n <= 3; n++ {
		// Every segment opens with a checkpoint at or below the compaction
		// point: each one supersedes the segment before it.
		cp := checkpoint{prevIndex: compacted / 2}
		rec := appendRecord(nil, &record{kind: recCheckpt, data: cp.encode()})
		path := filepath.Join(logDir, segName(n))
		if err := os.WriteFile(path, rec, 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if cerr := f.Close(); cerr != nil && !errors.Is(cerr, os.ErrClosed) {
				t.Error(cerr)
			}
		})
		l.segs = append(l.segs, &segment{n: n, f: f})
	}
	// The second segment still holds a retained entry: the pass removes the
	// first segment, then stops.
	l.slots = []slot{{seg: 2}}
	if err := os.Chmod(logDir, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(logDir, 0o700); err != nil {
			t.Error(err)
		}
	})
	err := l.dropSegmentsLocked()
	if len(l.segs) != 2 {
		t.Fatalf("segments left: %d, want 2", len(l.segs))
	}
	if err == nil {
		t.Fatal("the pass returned nil: the unlink it made was never synced")
	}
}

// TestAnUnreadableStopMarkerRefusesTheStart pins D4's "fail closed before
// any mutation": a stop marker that is there but cannot be read is a marker
// whose reason is unknown, not an absent one, so the start is refused instead
// of going ahead as if nothing had stopped this node.
func TestAnUnreadableStopMarkerRefusesTheStart(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the file's permission bits")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, markerStopPrefix+"LOG_QUARANTINED")
	if err := os.WriteFile(marker, []byte("boot=x\nseg 1\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	l := &FileLog{dir: dir, runDir: t.TempDir()}
	if err := l.checkMarkers(); err == nil {
		t.Fatal("an unreadable stop marker let the node start")
	}
}

// TestASnapshotListingReportsAnUnreadableMeta pins what List says of a
// published snapshot whose metadata cannot be read: an I/O failure, never
// "no snapshot". Inspect and Recover decide from this listing, and an
// operator reading "no snapshot" off a failing disk would act on a lie.
func TestASnapshotListingReportsAnUnreadableMeta(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the file's permission bits")
	}
	s, err := newFileSnapshots(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"x.snap", "x.meta"} {
		if err := os.WriteFile(filepath.Join(s.dir, name), []byte("bytes"), 0o000); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.List(); err == nil {
		t.Fatal("an unreadable .meta was listed as no snapshot")
	}
}
