package consensus

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"
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

// slowWriter takes 50 ms per write, and counts its writes and bytes.
type slowWriter struct{ writes, bytes atomic.Int64 }

func (w *slowWriter) Write(p []byte) (int, error) {
	time.Sleep(50 * time.Millisecond)
	w.writes.Add(1)
	w.bytes.Add(int64(len(p)))
	return len(p), nil
}

// SaveSnapshot returned ctx.Err() at its deadline while its copy went on
// writing to the caller's writer, chunk after chunk, to the end: a caller
// that reused, removed or read w after the call raced with the engine
// (#612). The copy now stops at its next read once ctx is done; only the
// write already begun may end after the return.
func TestSaveSnapshotStopsWritingOnceTheCallerLeft(t *testing.T) {
	c := newCluster(t, 3, 100000)
	l := c.settledLeader(5 * time.Second)
	for i := range 8 {
		b := make([]byte, 32<<10)
		_, _ = rand.Read(b)
		propose(t, l.node, fmt.Sprintf("k%d=%s", i, hex.EncodeToString(b)))
	}
	w := &slowWriter{}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	_, err := l.node.SaveSnapshot(ctx, "operator:backup", w)
	cancel()
	if err == nil {
		t.Fatal("setup: the copy ended before the deadline")
	}
	at, atBytes := w.writes.Load(), w.bytes.Load()
	time.Sleep(time.Second)
	after, afterBytes := w.writes.Load(), w.bytes.Load()
	t.Logf("SaveSnapshot returned %v after %d writes (%d bytes); 1 s later %d writes (%d bytes)", err, at, atBytes, after, afterBytes)
	if after > at+1 {
		t.Errorf("the engine went on writing to the caller's writer after SaveSnapshot returned: %d more writes, %d bytes", after-at, afterBytes-atBytes)
	}
}
