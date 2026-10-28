package consensus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/fs/flock"
)

// The byte trigger of D7 (#515) counts the payload applied since the last
// cut in snapState.sinceBytes, a field of the running process that started
// at 0 on every start. A volatile machine replays the log from the base at
// every start, and the replay counts those bytes again. A
// DurableStateMachine does not: raft starts at its applied index, and the
// entries between the base and that index are never handed to apply again.
// A durable node restarted before each run applied SnapshotEveryBytes never
// cut on bytes; only the count trigger (SnapshotEvery, 16 384 by default)
// was left, and with entries large enough the log reached MaxLogBytes less
// LogReserveBytes first: the no-space alarm refused every write of the
// cluster (#744).
//
// Durable voters over real data directories, every path under t.TempDir()
// (the runtime directory of the STORAGE_FAILED marker included): segments
// of 64 KiB, a reserve of 256 KiB and a headroom of eight segments, entries
// of 4 KiB, SnapshotEveryBytes 64 KiB (16 entries), the default
// SnapshotEvery of 16 384, and a clean stop (Close, as a rolling upgrade
// does) after every 12 writes, 48 KiB applied per run.

const (
	dbSeg     = 64 << 10
	dbReserve = 256 << 10
	dbQuota   = dbReserve + 8*dbSeg
	dbEvery   = 64 << 10
	dbPerRun  = 12
	dbRuns    = 4
)

var dbValue = bytes.Repeat([]byte("v"), 4<<10)

type dbNode struct {
	net  *MemoryNetwork
	cid  coreconsensus.ClusterID
	dir  string
	run  string
	dd   DataDir
	id   coreconsensus.NodeID
	dsm  *durableKV // survives the restarts, as a machine over its own store does
	node *Node
	min  int
}

func (s *dbNode) config() Config {
	return Config{
		Cluster: s.cid, Machine: s.dsm, Durability: DurabilityDurable, Log: s.dd.Log(), Snapshots: s.dd.Snapshots(),
		Transport: NewMemoryTransport(s.net, s.id), InitialTiming: testTimingDisk(), MaxSnapshotBytes: 1 << 30,
		SnapshotEveryBytes: dbEvery, SnapshotEvery: 16384, SnapshotTrailing: 2,
		MaxLogBytes: dbQuota, LogReserveBytes: dbReserve, MinVoters: s.min, SnapshotPausesApply: true,
	}
}

func (s *dbNode) openDir(t *testing.T) {
	t.Helper()
	d, err := NewDataDirWith(s.dir, DataDirOptions{SegmentBytes: dbSeg, RuntimeDir: s.run})
	//: no flock(2), no data directory (illumos, Solaris): the refusal is
	//: asserted, then the case skips, as openDir's do.
	if !flock.Native {
		if !errors.Is(err, coreproc.UnsupportedPlatform) {
			t.Fatalf("NewDataDirWith on %s = %v, want UNSUPPORTED_PLATFORM", runtime.GOOS, err)
		}
		t.Skipf("%s has no flock(2), so a data directory is refused there (ADR 0144)", runtime.GOOS)
	}
	if err != nil {
		t.Fatalf("open %s: %v", s.dir, err)
	}
	s.dd = d
}

// start opens the directory and starts the node on it.
func (s *dbNode) start(t *testing.T) {
	t.Helper()
	s.openDir(t)
	n, err := NewNode(s.config())
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	s.node = n
}

// stop closes the node cleanly and releases its directory and transport.
// Close waits for the apply goroutine, so a cut it ran is done.
func (s *dbNode) stop(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.node.Close(ctx); err != nil {
		t.Errorf("close: %v", err)
	}
	if err := NewMemoryTransport(s.net, s.id).Close(); err != nil {
		t.Errorf("close transport: %v", err)
	}
	if err := s.dd.Close(); err != nil {
		t.Errorf("close data directory: %v", err)
	}
	s.node = nil
}

func (s *dbNode) write(k int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := s.node.Propose(ctx, fmt.Appendf(nil, "k%d=%s", k%8, dbValue))
	return err
}

func (s *dbNode) state(t *testing.T) string {
	t.Helper()
	first, last := s.dd.Log().Bounds()
	metas, err := s.dd.Snapshots().List()
	if err != nil {
		t.Fatal(err)
	}
	var snaps []uint64
	for _, m := range metas {
		snaps = append(snaps, m.Position.Index)
	}
	st := ""
	if s.node != nil {
		ns := s.node.Status()
		st = fmt.Sprintf(", applied %d, alarm %d", ns.Applied, ns.NoSpace)
	}
	return fmt.Sprintf("log [%d, %d], %d KiB, snapshots %v%s", first, last, s.dd.d.log.logBytes()>>10, snaps, st)
}

func (s *dbNode) cuts(t *testing.T) int {
	t.Helper()
	metas, err := s.dd.Snapshots().List()
	if err != nil {
		t.Fatal(err)
	}
	return len(metas)
}

// dbStart initialises and bootstraps n durable voters on one network.
func dbStart(t *testing.T, cid byte, n int) []*dbNode {
	t.Helper()
	net := NewMemoryNetwork()
	c := coreconsensus.ClusterID{0x5d, 0x11, cid}
	run := filepath.Join(t.TempDir(), "run")
	var ns []*dbNode
	var members []coreconsensus.MemberValue
	for i := range n {
		s := &dbNode{net: net, cid: c, dir: t.TempDir(), run: run, dsm: &durableKV{kv: newKV()}, min: n}
		s.openDir(t)
		slot := fmt.Sprintf("q%d", i)
		id, inc, err := Init(s.dd, c, slot)
		if err != nil {
			t.Fatal(err)
		}
		s.id = id
		ns = append(ns, s)
		members = append(members, coreconsensus.MemberValue{ID: id, Incarnation: inc, Slot: slot, SPKI: MemorySPKI(id), Voter: true})
	}
	for _, s := range ns {
		cfg := s.config()
		if err := Bootstrap(context.Background(), &cfg, members); err != nil {
			t.Fatal(err)
		}
		if err := s.dd.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range ns {
		s.start(t)
	}
	t.Cleanup(func() {
		for _, s := range ns {
			if s.node != nil {
				s.stop(t)
			}
		}
	})
	return ns
}

func dbLeader(t *testing.T, ns []*dbNode) *dbNode {
	t.Helper()
	var l *dbNode
	waitFor(t, "a leader", func() bool {
		for _, s := range ns {
			if s.node != nil && s.node.Status().Role == coreconsensus.RoleLeader {
				l = s
				return true
			}
		}
		return false
	})
	return l
}

// TestADurableNodeRestartedBeforeEachByteCutStillCuts: one durable voter,
// restarted after every 12 writes (48 KiB, under the 64 KiB trigger). The
// bytes its start resumes past without applying them again still count
// towards the trigger: it cuts on bytes, in its second run. It used to cut
// never, and the alarm refused every write in its ninth.
func TestADurableNodeRestartedBeforeEachByteCutStillCuts(t *testing.T) {
	s := dbStart(t, 1, 1)[0]
	dbLeader(t, []*dbNode{s})
	k := 0
	for r := range dbRuns {
		for range dbPerRun {
			if err := s.write(k); err != nil {
				t.Fatalf("run %d: write refused after %d KiB applied: %v (%s)", r, k*len(dbValue)>>10, err, s.state(t))
			}
			k++
		}
		s.stop(t)
		s.start(t)
		dbLeader(t, []*dbNode{s})
	}
	if s.cuts(t) == 0 {
		t.Fatalf("restarted every %d writes, the durable node never cut on bytes: %s", dbPerRun, s.state(t))
	}
}

// TestOneDurableVoterRestartedOnThatRhythmKeepsTheClusterWriting: three
// durable voters; the leader and one follower run on, and only the third
// voter is restarted cleanly after every 12 writes. It cuts on bytes as the
// other two do, and the cluster never refuses a write NO_SPACE.
func TestOneDurableVoterRestartedOnThatRhythmKeepsTheClusterWriting(t *testing.T) {
	ns := dbStart(t, 2, 3)
	l := dbLeader(t, ns)
	var f *dbNode
	for _, s := range ns {
		if s != l {
			f = s
			break
		}
	}
	k := 0
	for r := range dbRuns {
		for range dbPerRun {
			if err := l.write(k); err != nil {
				t.Fatalf("run %d: the leader refused a write: %v; leader %s; restarted follower %s", r, err, l.state(t), f.state(t))
			}
			k++
		}
		applied := l.node.Status().Applied
		waitFor(t, "the follower to apply", func() bool { return f.node.Status().Applied >= applied })
		f.stop(t)
		f.start(t)
		waitFor(t, "the follower to catch up", func() bool { return f.node.Status().Applied >= applied })
	}
	if f.cuts(t) == 0 {
		t.Fatalf("the durable voter restarted every %d writes never cut on bytes: %s", dbPerRun, f.state(t))
	}
}

// onceFailingSnapKV fails the write of its first cut, as a full disk under
// the spool does, then cuts as kv does.
type onceFailingSnapKV struct {
	*kv
	failed *atomic.Bool
}

func (s onceFailingSnapKV) Snapshot() (coreconsensus.Snapshot, error) {
	if s.failed.CompareAndSwap(false, true) {
		return failingSnap{}, nil
	}
	return s.kv.Snapshot()
}

// TestACutThatPublishesNothingGivesBackTheBytesItTook: a cut on bytes whose
// write fails publishes nothing. The payload it covered is still applied
// since the last cut: once its wait is over (cutRetryMin), the next applied
// entry cuts. The cut used to zero the count as it started, and the next
// one waited for a whole SnapshotEveryBytes more (#744).
func TestACutThatPublishesNothingGivesBackTheBytesItTook(t *testing.T) {
	failed := new(atomic.Bool)
	c := newClusterWith(t, &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{0x5d, 0x13, 1}, every: 16384, size: 1,
		configure: func(tn *testNode, cfg *Config) {
			cfg.Machine = onceFailingSnapKV{kv: tn.sm, failed: failed}
			cfg.SnapshotPausesApply = true
			cfg.SnapshotEveryBytes = 16 << 10
		},
	})
	tn := c.nodes[0]
	c.leader(5 * time.Second)
	pad := strings.Repeat("v", 1<<10)
	for i := 0; !failed.Load(); i++ {
		if i > 64 {
			t.Fatal("no cut was tried after 64 KiB applied")
		}
		propose(t, tn.node, fmt.Sprintf("k%d=%s", i%8, pad))
	}
	time.Sleep(cutRetryMin + 100*time.Millisecond)
	propose(t, tn.node, "after=1")
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		metas, err := tn.snaps.List()
		if err != nil {
			t.Fatal(err)
		}
		if len(metas) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the cut after a failed one waits for another SnapshotEveryBytes: no snapshot once its wait was over")
		}
	}
}
