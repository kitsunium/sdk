package consensus

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// SaveSnapshot wrote the caller's writer on the apply goroutine: a writer
// that stopped reading — a pipe to an upload that stalled, a connection
// whose peer stopped reading — held it for good once the caller gave up.
// The node applied nothing more, its Barriers expired, it cut nothing, and
// Close never returned (#601). The writer is now copied to from the
// published snapshot, on a goroutine of the caller's.
func TestASaveSnapshotWriterThatStopsReadingDoesNotFreezeApply(t *testing.T) {
	c := newCluster(t, 3, 1000)
	l := c.settledLeader(5 * time.Second)
	for i := range 2000 {
		propose(t, l.node, fmt.Sprintf("k%04d=value-%04d-padding-padding-padding", i, i))
	}
	f := c.otherThan(l)
	c.waitApplied(l.node.Status().Applied, 5*time.Second)
	pr, pw := io.Pipe()
	defer pr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	_, err := f.node.SaveSnapshot(ctx, "operator:backup", pw)
	cancel()
	if err == nil {
		t.Fatal("SaveSnapshot into a pipe no one reads succeeded")
	}
	t.Logf("SaveSnapshot on follower %d returned %v (caller gone, the pipe is never read)", f.id, err)
	for i := range 20 {
		propose(t, l.node, fmt.Sprintf("after%02d=v", i))
	}
	c.waitApplied(l.node.Status().Applied, 5*time.Second)
	bctx, bcancel := context.WithTimeout(context.Background(), time.Second)
	_, berr := f.node.Barrier(bctx)
	bcancel()
	if berr != nil {
		t.Errorf("a Barrier on the follower after an abandoned SaveSnapshot: %v", berr)
	}
	closed := make(chan error, 1)
	go func() {
		cctx, ccancel := context.WithTimeout(context.Background(), time.Second)
		defer ccancel()
		closed <- f.node.Close(cctx)
	}()
	select {
	case cerr := <-closed:
		if cerr != nil {
			t.Errorf("Close after an abandoned SaveSnapshot: %v", cerr)
		}
	case <-time.After(3 * time.Second):
		t.Error("an abandoned SaveSnapshot holds Close: still running after 3 s")
		pr.CloseWithError(io.ErrClosedPipe)
		<-closed
	}
	f.node = nil
}

// The stream SaveSnapshot copies is the snapshot it published, byte for
// byte, and its metadata is that snapshot's.
func TestSaveSnapshotCopiesThePublishedSnapshot(t *testing.T) {
	c := newCluster(t, 3, 1000)
	l := c.settledLeader(5 * time.Second)
	for i := range 50 {
		propose(t, l.node, fmt.Sprintf("k%02d=v", i))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var buf bytes.Buffer
	meta, err := l.node.SaveSnapshot(ctx, "operator:backup", &buf)
	if err != nil {
		t.Fatal(err)
	}
	h, err := l.snaps.Open(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	want := make([]byte, h.Meta().EncodedSize)
	if _, err := h.ReadAt(want, 0); err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if hm := h.Meta(); meta.ID != hm.ID || meta.SHA256 != hm.SHA256 || !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("SaveSnapshot copied %d bytes under %+v, the store holds %d under %+v", buf.Len(), meta.Position, len(want), h.Meta().Position)
	}
}

// leavingWriter takes 50 ms per write, counts its writes and bytes, and
// calls leave during its second write: the caller gives up mid-copy.
type leavingWriter struct {
	writes, bytes atomic.Int64
	leave         func()
}

func (w *leavingWriter) Write(p []byte) (int, error) {
	if w.writes.Load() == 1 {
		w.leave()
	}
	time.Sleep(50 * time.Millisecond)
	w.writes.Add(1)
	w.bytes.Add(int64(len(p)))
	return len(p), nil
}

// SaveSnapshot returned ctx.Err() once its caller left while its copy
// went on writing to the caller's writer, chunk after chunk, to the end: a
// caller that reused, removed or read w after the call raced with the
// engine (#612). The copy now stops at its next read once ctx is done;
// only the write already begun may end after the return.
func TestSaveSnapshotStopsWritingOnceTheCallerLeft(t *testing.T) {
	c := newCluster(t, 3, 100000)
	l := c.settledLeader(5 * time.Second)
	for i := range 8 {
		b := make([]byte, 32<<10)
		_, _ = rand.Read(b)
		propose(t, l.node, fmt.Sprintf("k%d=%s", i, hex.EncodeToString(b)))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	w := &leavingWriter{leave: cancel}
	meta, err := l.node.SaveSnapshot(ctx, "operator:backup", w)
	if err == nil {
		t.Fatalf("setup: the copy of a %d-byte snapshot ended before the caller left", meta.EncodedSize)
	}
	at, atBytes := w.writes.Load(), w.bytes.Load()
	time.Sleep(time.Second)
	after, afterBytes := w.writes.Load(), w.bytes.Load()
	t.Logf("SaveSnapshot returned %v after %d writes (%d bytes); 1 s later %d writes (%d bytes)", err, at, atBytes, after, afterBytes)
	if after > at+1 {
		t.Errorf("the engine went on writing to the caller's writer after SaveSnapshot returned: %d more writes, %d bytes", after-at, afterBytes-atBytes)
	}
}

// windowsSnaps gives a store Windows' rename semantics: Go opens a file
// there without FILE_SHARE_DELETE, so the rename of a commit over a
// published snapshot a handle holds open fails, and the commit answers
// STORAGE_FAILED ("publish snapshot").
type windowsSnaps struct {
	coreconsensus.SnapshotStore
	mu    sync.Mutex
	opens map[string]int
}

type windowsHandle struct {
	coreconsensus.SnapshotHandle
	s    *windowsSnaps
	once sync.Once
}

func (h *windowsHandle) Close() error {
	h.once.Do(func() {
		h.s.mu.Lock()
		h.s.opens[h.Meta().ID]--
		h.s.mu.Unlock()
	})
	return h.SnapshotHandle.Close()
}

type windowsSink struct {
	coreconsensus.SnapshotSink
	s  *windowsSnaps
	id string
}

func (k *windowsSink) Commit(final coreconsensus.SnapshotMetaValue) error {
	k.s.mu.Lock()
	open := k.s.opens[k.id]
	k.s.mu.Unlock()
	if open > 0 {
		abandon(k.SnapshotSink.Abort())
		return storageFailed("publish snapshot", errors.New("Access is denied."))
	}
	return k.SnapshotSink.Commit(final)
}

func (s *windowsSnaps) Open(id string) (coreconsensus.SnapshotHandle, error) {
	h, err := s.SnapshotStore.Open(id)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.opens[id]++
	s.mu.Unlock()
	return &windowsHandle{SnapshotHandle: h, s: s}, nil
}

func (s *windowsSnaps) Create(id string, meta coreconsensus.SnapshotMetaValue) (coreconsensus.SnapshotSink, int64, error) {
	k, off, err := s.SnapshotStore.Create(id, meta)
	if err != nil {
		return nil, 0, err
	}
	return &windowsSink{SnapshotSink: k, s: s, id: id}, off, nil
}

// stuckWriter blocks every write until block is closed.
type stuckWriter struct{ block chan struct{} }

func (w stuckWriter) Write(p []byte) (int, error) { <-w.block; return len(p), nil }

// SaveSnapshot cut at the applied index whether or not the snapshot of
// that id was published already, and committed it again over the file. On
// an idle node an abandoned copy of the first save still held it open, and
// on Windows the rename of the second save failed and stopped the node
// STORAGE_FAILED (#611). A save at an index already published copies that
// snapshot instead.
func TestASecondSaveOfAnIdleNodeDoesNotRecommitThePublishedSnapshot(t *testing.T) {
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 2, 3}, every: 100000, size: 3}
	c.configure = func(tn *testNode, cfg *Config) {
		cfg.Snapshots = &windowsSnaps{SnapshotStore: tn.snaps, opens: map[string]int{}}
	}
	newClusterWith(t, c)
	l := c.settledLeader(5 * time.Second)
	for i := range 10 {
		propose(t, l.node, fmt.Sprintf("k%02d=v", i))
	}
	block := make(chan struct{})
	defer close(block)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	first, err := l.node.SaveSnapshot(ctx, "operator:backup", stuckWriter{block})
	cancel()
	t.Logf("first SaveSnapshot (upload stalled, caller gave up): %v", err)
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var buf bytes.Buffer
	second, err := l.node.SaveSnapshot(ctx, "operator:backup", &buf)
	time.Sleep(200 * time.Millisecond)
	if l.node.stopped.Load() {
		t.Fatalf("a second backup of an idle node committed the published snapshot again, over the handle the first one's copy holds: the node stopped: %v", err)
	}
	if err != nil {
		t.Fatalf("the second SaveSnapshot: %v", err)
	}
	if first.ID != "" && second.ID != first.ID {
		t.Errorf("the second save is %s, the first %s, at the same applied index", second.ID, first.ID)
	}
	if int64(buf.Len()) != second.EncodedSize {
		t.Errorf("the second save copied %d bytes of a %d-byte snapshot", buf.Len(), second.EncodedSize)
	}
}
