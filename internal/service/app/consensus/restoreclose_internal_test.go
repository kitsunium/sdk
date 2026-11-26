package consensus

import (
	"fmt"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// closeFailing is a snapshot store whose handle, opened by a restore while
// armed, is read in full and then fails to close, once. The install's two
// ends are watched: the Remove of a failed restore, held until release, and
// the List of the retain that follows a restore raft moved onto.
type closeFailing struct {
	coreconsensus.SnapshotStore
	armed    atomic.Bool
	once     sync.Once
	failed   chan string
	removed  chan string
	retained chan struct{}
	release  chan struct{}
}

// failingClose is a handle whose Close releases the store's pin, then
// reports a failure.
type failingClose struct {
	coreconsensus.SnapshotHandle
}

// Close implements SnapshotHandle.
func (h failingClose) Close() error {
	abandon(h.SnapshotHandle.Close())
	return storageFailed("close snapshot", os.ErrClosed)
}

// Open implements SnapshotStore, handing the first restore opened while
// armed a handle whose Close fails.
func (s *closeFailing) Open(id string) (coreconsensus.SnapshotHandle, error) {
	h, err := s.SnapshotStore.Open(id)
	if err != nil || !s.armed.Load() || !calledFrom(".(*Node).restoreFrom") {
		return h, err
	}
	first := false
	s.once.Do(func() { first = true })
	if !first {
		return h, err
	}
	s.failed <- id
	return failingClose{h}, nil
}

// Remove implements SnapshotStore, holding the Remove of a failed restore.
func (s *closeFailing) Remove(id string) error {
	if s.armed.Load() && calledFrom(".(*Node).restoreFailed") {
		s.removed <- id
		<-s.release
	}
	return s.SnapshotStore.Remove(id)
}

// List implements SnapshotStore, and signals the retain of an install.
func (s *closeFailing) List() ([]coreconsensus.SnapshotMetaValue, error) {
	metas, err := s.SnapshotStore.List()
	if s.armed.Load() && calledFrom(".(*Node).installed") {
		select {
		case s.retained <- struct{}{}:
		default:
		}
	}
	return metas, err
}

// A restore that read its snapshot in full and restored the machine does
// not fail on its handle's Close alone. It did: the install removed the copy
// and never moved raft, while the machine and Applied stood at it, and
// since #1502 the newest snapshot size described the copy removed (#1554).
func TestARestoreWhoseHandleFailsToCloseStillInstalls(t *testing.T) {
	var lagID atomic.Uint64
	store := &closeFailing{
		failed:   make(chan string, 1),
		removed:  make(chan string, 1),
		retained: make(chan struct{}, 1),
		release:  make(chan struct{}),
	}
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 5, 5, 4}, every: 20, size: 3}
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
	c.net.Isolate(lag.id, true)
	c.kill(lag)
	for i := 100; i < 300; i++ {
		tok = proposeOnLeader(t, c, fmt.Sprintf("k%d=%d", i, i))
	}
	compactedPast(t, c, held+1)
	lagID.Store(uint64(lag.id))
	c.start(lag)
	// Armed once started: the start's own restore reads a handle that
	// closes.
	store.armed.Store(true)
	c.net.Isolate(lag.id, false)
	var id string
	select {
	case id = <-store.failed:
	case <-time.After(10 * time.Second):
		t.Fatal("setup: the lagging node never restored an install")
	}
	select {
	case gone := <-store.removed:
		st := lag.node.Status()
		metas, err := store.SnapshotStore.List()
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(metas))
		for _, m := range metas {
			ids = append(ids, m.ID)
		}
		lag.sm.mu.Lock()
		keys := len(lag.sm.m)
		lag.sm.mu.Unlock()
		t.Errorf("the restore of %s read it in full and restored the machine (it holds %d keys), Applied %d, newest snapshot %d bytes, yet its handle's Close failed the install: %s removed (store %v after it), raft not moved (Commit %d)",
			id, keys, st.Applied, newestBytes(lag.node), gone, slices.DeleteFunc(ids, func(s string) bool { return s == gone }), st.Commit)
	case <-store.retained:
		metas, err := store.SnapshotStore.List()
		if err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(metas, func(m coreconsensus.SnapshotMetaValue) bool { return m.ID == id }) {
			t.Errorf("the install of %s was retained, yet the store holds %v", id, metas)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the install whose Close failed neither installed nor was removed")
	}
	released.Do(func() { close(store.release) })
	c.waitApplied(tok.Index, 10*time.Second)
}
