package consensus

import (
	"context"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// syncFailingSnapshots is a snapshot store whose every Commit fails as a
// failed fsync does: the spool was written, and the disk would not make it
// durable.
type syncFailingSnapshots struct{ *MemorySnapshots }

func (s syncFailingSnapshots) Create(id string, meta coreconsensus.SnapshotMeta) (coreconsensus.SnapshotSink, int64, error) {
	sink, off, err := s.MemorySnapshots.Create(id, meta)
	if err != nil {
		return nil, 0, err
	}
	return syncFailingSink{sink}, off, nil
}

// syncFailingSink is a spool whose Commit reports STORAGE_FAILED.
type syncFailingSink struct{ coreconsensus.SnapshotSink }

func (syncFailingSink) Commit(coreconsensus.SnapshotMeta) error {
	return storageFailed("sync snapshot", errImpossible)
}

// TestACutWhoseSyncFailsStopsTheNode pins D4 on the snapshot path: a failed
// fsync is never retried, and that holds for a snapshot's spool as for the
// log, since both live on the one disk whose page cache may now lie. The cut
// runs aside, or on the apply goroutine when the machine cannot cut cheaply,
// and either way the node stops with STORAGE_FAILED instead of carrying on
// and cutting again.
func TestACutWhoseSyncFailsStopsTheNode(t *testing.T) {
	for _, pauses := range []bool{false, true} {
		t.Run(map[bool]string{false: "aside", true: "on the apply goroutine"}[pauses], func(t *testing.T) {
			cid := coreconsensus.ClusterID{4, 5, 6}
			log, net := NewMemoryLog(), NewMemoryNetwork()
			id, inc, err := InitLog(log, cid, "slot-0")
			if err != nil {
				t.Fatal(err)
			}
			members := []coreconsensus.Member{{ID: id, Incarnation: inc, Slot: "slot-0", SPKI: MemorySPKI(id), Voter: true}}
			cfg := Config{
				Cluster: cid, Machine: newKV(), Durability: DurabilityVolatile, Log: log,
				Snapshots: syncFailingSnapshots{NewMemorySnapshots()}, Transport: net.Transport(id),
				InitialTiming: testTimingFast(), MaxSnapshotBytes: 1 << 30, SnapshotEveryBytes: 1 << 20,
				MaxLogBytes: 1 << 30, LogReserveBytes: 1 << 20, MinVoters: 1, SnapshotEvery: 2,
				SnapshotTrailing: 1, JoinAuthorizer: allowJoin{}, SnapshotPausesApply: pauses,
			}
			if err := Bootstrap(context.Background(), cfg, members); err != nil {
				t.Fatal(err)
			}
			n, err := NewRaft(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := n.HardClose(); err != nil {
					t.Error(err)
				}
			})
			deadline := time.Now().Add(10 * time.Second)
			for i := 0; ; i++ {
				if r, ok := n.StopReason(); ok {
					if !errs.HasCode(r.Err, coreconsensus.CodeStorageFailed) {
						t.Fatalf("stopped on %v, want STORAGE_FAILED", r.Err)
					}
					return
				}
				if time.Now().After(deadline) {
					t.Fatal("the node kept running after a snapshot's sync failed")
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				_, _, perr := n.Propose(ctx, []byte{'k', '=', byte('a' + i%26)})
				cancel()
				if perr != nil {
					time.Sleep(5 * time.Millisecond)
				}
			}
		})
	}
}
