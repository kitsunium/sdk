// Package server — a portable connection whose output failed after its
// reader ended cleanly is closed, not held until the stop (#982's review).
package server

import (
	"bytes"
	stdnet "net"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// writeFailH answers the first newline-delimited request with a large answer,
// and reports each close.
type writeFailH struct {
	answer []byte
	closed chan error
	// ref is the one connection; peerDone closes once the lane has taken its
	// reader's clean end of input.
	ref      corenet.LoopConnRef
	peerDone chan struct{}
	signaled bool
}

// OnBatchEnd runs on the lane's goroutine, the one that owns its connections:
// it reads whether the lane has taken the reader's end of input.
func (h *writeFailH) OnBatchEnd(io corenet.LoopIO) {
	pl, ok := io.(*portableLoop)
	if ok && !h.signaled && int(h.ref.Slot) < len(pl.conns) && pl.conns[h.ref.Slot].peerDone {
		h.signaled = true
		close(h.peerDone)
	}
}

func (h *writeFailH) OnOpen(_ corenet.LoopIO, ref corenet.LoopConnRef) { h.ref = ref }
func (h *writeFailH) OnWritable(corenet.LoopIO, corenet.LoopConnRef)   {}
func (h *writeFailH) OnTimeout(corenet.LoopIO, corenet.LoopConnRef)    {}

func (h *writeFailH) OnClose(_ corenet.LoopIO, _ corenet.LoopConnRef, err error) {
	select {
	case h.closed <- err:
	default:
	}
}

func (h *writeFailH) OnData(io corenet.LoopIO, ref corenet.LoopConnRef, buf []byte) int {
	i := bytes.IndexByte(buf, '\n')
	if i < 0 {
		return 0
	}
	swallowErr(io.Write(ref, h.answer))
	return i + 1
}

// Test_portableWriteFailAfterEOF pins that a connection whose writer failed is
// closed even when its reader already ended on a clean EOF: the peer shuts its
// writing side, the server is still sending an answer larger than the kernel's
// buffers, then the peer closes with that answer unread and the send fails.
// Nothing reads the socket any more, so the writer is the one to end the
// connection; it used to leave it open until the stop.
func Test_portableWriteFailAfterEOF(t *testing.T) {
	h := &writeFailH{answer: bytes.Repeat([]byte("a"), 48<<20), closed: make(chan error, 1), peerDone: make(chan struct{})}
	srv := New()
	t.Cleanup(func() { swallowErr(srv.Close()) })
	srv.Group("pl", Listen("tcp", "127.0.0.1:0"),
		Loop(corenet.LoopValue{Backend: corenet.LoopBackendPortable, Loops: 1, OutputCap: 64 << 20})).HandleLoop(h)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	c := mustDial(t, srv.State().Listeners[0].Address)
	tc, ok := c.(*stdnet.TCPConn)
	if !ok {
		t.Fatalf("conn is %T", c)
	}
	if _, err := c.Write([]byte("go\n")); err != nil {
		t.Fatal(err)
	}
	// The FIN: the server's reader ends on io.EOF while its writer is blocked.
	if err := tc.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	// The lane has taken the end of input: the writer is the only one left.
	select {
	case <-h.peerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the lane never took the end of input")
	}
	// The RST: the answer is unread, so the blocked send fails.
	if err := tc.SetLinger(0); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the connection stayed open after its output failed")
	}
}
