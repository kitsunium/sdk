// Package server — the listener bridge that lets net/http consume our accepts.
package server

import (
	stdnet "net"
	"sync"
)

// chanListener is a net.Listener fed by our accept path.
//
// net/http can only be driven through Serve(net.Listener); there is no public
// per-connection entry point. Rather than reimplement HTTP — which would mean
// owning request smuggling, HTTP/2 flow control, HPACK and the whole CVE stream
// to lose, not gain, throughput — the adapter hands http.Server a listener whose
// Accept pops from a channel our own accept loop fills. The cost is one channel
// hand-off per CONNECTION, not per request, which is noise next to a TCP
// handshake, and the benchmarks measure it rather than assuming it.
type chanListener struct {
	// conns carries accepted connections to http.Server.
	conns chan stdnet.Conn
	// addr is reported to http.Server as the listen address.
	addr stdnet.Addr
	// closed is shut to unblock Accept when the group drains.
	closed chan struct{}
	// once makes Close idempotent, since http.Server also closes its listener.
	once sync.Once
}

// newChanListener builds a bridge listener for the given address.
func newChanListener(addr stdnet.Addr) *chanListener {
	//: unbuffered: a connection is handed over only when http.Server is ready
	//: for it, so the queue depth stays the accept path's business, not ours.
	return &chanListener{
		conns:  make(chan stdnet.Conn),
		addr:   addr,
		closed: make(chan struct{}),
	}
}

// Accept implements net.Listener.
func (l *chanListener) Accept() (conn stdnet.Conn, err error) {
	select {
	//: a connection our accept loop handed over.
	case c := <-l.conns:
		//: a connection our accept loop handed over.
		return c, nil
	//: the group is draining; report the error http.Server expects.
	case <-l.closed:
		//: the termination http.Server expects from a closed listener.
		return nil, stdnet.ErrClosed
	}
}

// Close implements net.Listener.
func (l *chanListener) Close() error {
	//: http.Server closes its listener too, so this must be idempotent.
	l.once.Do(func() { close(l.closed) })
	//: closing a bridge cannot fail; there is no descriptor behind it.
	return nil
}

// Addr implements net.Listener.
func (l *chanListener) Addr() stdnet.Addr {
	//: the real bound address, so http.Server logs something meaningful.
	return l.addr
}

// offer hands a connection to http.Server, or reports that the bridge is closed.
func (l *chanListener) offer(c stdnet.Conn) bool {
	select {
	//: handed over; http.Server now owns the connection's lifetime.
	case l.conns <- c:
		//: handed over; http.Server now owns the connection's lifetime.
		return true
	//: the bridge closed while we were waiting to hand over.
	case <-l.closed:
		//: the bridge closed while we waited to hand over.
		return false
	}
}
