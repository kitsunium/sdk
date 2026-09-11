// Package server — the socket that holds its connection slot until it closes.
package server

import (
	"crypto/tls"
	"io"
	stdnet "net"
	"sync"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// trackedConn is an accepted socket that reports its own Close, so a ceilinged
// group keeps counting a connection a handler has taken over.
//
// Once a handler hijacks a response the engine neither closes the socket nor
// waits for it (ADR 0047 §D9), and ServeConn returns at the hijack — so the
// slot a ceiling holds for the length of ServeConn was given back while the
// connection stayed open. The one event left that says such a connection is
// over is somebody closing it, and this type is where that event is heard.
//
// It sits BELOW TLS — tls.NewListener wraps it — so net/http still receives
// the concrete *tls.Conn it type-asserts on, and Request.TLS is populated as
// before. On a plaintext listener net/http receives this type itself, which
// is why it forwards the two optional interfaces net/http looks for on a raw
// connection instead of hiding them: CloseWrite, for the graceful half-close
// before a server-side close, and ReadFrom, for the sendfile/splice path. A
// hijacking handler on such a group is handed this type too, never the
// *net.TCPConn underneath it.
//
// Only a group that serves HTTP under a ceiling builds one — nothing else can
// hijack, and nothing without a ceiling has a slot to hold — so every other
// group's sockets reach the engine unwrapped and cost nothing extra.
type trackedConn struct {
	stdnet.Conn
	// mu guards closed and slot: a hijacking handler closes the socket on its
	// own goroutine while the engine hands the slot over on the serve one.
	mu sync.Mutex
	// closed records that Close has run.
	closed bool
	// slot is the ceiling whose slot this socket holds, nil while it holds
	// none — which is always, except after a hijack.
	slot *connLimiter
}

// Close closes the socket, then returns the slot it holds, if any.
//
// The socket closes FIRST, so for an instant the ceiling can count one socket
// that is already gone, but never admit a new one while this one is still
// open.
func (t *trackedConn) Close() error {
	err := t.Conn.Close()
	t.mu.Lock()
	t.closed = true
	slot := t.slot
	//: cleared under the lock, so a second Close finds nothing to return.
	t.slot = nil
	t.mu.Unlock()
	//: a socket that holds no slot has nothing to settle.
	if slot != nil {
		slot.release()
	}
	//: the socket's own close result, exactly as an unwrapped socket reports it.
	return err
}

// holdSlot hands a ceiling's slot to this socket, to be returned by its Close.
//
// A socket that is already closed returns it at once: a handler can hijack
// and close before the engine has finished with the connection, and nothing
// would ever close it again to return the slot.
func (t *trackedConn) holdSlot(slot *connLimiter) {
	t.mu.Lock()
	//: closed before the hand-over — settle it here.
	if t.closed {
		t.mu.Unlock()
		slot.release()
		//: nothing left to hold.
		return
	}
	t.slot = slot
	t.mu.Unlock()
}

// CloseWrite shuts the socket's write half, forwarded so net/http's graceful
// half-close before a server-side close still reaches a plaintext socket.
func (t *trackedConn) CloseWrite() error {
	halfCloser, ok := t.Conn.(interface{ CloseWrite() error })
	//: a socket with no separate write half. None this engine accepts — TCP and
	//: Unix sockets both have one — and net/http ignores the result either way.
	if !ok {
		//: nothing to shut.
		return nil
	}
	//: the socket's own half-close.
	return halfCloser.CloseWrite()
}

// ReadFrom copies r to the socket through the socket's own ReadFrom when it
// has one, which is what keeps sendfile and splice on net/http's file-serving
// path for a plaintext socket.
func (t *trackedConn) ReadFrom(r io.Reader) (n int64, err error) {
	//: a TCP socket's ReadFrom is the zero-copy path.
	if readerFrom, ok := t.Conn.(io.ReaderFrom); ok {
		//: the socket's own copy.
		return readerFrom.ReadFrom(r)
	}
	//: the plain copy net/http itself falls back to. The socket has no
	//: ReadFrom, so io.Copy cannot recurse back into this one.
	return io.Copy(t.Conn, r)
}

// hijackedTracker returns the tracker under c's socket when a handler has
// taken that socket over, or nil — when nothing was hijacked, or the group
// does not track closes.
func hijackedTracker(c corenet.Conn) *trackedConn {
	pooled, ok := c.(*conn)
	//: only the engine's own wrapper carries the hijack flag.
	if !ok || !pooled.hijacked {
		//: still the engine's connection, so its slot ends with the handler.
		return nil
	}
	socket := pooled.Conn
	//: on a TLS listener the tracker is under the TLS layer, where
	//: tls.NewListener put it.
	if secured, isTLS := socket.(*tls.Conn); isTLS {
		socket = secured.NetConn()
	}
	tracked, isTracked := socket.(*trackedConn)
	//: a group that does not track closes built no tracker.
	if !isTracked {
		//: nothing can report this socket's Close.
		return nil
	}
	//: the socket that will say when the connection is over.
	return tracked
}
