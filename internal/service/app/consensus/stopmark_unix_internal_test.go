//go:build !windows

package consensus

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// TestAStorageStopSaysWhichMarkerItCouldNotWrite pins R9-05's accepted
// degradation: a STORAGE_FAILED stop writes its marker in two places, and a
// node that could write only one of them still stops — but says so, since
// the place it could not write is the one that would have held it through a
// lost data directory.
func TestAStorageStopSaysWhichMarkerItCouldNotWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory's permission bits")
	}
	dir := t.TempDir()
	d := openDir(t, dir, 1<<16)
	cid := coreconsensus.ClusterID{9}
	id, inc, err := Init(d, cid, "s0")
	if err != nil {
		t.Fatal(err)
	}
	// The boot-scoped place is made unwritable: a directory this user may
	// read but not write into.
	run := filepath.Join(t.TempDir(), "run")
	if err := os.Mkdir(run, 0o500); err != nil {
		t.Fatal(err)
	}
	d.d.log.runDir = run
	lg := &fakeLogger{}
	cfg := Config{
		Cluster: cid, Machine: newKV(), Durability: DurabilityVolatile, Log: d.Log(), Snapshots: d.Snapshots(),
		Transport: NewMemoryTransport(NewMemoryNetwork(), id), InitialTiming: testTimingFast(), MaxSnapshotBytes: 1 << 30,
		SnapshotEveryBytes: 1 << 20, MaxLogBytes: 1 << 30, LogReserveBytes: 1 << 20, MinVoters: 1, Logger: lg,
	}
	members := []coreconsensus.MemberValue{{ID: id, Incarnation: inc, Slot: "s0", SPKI: MemorySPKI(id), Voter: true}}
	if err := Bootstrap(context.Background(), cfg, members); err != nil {
		t.Fatal(err)
	}
	n, err := NewNode(cfg)
	if err != nil {
		t.Fatal(err)
	}
	n.stop(storageFailed("fdatasync", errImpossible), true)
	// A node that stopped itself answers HardClose with why it stopped.
	if err := n.HardClose(); err != nil && !errors.Is(err, coreconsensus.StorageFailed) {
		t.Error(err)
	}
	if err := d.Close(); err != nil {
		t.Error(err)
	}
	r, ok := n.StopReason()
	if !ok || r.Unmarked {
		t.Fatalf("stop reason %+v: the data directory's marker was written", r)
	}
	lg.mu.Lock()
	defer lg.mu.Unlock()
	for _, m := range lg.msgs {
		if strings.Contains(m, "marker") {
			return
		}
	}
	t.Fatalf("no line says the boot-scoped marker is missing: %q", lg.msgs)
}
