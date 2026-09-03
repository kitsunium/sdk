// Package server — per-connection state recycling.
package server

import (
	"maps"
	stdnet "net"
	"slices"

	"github.com/kitsunium/sdk/internal/kernel/buffer"
)

// acquire takes a pooled connection wrapper and fills it for this socket.
//
// Pooling the wrapper — and the scratch buffer with it — is what keeps a
// steady-state accept loop from allocating per connection. The cost of getting
// it wrong is a wrapper that outlives its socket, which is why release drops
// every reference it holds.
func (s *Server) acquire(raw stdnet.Conn, group *StreamGroup) *conn {
	c := s.connPool.Get()
	c.Conn = raw
	c.id = s.nextID.Add(1)
	c.group = group.name
	//: register the socket so an expired drain budget can sever it.
	s.liveMu.Lock()
	s.live[c.id] = raw
	s.liveMu.Unlock()
	//: only take a scratch buffer when the group asked for one, so a handler
	//: that never calls Buffer costs nothing.
	if size := group.limits.ReadBufferSize; size > 0 {
		c.scratch = buffer.Get()
		//: the pool guarantees a minimum capacity, not this group's size, so a
		//: larger request needs its own allocation.
		if cap(*c.scratch) < size {
			c.scratch = new(make([]byte, size))
		}
		*c.scratch = (*c.scratch)[:size]
	}
	//: filled and registered, ready for the handler.
	return c
}

// release closes the socket and returns the wrapper to the pool.
//
// The close error is not surfaced: this runs on a deferred path with no caller
// to report to, and by this point the handler has already finished, so a
// failing close cannot change the outcome it produced.
func (s *Server) release(c *conn) {
	//: drop the registry entry first, so a concurrent force-close cannot touch
	//: a socket that is already going back to the pool.
	s.liveMu.Lock()
	delete(s.live, c.id)
	s.liveMu.Unlock()
	//: closing here rather than in the handler means a handler that forgets —
	//: or panics — still cannot leak a descriptor. Close is nil-safe, so a
	//: connection net/http already closed costs nothing here.
	swallowErr(c.Close())
	//: hand the scratch buffer back before dropping the reference to it.
	if c.scratch != nil {
		buffer.Put(c.scratch)
	}
	c.reset()
	s.connPool.Put(c)
}

// closeLive severs every socket still being served.
//
// It is the teeth behind the drain budget: cancelling the context asks a
// handler to stop, but a handler blocked in Read will not notice until its
// socket closes underneath it. Close errors are ignored because a socket the
// handler already closed is the expected case, not a fault.
func (s *Server) closeLive() {
	s.liveMu.RLock()
	//: collect under the read lock, close outside it — Close can block, and
	//: holding the lock through it would stall every connection trying to
	//: deregister itself.
	sockets := slices.Collect(maps.Values(s.live))
	s.liveMu.RUnlock()
	//: sever each socket so its handler's next read or write fails.
	for _, socket := range sockets {
		swallowErr(socket.Close())
	}
}
