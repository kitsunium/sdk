package consensus

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// secondReadCut is a snapshot store whose handle, opened by the first
// restore once armed, serves its verification in full and, with cut set,
// its second read one chunk short: a .snap cut on a chunk boundary from
// outside between the two reads. The install's two ends are watched, as in
// closeFailing: the Remove of a failed restore, held until release, and the
// List of the retain after a restore raft moved onto.
type secondReadCut struct {
	coreconsensus.SnapshotStore
	cut      bool
	armed    atomic.Bool
	once     sync.Once
	opened   chan string
	removed  chan string
	retained chan struct{}
	release  chan struct{}
}

// cutHandle is a handle whose reads outside verifySnapshot end at the start
// of its last chunk when cut is set.
type cutHandle struct {
	coreconsensus.SnapshotHandle
	cut  bool
	last int64
}

// lastChunkAt is the offset of the last chunk of the encoded stream h
// holds, walked from the chunk headers.
func lastChunkAt(h coreconsensus.SnapshotHandle) (int64, error) {
	size := h.Meta().EncodedSize
	var off int64
	var hdr [chunkHeaderLen]byte
	//: each chunk header names the next chunk's offset.
	for {
		//: a header that cannot be read leaves no chunk to cut.
		if _, err := h.ReadAt(hdr[:], off); err != nil {
			return 0, err
		}
		next := off + chunkHeaderLen + int64(binary.LittleEndian.Uint32(hdr[0:]))
		//: the chunk that reaches the end is the last.
		if next >= size {
			return off, nil
		}
		off = next
	}
}

// ReadAt implements io.ReaderAt: in full for the verification, short by
// the last chunk for any other read when cut is set.
func (h cutHandle) ReadAt(p []byte, off int64) (int, error) {
	//: the verification, and an intact second read, see every byte.
	if !h.cut || calledFrom(".verifySnapshot") {
		return h.SnapshotHandle.ReadAt(p, off)
	}
	//: past the cut, the file has ended.
	if off >= h.last {
		return 0, io.EOF
	}
	//: a read across the cut stops at it.
	if off+int64(len(p)) > h.last {
		n, err := h.SnapshotHandle.ReadAt(p[:h.last-off], off)
		//: a read that failed before the cut keeps its own error.
		if err != nil {
			return n, err
		}
		return n, io.EOF
	}
	return h.SnapshotHandle.ReadAt(p, off)
}

// Open implements SnapshotStore, handing the first restore opened while
// armed the cut handle.
func (s *secondReadCut) Open(id string) (coreconsensus.SnapshotHandle, error) {
	h, err := s.SnapshotStore.Open(id)
	//: only a restore's handle, opened once armed, is replaced.
	if err != nil || !s.armed.Load() || !calledFrom(".(*Node).restoreFrom") {
		return h, err
	}
	first := false
	s.once.Do(func() { first = true })
	//: the restores after the first read the snapshot whole.
	if !first {
		return h, err
	}
	last, lerr := lastChunkAt(h)
	//: a snapshot whose chunks cannot be walked is not cut.
	if lerr != nil {
		abandon(h.Close())
		return nil, lerr
	}
	s.opened <- id
	return cutHandle{SnapshotHandle: h, cut: s.cut, last: last}, nil
}

// Remove implements SnapshotStore, holding the first Remove of a failed
// restore until release.
func (s *secondReadCut) Remove(id string) error {
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
func (s *secondReadCut) List() ([]coreconsensus.SnapshotMetaValue, error) {
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

// restoreSeen is what the first restore once armed did to the machine: the
// keys it held before and after, and the error the machine returned.
type restoreSeen struct {
	before, after int
	err           error
}

// restoreWatch is the lagging node's machine: a kv whose first Restore once
// armed is reported on seen and, with skim set, reads nothing and keeps its
// state, as a machine that stops before the end of its stream.
type restoreWatch struct {
	*kv
	skim  bool
	armed *atomic.Bool
	once  sync.Once
	seen  chan restoreSeen
}

// Restore implements StateMachine, reporting the first restore once armed.
func (w *restoreWatch) Restore(r io.Reader, meta coreconsensus.SnapshotMetaValue) error {
	keys := func() int {
		w.mu.Lock()
		defer w.mu.Unlock()
		return len(w.m)
	}
	before := keys()
	first := false
	//: only the first restore once armed is the one the store cut.
	if w.armed.Load() {
		w.once.Do(func() { first = true })
	}
	var err error
	//: a skimming machine reads none of the stream it is handed.
	if !first || !w.skim {
		err = w.kv.Restore(r, meta)
	}
	//: the first restore once armed is reported.
	if first {
		w.seen <- restoreSeen{before: before, after: keys(), err: err}
	}
	return err
}

// snapshotIndex is the index of snapshot id in s, 0 when s holds none.
func snapshotIndex(t *testing.T, s coreconsensus.SnapshotStore, id string) uint64 {
	t.Helper()
	metas, err := s.List()
	//: a store that cannot list leaves nothing to judge.
	if err != nil {
		t.Fatal(err)
	}
	//: the snapshot named id gives its index.
	for _, m := range metas {
		//: only id is read.
		if m.ID == id {
			return m.Position.Index
		}
	}
	return 0
}

// A restore's second read of a snapshot is held to the sizes and the
// SHA-256 its first read verified. It was checked per chunk only: a .snap
// cut on a chunk boundary between the two reads ended cleanly, the machine
// was restored from that prefix, and applied moved to the snapshot's index
// (#1562). Refused SNAPSHOT_CORRUPT, the machine keeps its state, the copy
// is removed, nothing moves, and the next install restores the whole
// state; a machine that stops reading before the end is refused too, the
// rest read after it. An intact second read installs.
func TestARestoreWhoseSecondReadIsCutIsRefused(t *testing.T) {
	type tc struct {
		name string
		cut  bool
		skim bool
	}
	runCase := func(t *testing.T, tcase tc) {
		var lagID atomic.Uint64
		store := &secondReadCut{
			cut:      tcase.cut,
			opened:   make(chan string, 1),
			removed:  make(chan string, 1),
			retained: make(chan struct{}, 1),
			release:  make(chan struct{}),
		}
		seen := make(chan restoreSeen, 1)
		c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 5, 6, 2}, every: 20, size: 3}
		c.configure = func(tn *testNode, cfg *Config) {
			//: only the lagging node reads the doubles.
			if uint64(tn.id) == lagID.Load() {
				store.SnapshotStore = tn.snaps
				cfg.Snapshots = store
				cfg.Machine = &restoreWatch{kv: tn.sm, skim: tcase.skim, armed: &store.armed, seen: seen}
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
		// Armed once started: the start's own restore reads its snapshot
		// whole.
		store.armed.Store(true)
		c.net.Isolate(lag.id, false)
		var id string
		select {
		//: the install's restore opened the cut handle.
		case id = <-store.opened:
		//: no install reached the lagging node.
		case <-time.After(10 * time.Second):
			t.Fatal("setup: the lagging node never restored an install")
		}
		var got restoreSeen
		select {
		//: the machine read the second read.
		case got = <-seen:
		//: the restore never reached the machine.
		case <-time.After(10 * time.Second):
			t.Fatalf("the restore of %s never reached the machine", id)
		}
		select {
		//: the restore failed and its copy is being removed.
		case gone := <-store.removed:
			st := lag.node.Status()
			index := snapshotIndex(t, store.SnapshotStore, gone)
			//: an intact second read is never refused.
			if !tcase.cut {
				t.Errorf("the restore of %s read it whole, yet it was refused (machine error %v): %s removed, Applied %d", id, got.err, gone, st.Applied)
			}
			//: the refusal is the snapshot's own fault, the machine kept its state, and applied did not move.
			if tcase.cut && ((!tcase.skim && !errs.HasCode(got.err, coreconsensus.CodeSnapshotCorrupt)) || got.after != got.before || gone != id || st.Applied >= index) {
				t.Errorf("the second read of %s was cut by a chunk: the machine read %v (want SNAPSHOT_CORRUPT) and holds %d keys against %d before, %s removed with Applied %d against the snapshot's %d",
					id, got.err, got.after, got.before, gone, st.Applied, index)
			}
		//: the install reached installed, raft moved.
		case <-store.retained:
			st := lag.node.Status()
			//: a second read cut by a chunk is never installed.
			if tcase.cut {
				t.Errorf("the second read of %s was cut by a chunk, yet it was installed: the machine read %v and holds %d keys (%d before), Applied %d",
					id, got.err, got.after, got.before, st.Applied)
			}
			//: an intact second read restores the machine in full.
			if !tcase.cut && (got.err != nil || got.after <= got.before) {
				t.Errorf("the intact restore of %s left the machine %d keys against %d before, error %v", id, got.after, got.before, got.err)
			}
		//: the install ended neither way.
		case <-time.After(10 * time.Second):
			t.Fatal("the install neither installed nor was removed")
		}
		released.Do(func() { close(store.release) })
		c.waitApplied(tok.Index, 10*time.Second)
		//: whatever the first install did, the node ends on the whole state.
		if keys, want := machineKeys(lag), machineKeys(l); keys != want {
			t.Errorf("caught up to %d, the lagging node's machine holds %d keys, the leader's %d", tok.Index, keys, want)
		}
	}
	//: each case runs its own cluster.
	for _, tcase := range []tc{
		{name: "a second read cut by a chunk is refused", cut: true},
		{name: "a cut stream a machine stopped reading is refused", cut: true, skim: true},
		{name: "an intact second read installs"},
	} {
		t.Run(tcase.name, func(t *testing.T) { runCase(t, tcase) })
	}
}
