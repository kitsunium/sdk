// Package server — the listener that hands out close-tracking sockets.
package server

import (
	"crypto/tls"
	"errors"
	stdnet "net"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// Test_trackedListener_Accept pins that listen puts the tracker where the
// engine will look for it: IS the accepted socket on a plaintext listener, and
// UNDER the *tls.Conn on a TLS one — never above it, which would hide the
// concrete *tls.Conn net/http type-asserts on and serve every HTTPS request as
// plaintext.
//
// It also pins that a closed listener's error travels verbatim: the accept loop
// recognises net.ErrClosed as the one signal to stop, and a wrapper that
// relabelled it would turn every Close into an accept loop spinning on errors.
//
// MUTATION-CHECKED. Wrapping the TLS listener instead of the one beneath it
// fails the TLS case with:
//
//	a TLS listener accepted a *server.trackedConn, want the *tls.Conn net/http asserts on
func Test_trackedListener_Accept(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// secured makes the listener a TLS one.
		secured bool
	}
	tests := []tc{
		{name: "a plaintext listener"},
		{name: "a TLS listener", secured: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var id corenet.IdentityValue
		if c.secured {
			id = testIdentity(t)
		}
		ln, err := listen(t.Context(), corenet.AddressValue{Network: "tcp", Addr: "127.0.0.1:0"}, id, false, true)
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		//: a plain dial is enough: tls.NewListener accepts before any handshake.
		peer, derr := stdnet.Dial("tcp", ln.Addr().String())
		if derr != nil {
			t.Fatalf("dial: %v", derr)
		}
		defer func() { swallowErr(peer.Close()) }()

		accepted, aerr := ln.Accept()
		if aerr != nil {
			t.Fatalf("accept: %v", aerr)
		}
		defer func() { swallowErr(accepted.Close()) }()

		socket := accepted
		if c.secured {
			secured, isTLS := accepted.(*tls.Conn)
			if !isTLS {
				t.Fatalf("a TLS listener accepted a %T, want the *tls.Conn net/http asserts on", accepted)
			}
			socket = secured.NetConn()
		}
		if _, tracked := socket.(*trackedConn); !tracked {
			t.Fatalf("the tracker is not where the engine looks for it: found a %T", socket)
		}

		if cerr := ln.Close(); cerr != nil {
			t.Fatalf("close: %v", cerr)
		}
		if _, lerr := ln.Accept(); !errors.Is(lerr, stdnet.ErrClosed) {
			t.Fatalf("Accept on a closed listener = %v, want net.ErrClosed verbatim", lerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
