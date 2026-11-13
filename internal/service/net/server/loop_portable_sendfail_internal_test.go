// Package server — a portable connection whose send failed reports that
// failure at OnClose, whichever end of its reader the lane takes first
// (#971's blackhole on the portable backend).
package server

import (
	"errors"
	"io"
	stdnet "net"
	"sync"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// errSendTimedOut stands for the socket error a user timeout gives the send:
// the kernel hands it to one call only, here the writer's.
var errSendTimedOut = errors.New("write: connection timed out")

// sendFailConn is a connection whose send fails, and whose reader ends, with
// end, only as the writer closes it — after the send failed, as on a socket
// whose user timeout fired. Close waits, a bounded time, for the lane to take
// the reader's end before it returns, so the writer's own end comes second:
// the order a loaded lane may see.
type sendFailConn struct {
	end     error
	readEnd chan struct{}
	once    sync.Once
	// taken closes once the handler heard OnClose.
	taken chan struct{}
}

func (c *sendFailConn) Read([]byte) (int, error) {
	<-c.readEnd
	return 0, c.end
}

func (*sendFailConn) Write([]byte) (int, error) { return 0, errSendTimedOut }

func (c *sendFailConn) Close() error {
	first := false
	c.once.Do(func() {
		first = true
		close(c.readEnd)
	})
	//: the writer's close: the lane takes the reader's end first.
	if first {
		select {
		case <-c.taken:
		case <-time.After(2 * time.Second):
		}
	}
	return nil
}

func (*sendFailConn) LocalAddr() stdnet.Addr           { return &stdnet.TCPAddr{} }
func (*sendFailConn) RemoteAddr() stdnet.Addr          { return &stdnet.TCPAddr{} }
func (*sendFailConn) SetDeadline(time.Time) error      { return nil }
func (*sendFailConn) SetReadDeadline(time.Time) error  { return nil }
func (*sendFailConn) SetWriteDeadline(time.Time) error { return nil }

// sendFailH queues an answer at OnOpen and records the close.
type sendFailH struct {
	taken  chan struct{}
	closed chan error
}

func (*sendFailH) OnData(corenet.LoopIO, corenet.LoopConnRef, []byte) int { return 0 }
func (*sendFailH) OnWritable(corenet.LoopIO, corenet.LoopConnRef)         {}
func (*sendFailH) OnTimeout(corenet.LoopIO, corenet.LoopConnRef)          {}
func (*sendFailH) OnBatchEnd(corenet.LoopIO)                              {}

func (h *sendFailH) OnOpen(lio corenet.LoopIO, ref corenet.LoopConnRef) {
	_ = lio.Write(ref, []byte("answer"))
}

func (h *sendFailH) OnClose(_ corenet.LoopIO, _ corenet.LoopConnRef, err error) {
	h.closed <- err
	close(h.taken)
}

// Test_portableSendFailureIsTheClose pins that a failed send is the error
// OnClose reports: the writer records it before it closes the socket, so the
// reader's clean EOF (the kernel gave the socket error to the send) or its
// read on the closed socket, taken first, no longer ends the connection
// cleanly. On the runner's veth pair a 1 s user timeout ended a portable
// connection "cleanly" 972 ms after its peer vanished, where every other
// engine reported "connection timed out".
func Test_portableSendFailureIsTheClose(t *testing.T) {
	for _, c := range []struct {
		name string
		end  error
	}{
		{name: "reader-eof", end: io.EOF},
		{name: "reader-closed", end: stdnet.ErrClosed},
	} {
		t.Run(c.name, func(t *testing.T) {
			taken := make(chan struct{})
			h := &sendFailH{taken: taken, closed: make(chan error, 1)}
			pl := newOpenPortable(h, 0)
			conn := &sendFailConn{end: c.end, readEnd: make(chan struct{}), taken: taken}
			t.Cleanup(func() { swallowErr(conn.Close()) })
			pl.open(conn)
			pl.batch()
			deadline := time.After(5 * time.Second)
			for {
				select {
				case err := <-h.closed:
					if !errors.Is(err, errSendTimedOut) {
						t.Fatalf("OnClose(%v), want the send's failure %v", err, errSendTimedOut)
					}
					return
				case ev := <-pl.events:
					pl.handle(&ev)
					pl.batch()
				case <-deadline:
					t.Fatal("the connection never closed")
				}
			}
		})
	}
}
