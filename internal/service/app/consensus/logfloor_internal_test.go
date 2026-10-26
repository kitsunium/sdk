package consensus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// floorNode starts one voter over a real data directory on segments of seg
// bytes, every path under t.TempDir(), the runtime directory of the
// STORAGE_FAILED marker included, with the quota and reserve given, a cut
// every everyBytes of payload and the trailing window given (0 is the
// default, 4 096). It returns the node, or nil and NewNode's refusal.
func floorNode(t *testing.T, cid byte, seg, quota, reserve, everyBytes int64, trailing uint64) (*Node, *diskNode, error) {
	t.Helper()
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{0x69, 0x60, cid}}
	dn := &diskNode{dir: t.TempDir()}
	d, err := NewDataDirWith(dn.dir, DataDirOptions{SegmentBytes: seg, RuntimeDir: filepath.Join(t.TempDir(), "run")})
	if err != nil {
		t.Fatalf("NewDataDirWith: %v", err)
	}
	dn.dd = d
	id, inc, err := Init(dn.dd, c.cid, "s0")
	if err != nil {
		t.Fatal(err)
	}
	dn.id, dn.sm = id, newKV()
	cfg := diskConfig(c, dn)
	cfg.MinVoters, cfg.MaxLogBytes, cfg.LogReserveBytes = 1, quota, reserve
	cfg.SnapshotEvery, cfg.SnapshotEveryBytes, cfg.SnapshotTrailing = 1<<30, everyBytes, trailing
	members := []coreconsensus.MemberValue{{ID: id, Incarnation: inc, Slot: "s0", SPKI: MemorySPKI(id), Voter: true}}
	if err := Bootstrap(context.Background(), new(cfg), members); err != nil {
		t.Fatal(err)
	}
	n, err := NewNode(cfg)
	if err != nil {
		_ = dn.dd.Close()
		return nil, dn, err
	}
	dn.node = n
	t.Cleanup(func() { dn.crash(t, c) })
	waitFor(t, "the node to lead", func() bool { return n.Status().Role == coreconsensus.RoleLeader })
	return n, dn, nil
}

// floorWrite proposes data once, under a bound that a single voter meets.
func floorWrite(n *Node, data []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := n.Propose(ctx, data)
	return err
}

// floorRepair is what an operator does online about a committed no-space
// alarm (D17), twice: cut a snapshot now, which compacts the log behind it,
// and clear the alarm. It then retries a write for up to two seconds and
// returns its last refusal, nil once one went through.
func floorRepair(t *testing.T, n *Node) error {
	t.Helper()
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if _, err := n.SaveSnapshot(ctx, "op:test", io.Discard); err != nil {
			t.Logf("SaveSnapshot: %v", err)
		}
		if alarm := n.Status().NoSpace; alarm != 0 {
			if err := n.ClearNoSpace(ctx, "op:test", coreconsensus.PreconditionSpec{Log: n.lineage(), Alarm: alarm}); err != nil {
				t.Logf("ClearNoSpace: %v", err)
			}
		}
		cancel()
	}
	var last error
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if last = floorWrite(n, []byte("after=1")); last == nil {
			return nil
		}
	}
	return last
}

// The log's own floor is two segments: the active one, preallocated
// whole, and at each rotation the one before it, which no compaction drops
// before a cut passes its last entry. Config refused only a LogReserveBytes
// not below MaxLogBytes: a headroom of one segment was filled at the open,
// and the node refused every write for good before its first; one of two
// segments was filled at every rotation (#696). The shipped log reports its
// segment size; a store that does not is not judged.
func TestAHeadroomOfTwoSegmentsOrLessIsRefused(t *testing.T) {
	const seg, reserve = 64 << 10, 256 << 10
	cases := []struct {
		name     string
		headroom int64
		refused  bool
	}{
		{"one segment", seg, true},
		{"two segments", 2 * seg, true},
		{"two segments and a byte", 2*seg + 1, false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, dn, err := floorNode(t, byte(i), seg, reserve+tc.headroom, reserve, 64<<10, 0)
			switch {
			case tc.refused && !errors.Is(err, coreconsensus.ConsensusMisconfigured):
				t.Fatalf("MaxLogBytes %d over LogReserveBytes %d with segments of %d: NewNode = %v, want CONSENSUS_MISCONFIGURED; the log occupies %d bytes at start",
					reserve+tc.headroom, reserve, seg, err, dn.dd.d.log.logBytes())
			case !tc.refused && err != nil:
				t.Fatalf("a headroom of two segments and a byte: NewNode = %v", err)
			}
		})
	}
	t.Run("a store with no segments", func(t *testing.T) {
		c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{0x69, 0x60, 0x0f}}
		tn := &testNode{id: 1, log: NewMemoryLog(), snaps: NewMemorySnapshots(), sm: newKV()}
		cfg := c.config(tn)
		cfg.MaxLogBytes, cfg.LogReserveBytes = 2, 1
		if _, err := withDefaults(&cfg); err != nil {
			t.Fatalf("a quota over a store that reports no segment: %v", err)
		}
	})
}

// SnapshotTrailing counts entries, whatever their size, and compaction kept
// them behind the older retained snapshot (afterCut): with entries of a
// quarter of the default MaxEntryBytes, the default 4 096 weigh 1 GiB, more
// than the 768 MiB of headroom the quota ADR 0152 recommends leaves over the
// default reserve. Nothing was compacted before the older snapshot passed
// index 4 096, the log reached the no-space alarm first, the alarm refused
// every command, and with no command no snapshot index moved: SaveSnapshot
// compacted nothing either, and every write was refused for good (#696).
// The trailing window now yields once the log is half way to the alarm.
//
// Scaled down: segments of 64 KiB, a headroom of eight over a reserve of
// 256 KiB, entries of 4 KiB cut every 64 KiB of payload, the default
// SnapshotTrailing, whose window is 16 MiB, 32 times the headroom; the
// state is kept at eight keys so that a cut is quick.
func TestTheTrailingWindowYieldsToTheLogQuota(t *testing.T) {
	const seg, reserve = 64 << 10, 256 << 10
	n, dn, err := floorNode(t, 0x10, seg, reserve+8*seg, reserve, 64<<10, 0)
	if err != nil {
		t.Fatal(err)
	}
	value := bytes.Repeat([]byte("v"), 4<<10)
	for k := range 384 {
		err := floorWrite(n, fmt.Appendf(nil, "k%d=%s", k%8, value))
		if err == nil {
			continue
		}
		first, _ := dn.dd.Log().Bounds()
		st := n.Status()
		t.Logf("write %d refused: %v; alarm %d, applied %d, log from %d, %d bytes", k, err, st.NoSpace, st.Applied, first, dn.dd.d.log.logBytes())
		rerr := floorRepair(t, n)
		first, _ = dn.dd.Log().Bounds()
		t.Fatalf("MaxLogBytes %d over LogReserveBytes %d (accepted): the node took %d writes of 4 KiB, then refused one; after the operator's repair a write: %v (log from %d, %d bytes)",
			reserve+8*seg, reserve, k, rerr, first, dn.dd.d.log.logBytes())
	}
}

// D17's repair of a committed no-space alarm — cut now (SaveSnapshot),
// which compacts behind it, and clear the alarm — freed nothing while the
// trailing window reached back past the log's start: the compaction point
// is the older retained snapshot less SnapshotTrailing entries, and with
// the alarm refusing the commands that would move them, no snapshot index
// ever passed it (#696). A short log keeps no trailing window.
//
// Nothing cuts here on its own (SnapshotEvery and SnapshotEveryBytes past
// the writes), so the log reaches the alarm: segments of 64 KiB, a
// headroom of four, entries of 4 KiB, the default SnapshotTrailing.
func TestTheRepairOfAShortLogCompactsPastTheTrailingWindow(t *testing.T) {
	const seg, reserve = 64 << 10, 256 << 10
	n, dn, err := floorNode(t, 0x20, seg, reserve+4*seg, reserve, 1<<30, 0)
	if err != nil {
		t.Fatal(err)
	}
	value := bytes.Repeat([]byte("v"), 4<<10)
	var refused error
	for k := 0; k < 128 && refused == nil; k++ {
		if err := floorWrite(n, fmt.Appendf(nil, "k%d=%s", k%8, value)); err != nil {
			if !errors.Is(err, coreconsensus.NoSpace) {
				t.Fatalf("write %d: %v", k, err)
			}
			refused = err
		}
	}
	if refused == nil {
		t.Fatal("setup: 512 KiB written over a headroom of 256 KiB and no write was refused NO_SPACE")
	}
	waitFor(t, "the no-space alarm", func() bool { return n.Status().NoSpace != 0 })
	if err := floorRepair(t, n); err != nil {
		first, _ := dn.dd.Log().Bounds()
		st := n.Status()
		t.Fatalf("after the operator's repair, twice, a write is still refused: %v (alarm %d, applied %d, log from %d, %d bytes)",
			err, st.NoSpace, st.Applied, first, dn.dd.d.log.logBytes())
	}
	if b := dn.dd.d.log.logBytes(); b+reserve >= reserve+4*seg {
		t.Fatalf("the repair left the log at %d bytes, short of a headroom of %d", b, 4*seg)
	}
}
