package consensus

import (
	"bytes"
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

// secondReadCut is a snapshot store whose handle, opened by each of the
// first cuts restores once armed, serves its verification in full and, with
// cut set, its second read one chunk short: a .snap cut on a chunk boundary
// from outside between the two reads. The install's two ends are watched,
// as in closeFailing: the Remove of a failed restore, held until release,
// and the List of the retain after a restore raft moved onto.
type secondReadCut struct {
	coreconsensus.SnapshotStore
	cut      bool
	armed    atomic.Bool
	cuts     atomic.Int32
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

// Open implements SnapshotStore, handing the first cuts restores opened
// while armed the cut handle.
func (s *secondReadCut) Open(id string) (coreconsensus.SnapshotHandle, error) {
	h, err := s.SnapshotStore.Open(id)
	//: only a restore's handle, opened once armed, is replaced.
	if err != nil || !s.armed.Load() || !calledFrom(".(*Node).restoreFrom") {
		return h, err
	}
	//: the restores past the first cuts read the snapshot whole.
	if s.cuts.Add(-1) < 0 {
		return h, err
	}
	last, lerr := lastChunkAt(h)
	//: a snapshot whose chunks cannot be walked is not cut.
	if lerr != nil {
		abandon(h.Close())
		return nil, lerr
	}
	//: only the first cut handle is announced: the test reads one.
	select {
	//: the first is handed to the test.
	case s.opened <- id:
	//: a later one, the start's after an install, goes unannounced.
	default:
	}
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

// restoreProbe is what the lagging node's machines share across its
// restarts: the first restore once armed is reported on seen once, and the
// entries applied at or below the snapshot it accepted are counted.
type restoreProbe struct {
	armed *atomic.Bool
	once  sync.Once
	seen  chan restoreSeen
	// node, when set, is handed the entries (A, match] right after the
	// first restore once armed accepted its stream: committed before the
	// snapshot's index, and handed after the restore, as when the core
	// raised the commit after it sent the restore.
	node atomic.Pointer[Node]
	// restored is the index of the snapshot the first restore once armed
	// accepted, 0 before.
	restored atomic.Uint64
	// twice counts the entries applied at or below restored after it: a
	// second application over the snapshot's state.
	twice atomic.Int64
}

// restoreWatch is the lagging node's machine: a kv restored through inner,
// the kv itself or its framed form, whose first Restore once armed is
// reported on the probe and, with skim set, reads nothing and keeps its
// state, as a machine that stops before the end of its stream.
type restoreWatch struct {
	*kv
	inner coreconsensus.StateMachine
	skim  bool
	p     *restoreProbe
}

// Apply implements StateMachine, counting an entry the restored snapshot
// already holds.
func (w *restoreWatch) Apply(e coreconsensus.EntryValue) any {
	//: an entry at or below the accepted snapshot is applied over its state a second time.
	if idx := w.p.restored.Load(); idx != 0 && e.Position.Index <= idx {
		w.p.twice.Add(1)
	}
	return w.kv.Apply(e)
}

// Snapshot implements StateMachine, in inner's format.
func (w *restoreWatch) Snapshot() (coreconsensus.Snapshot, error) { return w.inner.Snapshot() }

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
	if w.p.armed.Load() {
		w.p.once.Do(func() { first = true })
	}
	var err error
	//: a skimming machine reads none of the stream it is handed.
	if !first || !w.skim {
		err = w.inner.Restore(r, meta)
	}
	//: the first restore once armed is reported, and the state it accepted watched.
	if first {
		//: a state the machine accepted is the one later entries must not be applied over again.
		if err == nil && !w.skim {
			w.p.restored.Store(meta.Position.Index)
		}
		//: the entries below the snapshot are handed after its restore, as the core may.
		if n := w.p.node.Load(); n != nil && err == nil {
			n.aq.push(applyItem{entries: []coreconsensus.EntryValue{
				{Kind: coreconsensus.EntryCommand, Position: coreconsensus.PositionValue{Index: meta.Position.Index - 2, Term: meta.Position.Term}, Data: []byte("twice=1")},
				{Kind: coreconsensus.EntryCommand, Position: coreconsensus.PositionValue{Index: meta.Position.Index - 1, Term: meta.Position.Term}, Data: []byte("twice=2")},
			}})
		}
		w.p.seen <- restoreSeen{before: before, after: keys(), err: err}
	}
	return err
}

// framedKV writes the kv as one framed value, its length then its bytes,
// followed, once trail is set, by a chunk's worth of newlines it never reads
// back: a machine that decodes one value and stops at its end, as a
// json.Decoder leaves the final newline of a json.Encoder unread. The
// trailer fills the snapshot's last chunk, so a cut of that chunk removes
// only bytes it does not read. Written from the start, each cut took a
// mebibyte, the cuts fell behind the proposals, and the logs were not
// compacted past the lagging node in time.
type framedKV struct {
	*kv
	trail *atomic.Bool
}

// Snapshot implements StateMachine, framed and trailed.
func (f framedKV) Snapshot() (coreconsensus.Snapshot, error) {
	inner, err := f.kv.Snapshot()
	//: a kv that cannot be captured leaves nothing to frame.
	if err != nil {
		return nil, err
	}
	data := inner.(kvSnap).data
	b := binary.AppendUvarint(nil, uint64(len(data)))
	b = append(b, data...)
	//: only the snapshots cut once trail is set carry the unread chunk.
	if f.trail.Load() {
		b = append(b, bytes.Repeat([]byte{'\n'}, defaultChunkSize)...)
	}
	return kvSnap{data: b}, nil
}

// Restore implements StateMachine: it reads the framed value and stops
// there, then replaces the state with it.
func (f framedKV) Restore(r io.Reader, meta coreconsensus.SnapshotMetaValue) error {
	n, err := binary.ReadUvarint(byteAtATime{r})
	//: a frame whose length cannot be read is refused.
	if err != nil {
		return err
	}
	data := make([]byte, n)
	//: a frame shorter than its length is refused.
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	return f.kv.Restore(bytes.NewReader(data), meta)
}

// byteAtATime reads r one byte at a time, so a frame's length is read
// without reading past it.
type byteAtATime struct{ r io.Reader }

// ReadByte implements io.ByteReader.
func (b byteAtATime) ReadByte() (byte, error) {
	var one [1]byte
	_, err := io.ReadFull(b.r, one[:])
	return one[0], err
}

// trailedCut sets trail, then proposes until every live node's newest
// snapshot was cut after it, and returns the last proposal's token.
func trailedCut(t *testing.T, c *cluster, trail *atomic.Bool) coreconsensus.TokenValue {
	t.Helper()
	trail.Store(true)
	var tok coreconsensus.TokenValue
	//: enough entries past the flag for a cut on every live node.
	for i := range 2 * int(c.every) {
		tok = proposeOnLeader(t, c, fmt.Sprintf("t%d=%d", i, i))
	}
	from := tok.Index - 2*c.every + 1
	deadline := time.Now().Add(10 * time.Second)
	//: each live node's newest snapshot is past the flag.
	for _, tn := range c.nodes {
		//: a stopped node cuts nothing.
		if tn.node == nil {
			continue
		}
		//: a node waits for a cut past the flag.
		for {
			metas, err := tn.snaps.List()
			//: a store that cannot list leaves nothing to judge.
			if err != nil {
				t.Fatal(err)
			}
			//: a newest snapshot cut after the flag carries the trailer.
			if len(metas) > 0 && metas[0].Position.Index > from {
				break
			}
			//: no cut past the flag in time.
			if time.Now().After(deadline) {
				t.Fatalf("setup: node %d cut no snapshot past %d", tn.id, from)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	return tok
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
// (#1562). A machine that reads the cut is refused SNAPSHOT_CORRUPT: it
// keeps its state, the copy is removed, nothing moves, and the next install
// restores the whole state. A machine that returned nil before the cut end,
// the rest read after it, accepted a stream the engine then refuses: it may
// hold the snapshot's state while applied stays below it, and the entries
// handed after the restore were applied over that state a second time. The
// node stops STATE_MACHINE_FAILED before it applies another entry, keeps
// the copy, and a start that restores that way fails the same; the next
// start reads the copy whole. An intact second read installs.
func TestARestoreWhoseSecondReadIsCutIsRefused(t *testing.T) {
	type tc struct {
		name string
		cut  bool
		skim bool
		// framed: every node writes the framed kv, and the lagging node's
		// restore stops at the end of its value; the start after the stop
		// is cut too.
		framed bool
	}
	runCase := func(t *testing.T, tcase tc) {
		var lagID atomic.Uint64
		stops := tcase.cut && (tcase.skim || tcase.framed)
		store := &secondReadCut{
			cut:      tcase.cut,
			opened:   make(chan string, 1),
			removed:  make(chan string, 1),
			retained: make(chan struct{}, 1),
			release:  make(chan struct{}),
		}
		store.cuts.Store(1)
		//: the framed case cuts the restart's restore as well.
		if tcase.framed {
			store.cuts.Store(2)
		}
		var storeOnce sync.Once
		var trail atomic.Bool
		probe := &restoreProbe{armed: &store.armed, seen: make(chan restoreSeen, 1)}
		c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 5, 6, 2}, every: 20, size: 3}
		c.configure = func(tn *testNode, cfg *Config) {
			var inner coreconsensus.StateMachine = tn.sm
			//: a framed cluster writes and reads the framed kv everywhere.
			if tcase.framed {
				inner = framedKV{kv: tn.sm, trail: &trail}
				cfg.Machine = inner
			}
			//: only the lagging node reads the doubles.
			if uint64(tn.id) == lagID.Load() {
				// Set once: a restart reuses the node's store, and the
				// stopped node's goroutines may still read it.
				storeOnce.Do(func() { store.SnapshotStore = tn.snaps })
				cfg.Snapshots = store
				cfg.Machine = &restoreWatch{kv: tn.sm, inner: inner, skim: tcase.skim, p: probe}
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
		//: the framed case's install is a snapshot cut with its unread chunk.
		if tcase.framed {
			tok = trailedCut(t, c, &trail)
		}
		compactedPast(t, c, held+1)
		lagID.Store(uint64(lag.id))
		c.start(lag)
		//: the framed case hands the lagging node entries below the snapshot after its restore.
		if tcase.framed {
			probe.node.Store(lag.node)
		}
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
		case got = <-probe.seen:
		//: the restore never reached the machine.
		case <-time.After(10 * time.Second):
			t.Fatalf("the restore of %s never reached the machine", id)
		}
		//: the injected entries sit above what the lagging node applied, below the snapshot.
		if index := snapshotIndex(t, store.SnapshotStore, id); tcase.framed && index <= held+2 {
			t.Fatalf("setup: the install %s at %d does not leave entries between the lagging node's %d and it", id, index, held)
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
			//: a machine that accepted the stream cannot be refused and kept running.
			if stops {
				t.Errorf("the machine accepted %s (error %v, %d keys against %d before), its end was refused, yet the node kept running: %s removed, Applied %d against the snapshot's %d",
					id, got.err, got.after, got.before, gone, st.Applied, index)
			}
			//: the refusal is the snapshot's own fault, the machine kept its state, and applied did not move.
			if tcase.cut && !stops && (!errs.HasCode(got.err, coreconsensus.CodeSnapshotCorrupt) || got.after != got.before || gone != id || st.Applied >= index) {
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
		//: the restore stopped the node.
		case <-lag.node.stopc:
			st := lag.node.Status()
			reason, _ := lag.node.StopReason()
			index := snapshotIndex(t, store.SnapshotStore, id)
			//: only a machine that accepted a stream whose end was refused stops the node.
			if !stops {
				t.Errorf("the restore of %s stopped the node: %v", id, reason.Err)
			}
			//: the stop says the machine is not vouched for, applied did not move, and the copy stays for the next start.
			if stops && (!errs.HasCode(reason.Err, coreconsensus.CodeStateMachineFailed) || got.err != nil || index == 0 || st.Applied >= index) {
				t.Errorf("the machine accepted %s, whose end was refused: the node stopped with %v (want STATE_MACHINE_FAILED), the machine read %v, the copy is at %d, Applied %d",
					id, reason.Err, got.err, index, st.Applied)
			}
		//: the install ended no way.
		case <-time.After(10 * time.Second):
			t.Fatal("the install neither installed, was removed, nor stopped the node")
		}
		released.Do(func() { close(store.release) })
		//: a stopped node is started again, the copy it kept read at that start.
		if stops {
			c.kill(lag)
			//: the framed case's next start meets the same cut and fails.
			if tcase.framed {
				lag.sm = newKV()
				n, err := NewNode(c.config(lag))
				//: a start that restored a stream it then refused does not run.
				if err == nil {
					abandon(n.HardClose())
					t.Errorf("a start whose restore accepted a stream it then refused started")
				}
				//: it fails with the stop it made.
				if err != nil && !errs.HasCode(err, coreconsensus.CodeStateMachineFailed) {
					t.Errorf("a start whose restore accepted a stream it then refused failed with %v, want STATE_MACHINE_FAILED", err)
				}
			}
			c.start(lag)
		}
		c.waitApplied(tok.Index, 10*time.Second)
		//: whatever the first install did, the node ends on the whole state.
		if keys, want := machineKeys(lag), machineKeys(l); keys != want {
			t.Errorf("caught up to %d, the lagging node's machine holds %d keys, the leader's %d", tok.Index, keys, want)
		}
		//: no entry the accepted snapshot holds is applied over it again.
		if twice := probe.twice.Load(); twice != 0 {
			t.Errorf("%d entries at or below the snapshot %s the machine accepted were applied over its state a second time", twice, id)
		}
	}
	//: each case runs its own cluster.
	for _, tcase := range []tc{
		{name: "a second read cut by a chunk is refused", cut: true},
		{name: "a cut stream a machine stopped reading stops the node", cut: true, skim: true},
		{name: "a cut end past what a machine decodes stops the node", cut: true, framed: true},
		{name: "an intact second read installs"},
	} {
		t.Run(tcase.name, func(t *testing.T) { runCase(t, tcase) })
	}
}
