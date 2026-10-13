package consensus

import (
	"context"
	"errors"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// TestAStampOfAnUnknownStateIsRefused pins the stamp's decoding: a state
// byte this version does not define — a corrupt or a newer stamp — makes the
// stamp unreadable. Read as it was, Bootstrap took such a directory for one
// it could found, and stamped it a member.
func TestAStampOfAnUnknownStateIsRefused(t *testing.T) {
	dir := t.TempDir()
	unknown := coreconsensus.StampMember + 1
	if err := writeStamp(dir, coreconsensus.StampValue{ID: 1, State: unknown}); err != nil {
		t.Fatal(err)
	}
	if _, err := readStamp(dir); !errors.Is(err, coreconsensus.DataDirMismatch) {
		t.Fatalf("a stamp in state %d was read: %v", unknown, err)
	}
}

// TestBootstrapRefusesAStampOfAnUnknownState pins the same rule where a
// LogStore other than the shipped one hands back the stamp: Bootstrap founds
// only a directory whose state it knows.
func TestBootstrapRefusesAStampOfAnUnknownState(t *testing.T) {
	log := NewMemoryLog()
	cid := coreconsensus.ClusterID{3}
	id, inc, err := InitLog(log, cid, "s0")
	if err != nil {
		t.Fatal(err)
	}
	st, err := log.Stamp()
	if err != nil {
		t.Fatal(err)
	}
	st.State = coreconsensus.StampMember + 1
	if err := log.WriteStamp(st); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Cluster: cid, Machine: newKV(), Durability: DurabilityVolatile, Log: log, Snapshots: NewMemorySnapshots(),
		Transport: NewMemoryTransport(NewMemoryNetwork(), id), InitialTiming: testTimingFast(), MaxSnapshotBytes: 1 << 30,
		SnapshotEveryBytes: 1 << 20, MaxLogBytes: 1 << 30, LogReserveBytes: 1 << 20, MinVoters: 1,
	}
	members := []coreconsensus.MemberValue{{ID: id, Incarnation: inc, Slot: "s0", SPKI: MemorySPKI(id), Voter: true}}
	if err := Bootstrap(context.Background(), cfg, members); !errors.Is(err, coreconsensus.DataDirMismatch) {
		t.Fatalf("Bootstrap over an unknown stamp state: %v", err)
	}
}
