// Package server — per-connection state recycling.
package server

import (
	stdnet "net"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// Test_Server_acquire pins the two things a pooled wrapper must arrive with: the
// group's own identity and bounds, and an entry in the live registry.
//
// The registry is what lets an expired drain budget sever a socket whose handler
// is blocked in Read. Without the entry the socket is invisible to Shutdown, so
// the budget becomes advisory and the drain blocks forever — which is precisely
// what a budget exists to prevent.
func Test_Server_acquire(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// bufferSize is the group's requested scratch buffer.
		bufferSize int
		// budget is the group's read timeout.
		budget time.Duration
	}
	tests := []tc{
		{name: "a group with no buffer"},
		{name: "a group with a small buffer", bufferSize: 64},
		//: larger than the pool's guaranteed minimum, so the wrapper has to
		//: allocate its own rather than hand back a short one.
		{name: "a group with a large buffer", bufferSize: 1 << 20},
		{name: "a group with bounds", bufferSize: 64, budget: time.Second},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := New()
		group := &StreamGroup{
			name:     "api",
			limits:   corenet.LimitsValue{ReadBufferSize: c.bufferSize},
			timeouts: timeouts(c.budget, 0, 0),
		}
		socket := &fakeSocket{}

		wrapper := srv.acquire(socket, group)

		if wrapper.Conn != stdnet.Conn(socket) {
			t.Fatal("the wrapper does not hold the socket it was given")
		}
		//: identifiers start at one, so zero is never a live connection.
		if wrapper.ID() == 0 {
			t.Error("the wrapper was handed out with no identifier")
		}
		if wrapper.Group() != group.name {
			t.Errorf("Group = %q, want %q", wrapper.Group(), group.name)
		}
		//: the group's bounds travel with the connection, or Read and Write
		//: would be unbounded whatever the group configured.
		if wrapper.timeouts != group.timeouts {
			t.Errorf("the wrapper carries %+v, want %+v", wrapper.timeouts, group.timeouts)
		}
		//: full length, ready to read into.
		if len(wrapper.Buffer()) != c.bufferSize {
			t.Errorf("Buffer has length %d, want %d", len(wrapper.Buffer()), c.bufferSize)
		}
		//: registered, or an expired drain budget could not sever this socket.
		srv.liveMu.RLock()
		registered, live := srv.live[wrapper.ID()]
		srv.liveMu.RUnlock()
		if !live || registered != stdnet.Conn(socket) {
			t.Fatal("the socket is not in the live registry — an expired drain " +
				"budget would have nothing to sever")
		}
		//: two connections never share an identifier, or the registry would
		//: lose one of them.
		second := srv.acquire(&fakeSocket{}, group)
		if second.ID() == wrapper.ID() {
			t.Errorf("two connections were both assigned id %d", second.ID())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Server_release pins that reclaiming a connection closes its socket,
// deregisters it and drops every reference.
//
// Closing here rather than in the handler means a handler that forgets — or
// panics — still cannot leak a descriptor. Deregistering FIRST is what keeps a
// concurrent force-close from touching a socket that is already going back to
// the pool.
func Test_Server_release(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// bufferSize is the group's requested scratch buffer.
		bufferSize int
		// closedEarly closes the socket before the release, as net/http may.
		closedEarly bool
	}
	tests := []tc{
		{name: "a connection with no buffer"},
		{name: "a connection with a buffer", bufferSize: 64},
		//: net/http can close a connection before the engine reclaims it.
		{name: "a connection already closed", closedEarly: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := New()
		group := &StreamGroup{name: "api", limits: corenet.LimitsValue{ReadBufferSize: c.bufferSize}}
		socket := &fakeSocket{}
		wrapper := srv.acquire(socket, group)
		id := wrapper.ID()
		if c.closedEarly {
			if cerr := wrapper.Close(); cerr != nil {
				t.Fatalf("close: %v", cerr)
			}
		}

		srv.release(wrapper)

		//: the socket is closed whatever the handler did.
		if socket.closes == 0 {
			t.Error("the socket was never closed — a handler that forgets would leak it")
		}
		//: deregistered, or a later force-close would touch a pooled wrapper.
		srv.liveMu.RLock()
		_, live := srv.live[id]
		srv.liveMu.RUnlock()
		if live {
			t.Error("the socket is still in the live registry after being reclaimed")
		}
		//: every reference dropped, or a pooled entry pins a closed descriptor.
		if wrapper.Conn != nil || wrapper.scratch != nil || wrapper.group != "" {
			t.Errorf("the reclaimed wrapper still holds state: %+v", wrapper)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Server_closeLive pins the TEETH behind the drain budget.
//
// Cancelling the context asks a handler to stop, but a handler blocked in Read
// will not notice until its socket closes underneath it. Without this, an
// expired budget would report a timeout and then wait anyway — which is the
// exact bug the budget exists to prevent.
func Test_Server_closeLive(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// count is how many connections are being served.
		count int
	}
	tests := []tc{
		{name: "nothing in flight"},
		{name: "one connection", count: 1},
		{name: "several connections", count: 8},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := New()
		group := &StreamGroup{name: "api"}
		sockets := make([]*fakeSocket, 0, c.count)
		for range c.count {
			socket := &fakeSocket{}
			sockets = append(sockets, socket)
			srv.acquire(socket, group)
		}

		srv.closeLive()

		//: every live socket is severed, so every blocked handler wakes up.
		for i, socket := range sockets {
			if socket.closes == 0 {
				t.Errorf("socket %d was not severed — its handler would stay blocked "+
					"in Read past the drain budget", i)
			}
		}
		//: calling it twice is what Close after Shutdown does, and a socket
		//: already closed is the expected case rather than a fault.
		srv.closeLive()
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
