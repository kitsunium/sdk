package consensus

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// heldRetain is a snapshot store whose List, called by the node's retain
// while armed, waits for release: the install is held after its restore and
// its move of raft, at the call that used to record the newest snapshot.
type heldRetain struct {
	coreconsensus.SnapshotStore
	armed   atomic.Bool
	once    sync.Once
	held    chan coreconsensus.SnapshotMetaValue
	release chan struct{}
}

// List implements SnapshotStore, and holds the first call the install's
// retain makes while armed once the store publishes a snapshot. The retain
// of a cut (afterCut, on the apply goroutine) is let through: held, it held
// the restore behind it on that goroutine, and the setup failed (#1554).
func (s *heldRetain) List() ([]coreconsensus.SnapshotMetaValue, error) {
	metas, err := s.SnapshotStore.List()
	if err != nil || len(metas) == 0 || !s.armed.Load() || !calledFrom(".(*Node).retain") || !calledFrom(".(*Node).installed") {
		return metas, err
	}
	first := false
	s.once.Do(func() { first = true })
	if first {
		s.held <- metas[0]
		<-s.release
	}
	return metas, err
}

// calledFrom reports whether a function whose name ends with suffix is on
// the caller's stack.
func calledFrom(suffix string) bool {
	pc := make([]uintptr, 32)
	frames := runtime.CallersFrames(pc[:runtime.Callers(2, pc)])
	for {
		f, more := frames.Next()
		if strings.HasSuffix(f.Function, suffix) {
			return true
		}
		if !more {
			return false
		}
	}
}

// An install publishes its applied position only once the snapshot it came
// from is recorded as the node's newest. The restore moved Applied, then
// raft, and only then retain recorded the snapshot's size: in between, a
// node caught up past its log reported the newest snapshot it held before,
// which Promote, the unfed watch and Health read, and a learner whose limit
// was raised was found caught up with no snapshot over its former limit
// (#1502).
func TestAnInstallRecordsItsNewestSnapshotBeforeItsAppliedPosition(t *testing.T) {
	var lagID atomic.Uint64
	store := &heldRetain{held: make(chan coreconsensus.SnapshotMetaValue, 1), release: make(chan struct{})}
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{6, 7, 7, 5}, every: 20, size: 3}
	c.configure = func(tn *testNode, cfg *Config) {
		if uint64(tn.id) == lagID.Load() {
			store.SnapshotStore = tn.snaps
			cfg.Snapshots = store
		}
	}
	newClusterWith(t, c)
	var released sync.Once
	t.Cleanup(func() { released.Do(func() { close(store.release) }) })
	l := c.settledLeader(5 * time.Second)
	lag := c.otherThan(l)
	var tok coreconsensus.TokenValue
	for i := range 100 {
		tok = proposeOnLeader(t, c, fmt.Sprintf("k%d=%d", i, i))
	}
	c.waitApplied(tok.Index, 10*time.Second)
	held := tok.Index
	// Isolated while stopped: past the live logs' start, only an install
	// can catch it up (see TestAnInstallKeepsTheTwoNewestSnapshots).
	stopBehind(c, lag)
	for i := 100; i < 300; i++ {
		tok = proposeOnLeader(t, c, fmt.Sprintf("k%d=%d", i, i))
	}
	compactedPast(t, c, held+1)
	lagID.Store(uint64(lag.id))
	store.armed.Store(true)
	c.start(lag)
	c.net.Isolate(lag.id, false)
	var meta coreconsensus.SnapshotMetaValue
	select {
	case meta = <-store.held:
	case <-time.After(10 * time.Second):
		t.Fatal("setup: the lagging node never installed a snapshot")
	}
	if meta.Position.Index <= held {
		t.Fatalf("setup: the held snapshot is at %d, not past the %d the node held: no install", meta.Position.Index, held)
	}
	// Held in retain, the install has restored and moved raft: its applied
	// position is published.
	waitFor(t, "the install's applied position published", func() bool {
		return lag.node.Status().Applied >= meta.Position.Index
	})
	if got, want := newestBytes(lag.node), snapshotSize(&meta); got != want {
		t.Errorf("the node reports Applied %d, at or past the snapshot at %d it installed, yet its newest snapshot is %d bytes, not that snapshot's %d",
			lag.node.Status().Applied, meta.Position.Index, got, want)
	}
	released.Do(func() { close(store.release) })
	c.waitApplied(tok.Index, 10*time.Second)
}
