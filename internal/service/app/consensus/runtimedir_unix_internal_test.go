//go:build !windows

package consensus

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// A data directory whose boot-scoped marker directory cannot be created is
// refused at open, naming the path, before anything is written: every
// non-root deployment failed to create /run/kitsunium-consensus at every
// stop and nothing said so until a device-wide failure left no marker at
// all (R9-05, #562).
func TestAnUnwritableRuntimeDirIsRefusedAtOpen(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory's permission bits")
	}
	parent := filepath.Join(t.TempDir(), "run")
	if err := os.Mkdir(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	_, err := NewDataDirWith(dir, DataDirOptions{RuntimeDir: filepath.Join(parent, "kitsunium-consensus")})
	if !errors.Is(err, coreconsensus.ConsensusMisconfigured) {
		t.Fatalf("open with an unwritable runtime directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "log")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the refused open wrote the log directory")
	}
}

// With a writable boot-scoped directory, a STORAGE_FAILED stop whose data
// directory marker is refused still refuses a same-boot restart; and a stop
// that wrote no marker at all refuses any later open of the directory in
// the same process, for a consumer that does not exit on it (#562).
func TestAStorageStopRefusesTheSameBootRestart(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory's permission bits")
	}
	for _, both := range []bool{false, true} {
		dir := t.TempDir()
		run := filepath.Join(t.TempDir(), "run")
		d, err := NewDataDirWith(dir, DataDirOptions{SegmentBytes: 1 << 16, RuntimeDir: run})
		if err != nil {
			t.Fatal(err)
		}
		cid := coreconsensus.ClusterID{9, 6, 2}
		id, inc, err := Init(d, cid, "s0")
		if err != nil {
			t.Fatal(err)
		}
		cfg := Config{
			Cluster: cid, Machine: newKV(), Durability: DurabilityVolatile, Log: d.Log(), Snapshots: d.Snapshots(),
			Transport: NewMemoryTransport(NewMemoryNetwork(), id), InitialTiming: testTimingFast(), MaxSnapshotBytes: 1 << 30,
			SnapshotEveryBytes: 1 << 20, MaxLogBytes: 1 << 30, LogReserveBytes: 1 << 20, MinVoters: 1,
		}
		members := []coreconsensus.MemberValue{{ID: id, Incarnation: inc, Slot: "s0", SPKI: MemorySPKI(id), Voter: true}}
		if err := Bootstrap(context.Background(), &cfg, members); err != nil {
			t.Fatal(err)
		}
		n, err := NewNode(cfg)
		if err != nil {
			t.Fatal(err)
		}
		// The data directory refuses the marker, as a device-wide failure
		// does; with both, the boot-scoped place refuses it too.
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		if both {
			if err := os.Chmod(run, 0o500); err != nil {
				t.Fatal(err)
			}
		}
		n.stop(storageFailed("fdatasync", errImpossible), true)
		r, _ := n.StopReason()
		_ = n.HardClose()
		_ = d.Close()
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(run, 0o700); err != nil {
			t.Fatal(err)
		}
		if r.Unmarked != both {
			t.Fatalf("both=%v: unmarked %v", both, r.Unmarked)
		}
		d2, err := NewDataDirWith(dir, DataDirOptions{SegmentBytes: 1 << 16, RuntimeDir: run})
		if both {
			if !errors.Is(err, coreconsensus.StopMarkerPresent) {
				t.Fatalf("an open after an unmarked stop in this process: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := d2.d.log.checkMarkers(); !errors.Is(err, coreconsensus.StopMarkerPresent) {
			t.Fatalf("a same-boot restart after a STORAGE_FAILED stop: %v", err)
		}
		_ = d2.Close()
	}
}
