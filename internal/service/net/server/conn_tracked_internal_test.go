// Package server — the socket that holds its connection slot until it closes.
package server

import (
	"crypto/tls"
	"errors"
	"io"
	stdnet "net"
	"strings"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// Test_trackedConn_holdSlot pins the two orders a hijacked connection's slot
// can be settled in, and that it is settled exactly ONCE either way.
//
// The hand-over races the handler: the engine hands the slot to the socket on
// the serve goroutine after ServeConn returns, while a handler that hijacks
// and closes at once may already have closed it. A slot handed to a socket
// that will never close again must be returned there and then, or it leaks;
// and a socket closed twice must return it once, or the ceiling admits one
// connection too many for ever after.
//
// The ceiling here has TWO slots and both are claimed first — one for the
// hijacked connection, one for a neighbour — so a double release shows up as a
// count instead of as a receive on an empty semaphore that blocks forever.
//
// MUTATION-CHECKED. A Close that never returns the slot fails "handed over,
// then closed" and "…closed twice" with:
//
//	2 slots held, want 1
//
// a holdSlot that ignores an earlier Close fails "closed, then handed over"
// with the same message, and a Close that does not clear the slot it returned
// fails "…closed twice" with:
//
//	0 slots held, want 1
func Test_trackedConn_holdSlot(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// closeFirst closes the socket before the slot is handed over.
		closeFirst bool
		// hold hands the slot over at all.
		hold bool
		// closes is how many times the socket is closed after the hand-over.
		closes int
		// wantHeld is how many of the two slots are still held at the end.
		wantHeld int
	}
	tests := []tc{
		{name: "handed over, then closed", hold: true, closes: 1, wantHeld: 1},
		//: the handler closed before the engine let go of the connection.
		{name: "closed, then handed over", closeFirst: true, hold: true, wantHeld: 1},
		{name: "handed over, then closed twice", hold: true, closes: 2, wantHeld: 1},
		//: an open hijacked connection keeps counting.
		{name: "handed over and still open", hold: true, wantHeld: 2},
		//: a connection that was never hijacked settles its slot elsewhere.
		{name: "closed without a slot", closes: 1, wantHeld: 2},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		limiter := newConnLimiter(corenet.LimitsValue{MaxConns: 2})
		for range cap(limiter.slots) {
			if !limiter.tryAcquire() {
				t.Fatal("could not claim every slot of an empty ceiling")
			}
		}
		tracked := &trackedConn{Conn: &fakeSocket{}}

		if c.closeFirst {
			closeTracked(t, tracked)
		}
		if c.hold {
			tracked.holdSlot(limiter)
		}
		for range c.closes {
			closeTracked(t, tracked)
		}

		if got := len(limiter.slots); got != c.wantHeld {
			t.Fatalf("%d slots held, want %d", got, c.wantHeld)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_trackedConn_Close pins that the tracker is invisible to whoever closes
// it: the socket underneath is closed every time Close is called, and the
// socket's own verdict comes back unchanged.
func Test_trackedConn_Close(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// closes is how many times the tracker is closed.
		closes int
	}
	tests := []tc{
		{name: "closed once", closes: 1},
		//: a second Close reaches the socket too; only the slot is settled once.
		{name: "closed twice", closes: 2},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		socket := &fakeSocket{}
		tracked := &trackedConn{Conn: socket}

		for range c.closes {
			closeTracked(t, tracked)
		}

		socket.mu.Lock()
		defer socket.mu.Unlock()
		if socket.closes != c.closes {
			t.Fatalf("the socket was closed %d times, want %d", socket.closes, c.closes)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_trackedConn_CloseWrite pins that a plaintext socket keeps its half-close
// behind the tracker. net/http half-closes a connection before closing it after
// a response, so a peer still sending does not have the response truncated by
// a reset; hiding CloseWrite would silently drop that on every ceilinged HTTP
// group.
//
// MUTATION-CHECKED. A CloseWrite that returns nil without forwarding fails the
// TCP case with:
//
//	the peer read 0 bytes and read tcp 127.0.0.1:…->127.0.0.1:…: i/o timeout, want EOF from the half-close
func Test_trackedConn_CloseWrite(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// tcp tracks a real TCP socket, which has a write half of its own.
		tcp bool
	}
	tests := []tc{
		{name: "a TCP socket half-closes", tcp: true},
		//: never a socket the engine accepts, but the forwarding must not fail.
		{name: "a socket without a write half"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if !c.tcp {
			if err := (&trackedConn{Conn: &fakeSocket{}}).CloseWrite(); err != nil {
				t.Fatalf("CloseWrite = %v, want nil", err)
			}
			return
		}
		served, peer := tcpPair(t)
		tracked := &trackedConn{Conn: served}

		if err := tracked.CloseWrite(); err != nil {
			t.Fatalf("CloseWrite = %v, want nil", err)
		}

		//: the peer sees the end of the stream while the socket is still open.
		if n, err := peer.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
			t.Fatalf("the peer read %d bytes and %v, want EOF from the half-close", n, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_trackedConn_ReadFrom pins that the tracker offers the ReadFrom net/http
// looks for on a raw connection — without it, net/http takes its own buffered
// copy instead of the socket's, which is where sendfile and splice live — and
// that every byte arrives through it, whichever copy path the socket has.
//
// What it cannot see is WHICH path ran: io.Copy onto a *net.TCPConn reaches the
// socket's ReadFrom by itself, so the two branches deliver identical bytes.
// The property that matters is the method's presence, which the call pins.
func Test_trackedConn_ReadFrom(t *testing.T) {
	t.Parallel()
	const payload = "the whole payload"
	type tc struct {
		// name describes the case.
		name string
		// tcp tracks a real TCP socket, which has a ReadFrom of its own.
		tcp bool
	}
	tests := []tc{
		{name: "a TCP socket copies through its own ReadFrom", tcp: true},
		{name: "a socket without one falls back to a plain copy"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var (
			socket stdnet.Conn = &fakeSocket{}
			peer   stdnet.Conn
		)
		if c.tcp {
			socket, peer = tcpPair(t)
		}
		tracked := &trackedConn{Conn: socket}

		n, err := tracked.ReadFrom(strings.NewReader(payload))

		if err != nil || n != int64(len(payload)) {
			t.Fatalf("ReadFrom = %d, %v, want %d, nil", n, err, len(payload))
		}
		if peer == nil {
			//: the fake socket accepts every write whole; the count is the proof.
			return
		}
		got := make([]byte, len(payload))
		if _, rerr := io.ReadFull(peer, got); rerr != nil || string(got) != payload {
			t.Fatalf("the peer read %q (%v), want %q", got, rerr, payload)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_hijackedTracker pins where the engine looks for the socket that will
// report a hijacked connection's Close — and that it finds one ONLY for a
// connection that was actually hijacked.
//
// A tracker found for a connection the engine still owns would hand its slot to
// a socket the engine is about to close anyway: harmless, but a second path to
// the same release. One NOT found for a hijacked connection gives the slot back
// at the hijack, which is the defect. The TLS case is the one that is easy to
// miss: the tracker is beneath the *tls.Conn, not the socket itself.
//
// MUTATION-CHECKED. Dropping the NetConn step fails the TLS case with:
//
//	hijackedTracker = 0x0, want 0xc…
func Test_hijackedTracker(t *testing.T) {
	t.Parallel()
	tracker := &trackedConn{Conn: &fakeSocket{}}
	type tc struct {
		// name describes the case.
		name string
		// c is the connection the engine served.
		c corenet.Conn
		// want is the tracker that must be found, or nil.
		want *trackedConn
	}
	tests := []tc{
		{name: "a hijacked plaintext socket", c: &conn{Conn: tracker, hijacked: true}, want: tracker},
		{
			name: "a hijacked TLS socket, tracker underneath",
			c:    &conn{Conn: tls.Server(tracker, &tls.Config{MinVersion: tls.VersionTLS12}), hijacked: true},
			want: tracker,
		},
		//: still the engine's connection; its slot ends with the handler.
		{name: "a socket nobody hijacked", c: &conn{Conn: tracker}},
		//: a group that does not track closes built no tracker.
		{name: "a hijacked socket with no tracker", c: &conn{Conn: &fakeSocket{}, hijacked: true}},
		//: a middleware's own Conn carries no hijack flag.
		{name: "a connection that is not the engine's wrapper", c: &notPooled{Conn: tracker}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := hijackedTracker(c.c); got != c.want {
			t.Fatalf("hijackedTracker = %p, want %p", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// closeTracked closes a tracker whose socket cannot fail to close.
func closeTracked(t *testing.T, tracked *trackedConn) {
	t.Helper()
	if err := tracked.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// tcpPair returns both ends of a real loopback TCP connection, closed when the
// test ends.
func tcpPair(t *testing.T) (served, peer stdnet.Conn) {
	t.Helper()
	ln, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { swallowErr(ln.Close()) }()
	peer, err = stdnet.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	served, err = ln.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	t.Cleanup(func() {
		swallowErr(served.Close())
		swallowErr(peer.Close())
	})
	//: a deadline turns a hang into a failure rather than a stuck suite.
	if derr := peer.SetDeadline(time.Now().Add(slotWait)); derr != nil {
		t.Fatalf("deadline: %v", derr)
	}
	//: the engine's end and the client's end.
	return served, peer
}
