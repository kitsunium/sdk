package consensus

import (
	"fmt"
	"io"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// closeFailing is a snapshot store whose handle, opened by a restore while
// armed, fails to close, once, and with readFails fails every read as well.
// The install's two ends are watched: the Remove of a failed restore, held
// until release, and the List of the retain that follows a restore raft
// moved onto.
type closeFailing struct {
	coreconsensus.SnapshotStore
	readFails bool
	armed     atomic.Bool
	once      sync.Once
	failed    chan string
	removed   chan string
	retained  chan struct{}
	release   chan struct{}
}

// failingClose is a handle whose Close releases the store's pin, then
// reports a failure, and whose reads fail when readFails is set.
type failingClose struct {
	coreconsensus.SnapshotHandle
	readFails bool
}

// ReadAt implements io.ReaderAt, failing when the handle's reads do.
func (h failingClose) ReadAt(p []byte, off int64) (int, error) {
	//: a failing read is the cause the restore must still return.
	if h.readFails {
		return 0, io.ErrUnexpectedEOF
	}
	return h.SnapshotHandle.ReadAt(p, off)
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
	//: only a restore's handle, opened once armed, is replaced.
	if err != nil || !s.armed.Load() || !calledFrom(".(*Node).restoreFrom") {
		return h, err
	}
	first := false
	s.once.Do(func() { first = true })
	//: the restores after the first read a handle that closes.
	if !first {
		return h, err
	}
	s.failed <- id
	return failingClose{SnapshotHandle: h, readFails: s.readFails}, nil
}

// Remove implements SnapshotStore, holding the first Remove of a failed
// restore until release.
func (s *closeFailing) Remove(id string) error {
	//: only the Remove of a failed restore is watched.
	if s.armed.Load() && calledFrom(".(*Node).restoreFailed") {
		select {
		//: the first is handed to the test and held until release.
		case s.removed <- id:
			<-s.release
		//: a later one goes through: the test reads one.
		default:
		}
	}
	return s.SnapshotStore.Remove(id)
}

// List implements SnapshotStore, and signals the retain of an install.
func (s *closeFailing) List() ([]coreconsensus.SnapshotMetaValue, error) {
	metas, err := s.SnapshotStore.List()
	//: only the List of an install's retain is signalled.
	if s.armed.Load() && calledFrom(".(*Node).installed") {
		select {
		//: the first signal is kept for the test.
		case s.retained <- struct{}{}:
		//: a later one is dropped: the test reads one.
		default:
		}
	}
	return metas, err
}

// heldStore is what the store holds while a failed restore's Remove of id
// is held: the ids it keeps once id goes, and id's index, 0 when absent.
func heldStore(t *testing.T, store *closeFailing, id string) (kept []string, index uint64) {
	t.Helper()
	metas, err := store.SnapshotStore.List()
	//: a store that cannot list leaves nothing to judge.
	if err != nil {
		t.Fatal(err)
	}
	//: every snapshot but id is kept, and id's index read.
	for _, m := range metas {
		//: id is the one the Remove takes.
		if m.ID == id {
			index = m.Position.Index
			continue
		}
		kept = append(kept, m.ID)
	}
	return kept, index
}

// machineKeys is the number of keys tn's machine holds.
func machineKeys(tn *testNode) int {
	tn.sm.mu.Lock()
	defer tn.sm.mu.Unlock()
	return len(tn.sm.m)
}

// A restore that read its snapshot in full and restored the machine does
// not fail on its handle's Close alone. It did: the install removed the copy
// and never moved raft, while the machine and Applied stood at it, and
// since #1502 the newest snapshot size described the copy removed (#1554).
// A restore whose read failed is still refused, its copy removed, whatever
// its Close says.
func TestARestoreWhoseHandleFailsToCloseStillInstalls(t *testing.T) {
	type tc struct {
		name      string
		readFails bool
	}
	runCase := func(t *testing.T, tcase tc) {
		var lagID atomic.Uint64
		store := &closeFailing{
			readFails: tcase.readFails,
			failed:    make(chan string, 1),
			removed:   make(chan string, 1),
			retained:  make(chan struct{}, 1),
			release:   make(chan struct{}),
		}
		c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 5, 5, 4}, every: 20, size: 3}
		c.configure = func(tn *testNode, cfg *Config) {
			//: only the lagging node reads the double.
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
		//: a first state the lagging node holds before it stops.
		for i := range 100 {
			tok = proposeOnLeader(t, c, fmt.Sprintf("k%d=%d", i, i))
		}
		c.waitApplied(tok.Index, 10*time.Second)
		held := tok.Index
		// Isolated while stopped: past the live logs' start, only an install
		// can catch it up (see TestAnInstallKeepsTheTwoNewestSnapshots).
		c.net.Isolate(lag.id, true)
		c.kill(lag)
		//: the state the install carries, past the logs the others keep.
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
		//: the install's restore opened the failing handle.
		case id = <-store.failed:
		//: no install reached the lagging node.
		case <-time.After(10 * time.Second):
			t.Fatal("setup: the lagging node never restored an install")
		}
		select {
		//: the restore failed and its copy is being removed.
		case gone := <-store.removed:
			st := lag.node.Status()
			kept, index := heldStore(t, store, gone)
			//: a full read and a restore failed on the Close alone.
			if !tcase.readFails {
				t.Errorf("the restore of %s read it in full and restored the machine (it holds %d keys), Applied %d, newest snapshot %d bytes, yet its handle's Close failed the install: %s removed (store %v after it), raft not moved (Commit %d)",
					id, machineKeys(lag), st.Applied, newestBytes(lag.node), gone, kept, st.Commit)
			}
			//: a failed read removes the copy it read, and moves nothing.
			if tcase.readFails && (gone != id || st.Applied >= index) {
				t.Errorf("the restore of %s failed its read, yet %s was removed with Applied %d against the snapshot's %d", id, gone, st.Applied, index)
			}
		//: the install reached installed, raft moved.
		case <-store.retained:
			metas, err := store.SnapshotStore.List()
			//: a store that cannot list leaves nothing to judge.
			if err != nil {
				t.Fatal(err)
			}
			has := slices.ContainsFunc(metas, func(m coreconsensus.SnapshotMetaValue) bool { return m.ID == id })
			//: a failed read is never installed.
			if tcase.readFails {
				t.Errorf("the restore of %s failed its read, yet it was installed (store %v)", id, metas)
			}
			//: an installed copy stays in the store.
			if !tcase.readFails && !has {
				t.Errorf("the install of %s was retained, yet the store holds %v", id, metas)
			}
		//: the install ended neither way.
		case <-time.After(10 * time.Second):
			t.Fatal("the install whose Close failed neither installed nor was removed")
		}
		released.Do(func() { close(store.release) })
		c.waitApplied(tok.Index, 10*time.Second)
	}
	//: each case runs its own cluster.
	for _, tcase := range []tc{
		{name: "a full read installs"},
		{name: "a failed read is refused", readFails: true},
	} {
		t.Run(tcase.name, func(t *testing.T) { runCase(t, tcase) })
	}
}
