//go:build !windows

package consensus

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// A sync that fails at open on a device that refuses both markers, the data
// directory's and the boot-scoped one, refuses any later open of the
// directory in the process, as a running node's unmarked stop does (#562,
// #637): nothing on disk says the page cache lies.
func TestAnUnmarkedSyncFailureAtOpenRefusesAnyLaterOpenInTheProcess(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory's permission bits")
	}
	dir, d := base(t)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	run := filepath.Join(t.TempDir(), "run")
	opts := DataDirOptions{SegmentBytes: 1 << 16, RuntimeDir: run}
	// The device fails the sync and, from then on, every write: both
	// places refuse their marker.
	syncFileAtOpen = func(*os.File) error {
		for _, p := range []string{dir, run} {
			if err := os.Chmod(p, 0o500); err != nil {
				t.Error(err)
			}
		}
		return errWriteBack
	}
	t.Cleanup(func() { syncFileAtOpen = datasync })
	_, err := NewDataDirWith(dir, opts)
	for _, p := range []string{dir, run} {
		if cerr := os.Chmod(p, 0o700); cerr != nil {
			t.Fatal(cerr)
		}
	}
	if !errors.Is(err, coreconsensus.StorageFailed) {
		t.Fatalf("an open whose sync fails: %v", err)
	}
	syncFileAtOpen = datasync
	if _, err := NewDataDirWith(dir, opts); !errors.Is(err, coreconsensus.StopMarkerPresent) {
		t.Fatalf("an open after an unmarked sync failure at open in this process: %v", err)
	}
}

// A read that fails at open writes no marker: it consumes no write-back
// error, and a marker would refuse every start for the rest of the boot
// once the cause, here a permission, is gone. Only a failed sync does
// (#637).
func TestAReadThatFailsAtOpenWritesNoMarker(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the file's permission bits")
	}
	dir, d := base(t)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	seg := filepath.Join(dir, "log", segName(1))
	if err := os.Chmod(seg, 0o000); err != nil {
		t.Fatal(err)
	}
	_, err := NewDataDirWith(dir, DataDirOptions{SegmentBytes: 1 << 16, RuntimeDir: testRuntimeDir()})
	if cerr := os.Chmod(seg, 0o600); cerr != nil {
		t.Fatal(cerr)
	}
	if !errors.Is(err, coreconsensus.StorageFailed) {
		t.Fatalf("an open that cannot read a segment: %v", err)
	}
	if ms, err := stopMarkers(dir); err != nil || len(ms) != 0 {
		t.Fatalf("markers after a failed read at open: %v %v", ms, err)
	}
	d2 := openDir(t, dir, 1<<16)
	defer d2.Close()
	if err := d2.d.log.checkMarkers(); err != nil {
		t.Fatalf("a start once the segment reads again: %v", err)
	}
}
