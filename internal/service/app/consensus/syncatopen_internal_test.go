package consensus

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// errWriteBack stands for the EIO a failed write-back reports to the next
// descriptor that syncs the file.
var errWriteBack = errors.New("input/output error")

// failSyncAtOpen makes one sync at open of what match names fail, the one
// after skip others of it, as a write-back that failed is reported to the
// first descriptor that syncs: the next one, in the same boot, syncs clean
// (errseq, Linux 4.13). Files go to the file sync, directories to the
// directory sync; match sees the file's name or the directory's path.
func failSyncAtOpen(t *testing.T, match func(path string) bool, skip int) {
	t.Helper()
	seen := 0
	fail := func(p string) bool {
		if !match(p) {
			return false
		}
		seen++
		return seen == skip+1
	}
	syncFileAtOpen = func(f *os.File) error {
		if fail(f.Name()) {
			return errWriteBack
		}
		return datasync(f)
	}
	syncDirAtOpen = func(dir string) error {
		if fail(dir) {
			return errWriteBack
		}
		return syncDir(dir)
	}
	t.Cleanup(func() { syncFileAtOpen, syncDirAtOpen = datasync, syncDir })
}

// A sync that fails at open writes the STORAGE_FAILED markers before the
// open returns, as a running node's stop does: the kernel reports a failed
// write-back once, to the first descriptor that syncs, and this open
// consumed it. The next open in the same boot synced clean, and the node
// acknowledged and voted on pages the disk had refused (#637, #414). It is
// refused STOP_MARKER_PRESENT now, whichever sync failed — a segment's, the
// log directory's, the data directory's, the snapshot directory's, or the
// segment's again once a torn tail was zeroed — and after a reboot the
// directory opens and serves its log again.
func TestASyncThatFailsAtOpenRefusesTheSameBootRestart(t *testing.T) {
	seg := func(string) func(string) bool {
		return func(p string) bool { return filepath.Base(p) == segName(1) }
	}
	cases := []struct {
		name  string
		match func(dir string) func(path string) bool
		// torn leaves a torn batch at the tail, which the open zeroes.
		torn bool
		skip int
	}{
		{name: "a segment", match: seg},
		{name: "the zeroed torn tail", match: seg, torn: true, skip: 1},
		{name: "the log directory", match: func(dir string) func(string) bool {
			return func(p string) bool { return p == filepath.Join(dir, "log") }
		}},
		{name: "the data directory", match: func(dir string) func(string) bool {
			return func(p string) bool { return p == dir }
		}},
		{name: "the snapshot directory", match: func(dir string) func(string) bool {
			return func(p string) bool { return p == filepath.Join(dir, "snap") }
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, d := base(t)
			if tc.torn {
				tail(t, d, rec(recEntry, d.d.log.batch, 4, false), 0)
			}
			if err := d.Close(); err != nil {
				t.Fatal(err)
			}
			failSyncAtOpen(t, tc.match(dir), tc.skip)
			if _, err := NewDataDirWith(dir, DataDirOptions{SegmentBytes: 1 << 16, RuntimeDir: testRuntimeDir()}); !errors.Is(err, coreconsensus.StorageFailed) {
				t.Fatalf("an open whose sync of %s fails: %v", tc.name, err)
			}
			// The same boot: the sync now passes over what the disk refused.
			d2 := openDir(t, dir, 1<<16)
			err := d2.d.log.checkMarkers()
			if cerr := d2.Close(); cerr != nil {
				t.Fatal(cerr)
			}
			if !errors.Is(err, coreconsensus.StopMarkerPresent) {
				t.Fatalf("a same-boot restart after a failed sync of %s at open: %v", tc.name, err)
			}
			reboot(t, dir)
			d3 := openDir(t, dir, 1<<16)
			defer d3.Close()
			if err := d3.d.log.checkMarkers(); err != nil {
				t.Fatalf("start after the reboot: %v %v", err, errs.FieldsOf(err))
			}
			if f, la := d3.Log().Bounds(); f != 1 || la != 3 {
				t.Fatalf("bounds %d %d after the reboot, want 1 3", f, la)
			}
		})
	}
}

// An open whose recovery fails closes the segment descriptors it opened: it
// dropped them, and a caller that retried in the same process leaked one per
// segment at every try (#637).
func TestAFailedRecoveryClosesTheSegmentsItOpened(t *testing.T) {
	dir, d := base(t)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	var opened []*os.File
	syncFileAtOpen = func(f *os.File) error {
		opened = append(opened, f)
		return errWriteBack
	}
	t.Cleanup(func() { syncFileAtOpen = datasync })
	if _, err := NewDataDirWith(dir, DataDirOptions{SegmentBytes: 1 << 16, RuntimeDir: testRuntimeDir()}); !errors.Is(err, coreconsensus.StorageFailed) {
		t.Fatalf("an open whose sync fails: %v", err)
	}
	if len(opened) == 0 {
		t.Fatal("setup: no segment reached the sync at open")
	}
	for _, f := range opened {
		// A read goes through the descriptor's own reference count, which
		// answers ErrClosed on every platform; Stat on Windows asks the
		// handle directly and only says it is invalid.
		if _, err := f.ReadAt(make([]byte, 1), 0); !errors.Is(err, os.ErrClosed) {
			t.Errorf("segment %s still open after the failed open (read: %v)", filepath.Base(f.Name()), err)
		}
	}
	forgetBoot(t, dir)
}

// forgetBoot removes the boot-scoped marker of dir, if any, so the shared
// test runtime directory does not keep it.
func forgetBoot(t *testing.T, dir string) {
	t.Helper()
	l := &fileLog{dir: dir, runDir: testRuntimeDir()}
	if err := os.Remove(l.runtimeMarker("STORAGE_FAILED")); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

// A directory that cannot be opened to be synced at open — a descriptor
// table full — writes no marker: no sync ran, so no write-back error was
// consumed, and a marker would refuse every start until the reboot (#637).
func TestADirectoryThatCannotBeOpenedToSyncWritesNoMarker(t *testing.T) {
	dir, d := base(t)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	syncDirAtOpen = func(p string) error {
		if p == filepath.Join(dir, "log") {
			return &fs.PathError{Op: "open", Path: p, Err: errDescriptors}
		}
		return syncDir(p)
	}
	t.Cleanup(func() { syncDirAtOpen = syncDir })
	if _, err := NewDataDirWith(dir, DataDirOptions{SegmentBytes: 1 << 16, RuntimeDir: testRuntimeDir()}); !errors.Is(err, coreconsensus.StorageFailed) {
		t.Fatalf("an open that cannot open its log directory to sync it: %v", err)
	}
	syncDirAtOpen = syncDir
	if ms, err := stopMarkers(dir); err != nil || len(ms) != 0 {
		t.Fatalf("markers after a directory that could not be opened: %v %v", ms, err)
	}
	d2 := openDir(t, dir, 1<<16)
	defer d2.Close()
	if err := d2.d.log.checkMarkers(); err != nil {
		t.Fatalf("a start once the directory opens again: %v", err)
	}
}

// errDescriptors stands for EMFILE, a descriptor table full.
var errDescriptors = errors.New("too many open files")
