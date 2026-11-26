package consensus

import (
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// A follower sent every byte of a snapshot whose install never ends — a
// stuck disk under its spool's sync, a stuck Restore, an apply goroutine
// held — kept the leader's stream waiting for its verdict, and the stream's
// pin, which had no end while it streamed, held the leader's compaction at
// the snapshot for good: the log grew to the NO_SPACE alarm (#602). The pin
// now ends pinAfterStream maximum election timeouts after the last byte
// was read, and the stream is cancelled with it.
func TestAFollowerStuckInItsInstallDoesNotPinCompaction(t *testing.T) {
	stuckInstall(t, false)
}

// The leader's log, once the stuck follower's pin ended, is compacted as
// far as its retention goes, and no farther: back to the older of its two
// newest snapshots, less SnapshotTrailing (afterCut, R6-02). The test used
// to require at most 200 entries left, which counted on a cut every
// SnapshotEvery entries. A cut whose write outlasted the writes coalesced
// their triggers into one cut at the end (maybeSnapshot), the two newest
// snapshots stood 225 entries apart, and the log, compacted as far as that
// retention let it with no pin held, failed the count on macOS: log
// [275,503], no pin (#1444). The leader's cut is held here across the
// writes, and the log must still reach that retention, past the stuck pin.
func TestAStuckInstallsPinEndsWhileTheLeadersCutIsSlow(t *testing.T) {
	stuckInstall(t, true)
}

// stuckInstall runs a follower stuck in the restore of the snapshot it is
// streamed while its leader writes 400 entries, the leader's snapshot
// writes held across those writes when slowCut is set, and requires the
// leader's log compacted to its retention, past the point the stuck
// stream's pin held it at, within the deadline.
func stuckInstall(t *testing.T, slowCut bool) {
	holds := map[*testNode]*cutHold{}
	c := newClusterWith(t, &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 2, 3}, every: 20, size: 3,
		configure: func(tn *testNode, cfg *Config) {
			h := &cutHold{kv: tn.sm}
			holds[tn] = h
			cfg.Machine = h
		},
	})
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	c.net.Isolate(f.id, true)
	for i := range 100 {
		propose(t, l.node, fmt.Sprintf("a%03d=v", i))
	}
	waitFor(t, "the leader compacted past the isolated follower", func() bool {
		first, _ := l.log.Bounds()
		_, fl := f.log.Bounds()
		return first > fl+1
	})
	// The follower's machine hangs: its apply goroutine stops at the
	// restore of the snapshot it is sent.
	f.sm.mu.Lock()
	released := false
	release := func() {
		if !released {
			released = true
			f.sm.mu.Unlock()
		}
	}
	defer release()
	c.net.Isolate(f.id, false)
	waitFor(t, "a snapshot streamed to the follower", func() bool {
		streaming := false
		l.node.exec(func() {
			pin, ok := l.node.sending[f.id]
			streaming = ok && pin.until.IsZero() && pin.sent != nil && pin.sent.Load() != 0
		})
		return streaming
	})
	firstBefore, _ := l.log.Bounds()
	hold := holds[l]
	if slowCut {
		hold.arm()
	}
	defer hold.release()
	for i := range 400 {
		propose(t, l.node, fmt.Sprintf("b%03d=v", i))
	}
	hold.release()
	deadline := time.Now().Add(10 * time.Second)
	for {
		first, last := l.log.Bounds()
		floor, metas := retentionFloor(t, l)
		if first > firstBefore && first > floor {
			break
		}
		if time.Now().After(deadline) {
			l.node.exec(func() { t.Logf("leader pins: %+v", l.node.sending) })
			t.Fatalf("one follower stuck in its install's restore pins the leader's compaction with no bound: log [%d,%d], first was %d before 400 more writes, the retention compacts through %d (snapshots at %v)",
				first, last, firstBefore, floor, metas)
		}
		time.Sleep(10 * time.Millisecond)
	}
	release()
	c.waitApplied(l.node.Status().Applied, 10*time.Second)
}

// retentionFloor is the index tn's retention compacts its log through when
// no pin holds it, as afterCut computes it: the older of its two newest
// snapshots, or its applied index when lower, less the trailing entries
// kept; and the snapshot indexes it read, newest first.
func retentionFloor(t *testing.T, tn *testNode) (uint64, []uint64) {
	t.Helper()
	metas, err := tn.snaps.List()
	if err != nil {
		t.Fatal(err)
	}
	idx := make([]uint64, 0, len(metas))
	for i := range metas {
		idx = append(idx, metas[i].Position.Index)
	}
	if len(metas) == 0 {
		return 0, idx
	}
	tn.node.am.Lock()
	applied := tn.node.applied
	tn.node.am.Unlock()
	through, trailing := min(metas[min(1, len(metas)-1)].Position.Index, applied), tn.node.trailingKept()
	if through <= trailing {
		return 0, idx
	}
	return through - trailing, idx
}

// cutHold is a kv whose snapshots, once armed, write nothing until it is
// released: the capture is taken on the apply goroutine as always, and the
// cut's write goroutine waits, as one held by a slow disk or a busy
// scheduler does.
type cutHold struct {
	*kv
	mu   sync.Mutex
	gate chan struct{}
}

// Snapshot captures the state, its write gated by the hold armed now.
func (h *cutHold) Snapshot() (coreconsensus.Snapshot, error) {
	s, err := h.kv.Snapshot()
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return heldSnap{Snapshot: s, gate: h.gate}, nil
}

// arm holds the writes of the snapshots captured from now on.
func (h *cutHold) arm() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.gate = make(chan struct{})
}

// release lets every held write go, and holds no later one; once is
// enough, and again is nothing.
func (h *cutHold) release() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.gate != nil {
		close(h.gate)
		h.gate = nil
	}
}

// heldSnap is a snapshot whose write waits for its gate, when it has one.
type heldSnap struct {
	coreconsensus.Snapshot
	gate chan struct{}
}

// WriteTo waits for the gate, then writes the snapshot.
func (s heldSnap) WriteTo(w io.Writer) (int64, error) {
	if s.gate != nil {
		<-s.gate
	}
	return s.Snapshot.WriteTo(w)
}

// A receiver offered again the snapshot it is restoring — the leader's pin
// on it fell due and the leader probed again — opened a spool over the
// published id, was streamed the whole snapshot a second time, committed it
// over the file its restore reads, and restored it once more (#610). It
// refuses that offer OVERLOADED now.
func TestAnOfferOfTheSnapshotBeingRestoredIsRefused(t *testing.T) {
	c := newCluster(t, 3, 20)
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	c.net.Isolate(f.id, true)
	for i := range 100 {
		propose(t, l.node, fmt.Sprintf("a%03d=v", i))
	}
	waitFor(t, "the leader compacted past the isolated follower", func() bool {
		first, _ := l.log.Bounds()
		_, fl := f.log.Bounds()
		return first > fl+1
	})
	f.sm.mu.Lock()
	released := false
	release := func() {
		if !released {
			released = true
			f.sm.mu.Unlock()
		}
	}
	defer release()
	c.net.Isolate(f.id, false)
	var id string
	// The install counts as restoring from before its Commit (#623): the
	// snapshot is published as well once its restore runs.
	waitFor(t, "the follower restoring a snapshot it committed", func() bool {
		f.node.snapState.mu.Lock()
		for k := range f.node.snapState.restoring {
			id = k
		}
		f.node.snapState.mu.Unlock()
		if id == "" {
			return false
		}
		h, err := f.snaps.Open(id)
		if err != nil {
			return false
		}
		_ = h.Close()
		return true
	})
	h, err := f.snaps.Open(id)
	if err != nil {
		t.Fatal(err)
	}
	meta := h.Meta()
	_ = h.Close()
	acc := f.node.offer(coreconsensus.AuthenticatedPeerValue{ID: l.id},
		coreconsensus.InstallOfferValue{Term: l.node.Status().Term, SnapshotID: id, Meta: meta})
	if acc.Refused != coreconsensus.CodeOverloaded {
		t.Errorf("an offer of the snapshot the follower is restoring was answered %+v, not refused OVERLOADED", acc)
	}
	release()
	c.waitApplied(l.node.Status().Applied, 10*time.Second)
}
