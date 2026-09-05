// Package server — the listener bridge that lets net/http consume our accepts.
package server

import (
	"errors"
	stdnet "net"
	"testing"
	"time"
)

// chanListener is the net.Listener net/http is handed; the assertion is what
// keeps a signature change from silently detaching it.
var _ stdnet.Listener = (*chanListener)(nil)

// Test_newChanListener pins that the bridge is UNBUFFERED.
//
// A connection is handed over only when http.Server is ready for it, so the
// queue depth stays the accept path's business rather than becoming a second,
// invisible backlog with no ceiling and no accounting. A buffered bridge would
// let the engine accept past the group's limits and park connections nothing is
// tracking.
func Test_newChanListener(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// addr is the address the bridge reports.
		addr stdnet.Addr
	}
	tests := []tc{
		{name: "a TCP address", addr: &stdnet.TCPAddr{IP: stdnet.IPv4(127, 0, 0, 1), Port: 8080}},
		{name: "a unix address", addr: &stdnet.UnixAddr{Name: "/run/svc.sock", Net: "unix"}},
		//: an adapter can be built before a connection reveals an address.
		{name: "no address at all"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		bridge := newChanListener(c.addr)

		if bridge.Addr() != c.addr {
			t.Fatalf("Addr = %v, want %v", bridge.Addr(), c.addr)
		}
		//: unbuffered: an offer with nobody accepting must not complete.
		if cap(bridge.conns) != 0 {
			t.Errorf("the bridge buffers %d connections — the engine would accept "+
				"past the group's limits into a backlog nothing tracks", cap(bridge.conns))
		}
		//: a fresh bridge is open, so Accept blocks rather than reporting closed.
		select {
		case <-bridge.closed:
			t.Fatal("a fresh bridge is already closed")
		default:
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_chanListener_Accept pins the two ways Accept returns: a connection the
// accept loop handed over, and net.ErrClosed once the group drains.
//
// Goroutine lifecycle: one goroutine per case stands in for http.Server and
// blocks in Accept. It always reports on a buffered channel the case receives
// from before returning, so it cannot outlive the case.
//
// The error matters as much as the connection. net/http treats net.ErrClosed as
// the normal end of Serve; anything else it logs and, depending on the error,
// retries — so a bridge that reported something different on drain would either
// spin or fill the log with a shutdown that worked.
func Test_chanListener_Accept(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// closedFirst closes the bridge before the accept.
		closedFirst bool
	}
	tests := []tc{
		{name: "a connection handed over"},
		{name: "a bridge that has drained", closedFirst: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		bridge := newChanListener(nil)
		socket := &fakeSocket{}

		if c.closedFirst {
			if cerr := bridge.Close(); cerr != nil {
				t.Fatalf("Close = %v, want nil", cerr)
			}
			got, err := bridge.Accept()
			//: the termination http.Server expects; anything else makes it spin
			//: or log a shutdown that worked.
			if !errors.Is(err, stdnet.ErrClosed) {
				t.Fatalf("Accept = %v, want net.ErrClosed", err)
			}
			if got != nil {
				t.Error("Accept returned a connection from a closed bridge")
			}
			return
		}
		accepted := make(chan stdnet.Conn, 1)
		go func() {
			conn, err := bridge.Accept()
			if err != nil {
				accepted <- nil
				return
			}
			accepted <- conn
		}()
		if !bridge.offer(socket) {
			t.Fatal("the bridge refused a connection while open")
		}
		select {
		case got := <-accepted:
			if got != stdnet.Conn(socket) {
				t.Fatalf("Accept returned %v, want the offered socket", got)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Accept never returned the offered connection")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_chanListener_Close pins IDEMPOTENCE. http.Server closes its listener too,
// on its own path, so a second close is the expected case rather than a fault —
// and closing an already-closed channel is a panic, on a goroutine nobody is
// recovering.
func Test_chanListener_Close(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// closes is how many times the bridge is closed.
		closes int
	}
	tests := []tc{
		{name: "closed once", closes: 1},
		//: the engine's drain and http.Server both close it.
		{name: "closed twice", closes: 2},
		{name: "closed many times", closes: 8},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		bridge := newChanListener(nil)

		for i := range c.closes {
			//: reaching the second iteration at all is the property: closing a
			//: closed channel panics.
			if err := bridge.Close(); err != nil {
				t.Fatalf("close %d = %v, want nil", i, err)
			}
		}

		//: once closed, Accept reports it rather than blocking forever.
		if _, err := bridge.Accept(); !errors.Is(err, stdnet.ErrClosed) {
			t.Fatalf("Accept = %v, want net.ErrClosed", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_chanListener_Addr pins the address handed to http.Server. It is the real
// bound address rather than the bridge's own fiction, so anything net/http logs
// or reports names something an operator can actually dial.
func Test_chanListener_Addr(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// addr is the address the accepting socket reported.
		addr stdnet.Addr
	}
	tests := []tc{
		{name: "a TCP address", addr: &stdnet.TCPAddr{IP: stdnet.IPv4(10, 0, 0, 1), Port: 443}},
		{name: "a unix address", addr: &stdnet.UnixAddr{Name: "/run/svc.sock", Net: "unix"}},
		{name: "no address at all"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := newChanListener(c.addr).Addr(); got != c.addr {
			t.Fatalf("Addr = %v, want %v", got, c.addr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_chanListener_offer pins that a hand-off which cannot land is REPORTED
// rather than blocking.
//
// The bridge is unbuffered, so an offer parks until http.Server accepts it. If
// the group drains while it is parked, nothing will ever accept — and without
// the closed case the engine goroutine holding that connection would block for
// the life of the process, taking an in-flight token with it.
func Test_chanListener_offer(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// closedFirst closes the bridge before the offer.
		closedFirst bool
		// closeWhileParked closes it while the offer is waiting to land.
		closeWhileParked bool
	}
	tests := []tc{
		{name: "a bridge with an acceptor"},
		{name: "a bridge already drained", closedFirst: true},
		{name: "a bridge that drains while the offer waits", closeWhileParked: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		bridge := newChanListener(nil)
		socket := &fakeSocket{}

		if c.closedFirst {
			if cerr := bridge.Close(); cerr != nil {
				t.Fatalf("Close = %v, want nil", cerr)
			}
			if bridge.offer(socket) {
				t.Fatal("a drained bridge accepted a connection")
			}
			return
		}
		handed := make(chan bool, 1)
		go func() { handed <- bridge.offer(socket) }()

		if c.closeWhileParked {
			//: nothing is accepting, so the offer is parked on the channel.
			if cerr := bridge.Close(); cerr != nil {
				t.Fatalf("Close = %v, want nil", cerr)
			}
			select {
			case landed := <-handed:
				if landed {
					t.Fatal("an offer landed on a bridge that drained under it")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("the offer is still parked after the bridge drained — the " +
					"engine goroutine would hold its in-flight token forever")
			}
			return
		}
		got, err := bridge.Accept()
		if err != nil {
			t.Fatalf("Accept = %v, want nil", err)
		}
		if got != stdnet.Conn(socket) {
			t.Fatalf("Accept returned %v, want the offered socket", got)
		}
		if landed := <-handed; !landed {
			t.Error("offer reported a failed hand-off that Accept received")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
