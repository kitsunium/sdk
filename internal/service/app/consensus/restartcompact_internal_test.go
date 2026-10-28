package consensus

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// After a restart the step function's base is the newest snapshot that
// restored (load: restoreLatest, finishInstall, readTail from base+1), while
// the store still holds the log back to the older retained snapshot less the
// trailing window (afterCut, R6-02). The first cut after the restart asked
// raft to compact through the older retained snapshot, which is now that
// base, and raft's Compact returned on an index at or below its base without
// asking the store for anything. A node restarted between every two of its
// cuts never compacted its log, which grew until the no-space alarm refused
// every write of the cluster (#741).
//
// One voter over a real data directory, every path under t.TempDir() (the
// runtime directory of the STORAGE_FAILED marker included), segments of
// 64 KiB, a reserve of 256 KiB and a headroom of eight segments, entries of
// 4 KiB cut every 16 entries (64 KiB), two trailing entries, and the cut on
// the apply goroutine (SnapshotPausesApply), as forgejo runs it: a cut is
// over before the next entry is applied.

const (
	rcSeg      = 64 << 10
	rcReserve  = 256 << 10
	rcQuota    = rcReserve + 8*rcSeg
	rcTrailing = 2
)

var rcValue = bytes.Repeat([]byte("v"), 4<<10)

type rcNode struct {
	c      *cluster
	dn     *diskNode
	run    string
	closed bool
}

func (s *rcNode) config() Config {
	cfg := diskConfig(s.c, s.dn)
	cfg.MinVoters, cfg.MaxLogBytes, cfg.LogReserveBytes = 1, rcQuota, rcReserve
	cfg.SnapshotEvery, cfg.SnapshotEveryBytes, cfg.SnapshotTrailing = 16, 1<<30, rcTrailing
	cfg.SnapshotPausesApply = true
	return cfg
}

func (s *rcNode) openDir(t *testing.T) {
	t.Helper()
	d, err := NewDataDirWith(s.dn.dir, DataDirOptions{SegmentBytes: rcSeg, RuntimeDir: s.run})
	if err != nil {
		t.Fatalf("open %s: %v", s.dn.dir, err)
	}
	s.dn.dd, s.closed = d, false
}

func (s *rcNode) open(t *testing.T) {
	t.Helper()
	if s.closed {
		s.openDir(t)
	}
	if s.dn.sm == nil {
		s.dn.sm = newKV()
	}
	n, err := NewNode(s.config())
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	s.dn.node = n
	waitFor(t, "the node to lead", func() bool { return n.Status().Role == coreconsensus.RoleLeader })
}

// stop closes the node cleanly, as a rolling upgrade does, and releases its
// directory and transport. Close waits for the apply goroutine, so the cut
// it ran, and the compaction that cut asked for, are done.
func (s *rcNode) stop(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.dn.node.Close(ctx); err != nil {
		t.Errorf("close: %v", err)
	}
	if err := NewMemoryTransport(s.c.net, s.dn.id).Close(); err != nil {
		t.Errorf("close transport: %v", err)
	}
	if err := s.dn.dd.Close(); err != nil {
		t.Errorf("close data directory: %v", err)
	}
	s.dn.node, s.dn.sm, s.closed = nil, nil, true
}

func (s *rcNode) write(k int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := s.dn.node.Propose(ctx, fmt.Appendf(nil, "k%d=%s", k%8, rcValue))
	return err
}

type rcState struct {
	first, last, bytes uint64
	snaps              []uint64
	base               uint64
	alarm              coreconsensus.AlarmID
}

func (s *rcNode) state(t *testing.T) rcState {
	t.Helper()
	var st rcState
	st.first, st.last = s.dn.dd.Log().Bounds()
	st.bytes = uint64(s.dn.dd.d.log.logBytes())
	metas, err := s.dn.dd.Snapshots().List()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range metas {
		st.snaps = append(st.snaps, m.Position.Index)
	}
	if s.dn.node != nil {
		// Raft is the loop's: read on it.
		s.dn.node.exec(func() { st.base = s.dn.node.r.SnapshotIndex() })
		st.alarm = s.dn.node.Status().NoSpace
	}
	return st
}

func (st rcState) String() string {
	return fmt.Sprintf("log [%d, %d], %d KiB, snapshots %v, raft base %d, alarm %d", st.first, st.last, st.bytes>>10, st.snaps, st.base, st.alarm)
}

func rcStart(t *testing.T, cid byte) *rcNode {
	t.Helper()
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{0x5d, 0x10, cid}}
	s := &rcNode{c: c, dn: &diskNode{dir: t.TempDir()}, run: filepath.Join(t.TempDir(), "run")}
	s.openDir(t)
	id, inc, err := Init(s.dn.dd, c.cid, "s0")
	if err != nil {
		t.Fatal(err)
	}
	s.dn.id, s.dn.sm = id, newKV()
	cfg := s.config()
	members := []coreconsensus.MemberValue{{ID: id, Incarnation: inc, Slot: "s0", SPKI: MemorySPKI(id), Voter: true}}
	if err := Bootstrap(context.Background(), &cfg, members); err != nil {
		t.Fatal(err)
	}
	s.open(t)
	t.Cleanup(func() {
		if s.dn.node != nil {
			s.stop(t)
		}
	})
	return s
}

// newest is the index of the newest published snapshot, 0 for none.
func (s *rcNode) newest(t *testing.T) uint64 {
	t.Helper()
	metas, err := s.dn.dd.Snapshots().List()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) == 0 {
		return 0
	}
	return metas[0].Position.Index
}

// rcRounds runs rounds of one cut each: it writes until a new snapshot is
// published, then once more (the cut runs on the apply goroutine, so that
// write is applied after it), and requires the log to start no earlier than
// the older retained snapshot less the trailing window, as afterCut
// compacts it; it restarts the node after each round when restart is set.
func rcRounds(t *testing.T, s *rcNode, rounds int, restart bool) {
	t.Helper()
	k := 0
	for r := range rounds {
		prev := s.newest(t)
		for n := 0; s.newest(t) == prev; n++ {
			if n > 64 {
				t.Fatalf("round %d: no cut after %d writes: %v", r, n, s.state(t))
			}
			if err := s.write(k); err != nil {
				t.Fatalf("round %d: write refused: %v (%v)", r, err, s.state(t))
			}
			k++
		}
		if err := s.write(k); err != nil {
			t.Fatalf("round %d: write refused: %v (%v)", r, err, s.state(t))
		}
		k++
		st := s.state(t)
		if older := st.snaps[len(st.snaps)-1]; st.first+rcTrailing < older {
			t.Fatalf("round %d: the log starts at %d, more than %d entries before the older snapshot %d: %v",
				r, st.first, rcTrailing, older, st)
		}
		if st.alarm != 0 {
			t.Fatalf("round %d: the no-space alarm stands: %v", r, st)
		}
		if restart {
			s.stop(t)
			s.open(t)
		}
	}
}

// TestALogIsCompactedWithoutARestart: six rounds of one cut each (17
// writes of 4 KiB) with no restart; the log follows the older snapshot and
// never reaches the alarm.
func TestALogIsCompactedWithoutARestart(t *testing.T) {
	rcRounds(t, rcStart(t, 1), 6, false)
}

// TestANodeRestartedBetweenItsCutsStillCompactsItsLog: the same writes, the
// node restarted after each round, so one cut per run. Each cut compacts the
// store through the older snapshot less the trailing window, although that
// point is at or below the base the step function restarted on: the log
// stays two cut intervals long. It used to keep every entry from the first
// cut on, a round more at each restart, until the alarm refused every write
// in the seventh round.
func TestANodeRestartedBetweenItsCutsStillCompactsItsLog(t *testing.T) {
	rcRounds(t, rcStart(t, 2), 4, true)
}
