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

// TestACommitWhoseSeekFailsIsAStorageFailure pins what a failing disk looks
// like at the end of a snapshot spool: the size could not be read, which is
// an I/O failure (STORAGE_FAILED), never a snapshot of the wrong size
// (SNAPSHOT_CORRUPT) — the two send an operator down opposite paths.
func TestACommitWhoseSeekFailsIsAStorageFailure(t *testing.T) {
	s, err := newFileSnapshots(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sink, _, err := s.Create("x", coreconsensus.SnapshotMetaValue{ID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	fs, ok := sink.(*fileSink)
	if !ok {
		t.Fatalf("sink %T", sink)
	}
	// A descriptor the kernel no longer serves: every Seek on it fails.
	if err := fs.f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sink.Commit(coreconsensus.SnapshotMetaValue{}); !errors.Is(err, coreconsensus.StorageFailed) {
		t.Fatalf("Commit over a failed Seek: %v, want STORAGE_FAILED", err)
	}
}

// TestAnUnreadableSilenceConditionIsReported pins the silence condition's
// safe side and its voice: a condition file that cannot be read keeps the
// node silent past the end of its log, as before, and now says so, so an
// operator learns the file is damaged instead of finding a node that never
// votes.
func TestAnUnreadableSilenceConditionIsReported(t *testing.T) {
	dir := t.TempDir()
	d := openDir(t, dir, 1<<16)
	cid := coreconsensus.ClusterID{11}
	id, inc, err := Init(d, cid, "s0")
	if err != nil {
		t.Fatal(err)
	}
	lg := &fakeLogger{}
	cfg := Config{
		Cluster: cid, Machine: newKV(), Durability: DurabilityVolatile, Log: d.Log(), Snapshots: d.Snapshots(),
		Transport: NewMemoryTransport(NewMemoryNetwork(), id), InitialTiming: testTimingFast(), MaxSnapshotBytes: 1 << 30,
		SnapshotEveryBytes: 1 << 20, MaxLogBytes: 1 << 30, LogReserveBytes: 1 << 20, MinVoters: 1, Logger: lg,
	}
	members := []coreconsensus.MemberValue{{ID: id, Incarnation: inc, Slot: "s0", SPKI: MemorySPKI(id), Voter: true}}
	if err := Bootstrap(context.Background(), &cfg, members); err != nil {
		t.Fatal(err)
	}
	// A condition of the wrong length: written by nothing this version
	// writes, so unreadable.
	if err := os.WriteFile(filepath.Join(dir, fileSilent), []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	n, err := NewNode(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.HardClose(); err != nil {
		t.Error(err)
	}
	if err := d.Close(); err != nil {
		t.Error(err)
	}
	lg.mu.Lock()
	defer lg.mu.Unlock()
	for _, m := range lg.msgs {
		if strings.Contains(m, "silence condition") && strings.Contains(m, "WARN") {
			return
		}
	}
	t.Fatalf("no warning names the unreadable silence condition: %q", lg.msgs)
}
