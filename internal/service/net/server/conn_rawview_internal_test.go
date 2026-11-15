// Package server — the view a handler receives (#970): a group that bounds
// neither reads nor writes hands out its socket's syscall.Conn, so the
// standard library's own zero-copy paths reach the descriptor; a bounded or
// layered connection does not.
package server

import (
	"crypto/tls"
	"errors"
	stdnet "net"
	"syscall"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// rawSeen is what a handler found on the connection it received.
type rawSeen struct {
	// raw reports whether the connection implemented syscall.Conn.
	raw bool
	// same reports whether two SyscallConn calls answered one RawConn.
	same bool
	// fd is the descriptor the RawConn's Control saw.
	fd uintptr
	// err is the first failure met.
	err error
}

// seeRaw is a handler body recording what c offers of its socket.
func seeRaw(c corenet.Conn, seen chan<- rawSeen) error {
	var s rawSeen
	sc, ok := c.(syscall.Conn)
	s.raw = ok
	//: the view: its RawConn, asked twice, and the descriptor under it.
	if ok {
		first, err := sc.SyscallConn()
		second, err2 := sc.SyscallConn()
		s.err = errors.Join(err, err2)
		//: both answered.
		if s.err == nil {
			s.same = first == second
			s.err = first.Control(func(fd uintptr) { s.fd = fd })
		}
	}
	seen <- s
	//: the connection ends with the handler.
	return nil
}

// TestConnHandedView pins which connections a handler receives as a rawView:
// a plaintext TCP group with no read, idle nor write bound hands out the
// socket's syscall.Conn, its RawConn asked once and kept, its Control on the
// accepted descriptor; a group bounding any of the three, or serving TLS,
// hands out the conn alone, so its copies keep the bound per step through
// ReadFrom and WriteTo.
func TestConnHandedView(t *testing.T) {
	type tc struct {
		name string
		opts []GroupOption
		tls  bool
		raw  bool
	}
	tests := []tc{
		{name: "no bound", raw: true},
		{name: "WriteTimeout", opts: []GroupOption{WriteTimeout(time.Minute)}},
		{name: "ReadTimeout", opts: []GroupOption{ReadTimeout(time.Minute)}},
		{name: "IdleTimeout", opts: []GroupOption{IdleTimeout(time.Minute)}},
		{name: "TLS", opts: []GroupOption{TLS(testIdentity(t))}, tls: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		seen := make(chan rawSeen, 1)
		addr := serveOneConn(t, func(conn corenet.Conn) error { return seeRaw(conn, seen) }, c.opts...)
		client := dialTimed(t, addr)
		//: the handler runs once the handshake is done.
		if c.tls {
			secured := tls.Client(client, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // a test's self-signed peer.
			if err := secured.HandshakeContext(t.Context()); err != nil {
				t.Fatalf("handshake: %v", err)
			}
		}
		var s rawSeen
		select {
		case s = <-seen:
		case <-time.After(10 * time.Second):
			t.Fatal("the handler never ran")
		}
		swallowErr(client.Close())
		if s.raw != c.raw {
			t.Fatalf("syscall.Conn handed = %v, want %v", s.raw, c.raw)
		}
		if !c.raw {
			return
		}
		if s.err != nil || !s.same || s.fd == 0 {
			t.Fatalf("RawConn: err %v, kept %v, fd %d: want one RawConn on the socket's descriptor", s.err, s.same, s.fd)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestConnRawViewReleased pins a view kept past its handler: once the engine
// released the connection, SyscallConn answers what a closed socket's own
// does through the standard library, net.ErrClosed, and never the next
// connection's descriptor.
func TestConnRawViewReleased(t *testing.T) {
	c := acquiredOver(t, &fakeSocket{}, corenet.TimeoutsValue{})
	c.reset()
	if _, err := (*rawView)(c).SyscallConn(); !errors.Is(err, stdnet.ErrClosed) {
		t.Fatalf("SyscallConn on a released view: %v, want net.ErrClosed", err)
	}
}
