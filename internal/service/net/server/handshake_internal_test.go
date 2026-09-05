// Package server — the bounded TLS handshake.
package server

import (
	"context"
	"crypto/tls"
	"errors"
	stdnet "net"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// tlsPair returns a server-side TLS connection and the raw socket its peer would
// speak on, so a test can decide whether the peer ever sends a ClientHello.
func tlsPair(t *testing.T) (server *tls.Conn, peer stdnet.Conn) {
	t.Helper()
	serverSide, clientSide := stdnet.Pipe()
	t.Cleanup(func() {
		//: both halves are memory-backed, so a close cannot fail meaningfully.
		swallowErr(serverSide.Close())
		swallowErr(clientSide.Close())
	})
	return tls.Server(serverSide, testIdentity(t).ServerConfig()), clientSide
}

// negotiate drives the client half of a TLS handshake.
//
// Goroutine lifecycle: one per case that wants a peer, started by the case and
// ended by the negotiation itself or by the pipe closing at cleanup. It reports
// nothing: the property under test is what the SERVER half does, and a client
// that fails is one of the outcomes.
func negotiate(ctx context.Context, peer stdnet.Conn) {
	client := tls.Client(peer, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec
	//: the server half is what is under test; this end only has to speak.
	swallowErr(client.HandshakeContext(ctx))
}

// Test_handshakeBudget pins that an unconfigured group still BOUNDS its
// handshake.
//
// Read, Write and Idle all treat zero as "no bound", and that is deliberate for
// them: a handler is running and can decide for itself. The handshake is the one
// phase where nothing of the sort is true — it completes before any handler
// exists — so zero here must fall back to the domain default instead. A group
// that configures nothing is the common case, and it was the unbounded one.
func Test_handshakeBudget(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the configuration.
		name string
		// configured is the group's Handshake timeout; zero means unset.
		configured time.Duration
		// want is the bound the negotiation must run under.
		want time.Duration
	}
	tests := []tc{
		{name: "unset falls back to the domain default", configured: 0, want: defaultHandshakeTimeout},
		{name: "negative falls back too", configured: -time.Second, want: defaultHandshakeTimeout},
		{name: "an explicit budget wins", configured: 250 * time.Millisecond, want: 250 * time.Millisecond},
		{name: "a generous explicit budget wins too", configured: time.Hour, want: time.Hour},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := handshakeBudget(corenet.TimeoutsValue{
			Handshake: corenet.DurationValue(c.configured),
		})
		//: an unbounded handshake is the defect; zero must never survive to here.
		if got != c.want {
			t.Fatalf("handshakeBudget(%v) = %v, want %v", c.configured, got, c.want)
		}
		if got <= 0 {
			t.Fatalf("handshakeBudget(%v) = %v — a peer that opens a socket and "+
				"says nothing would hold a goroutine forever", c.configured, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_boundHandshake pins the three things the bound has to get right.
//
// Goroutine lifecycle: each case may start one client goroutine to negotiate
// against, and one more to prove a later read is unbounded. Both end with the
// case, since the pipe is closed at cleanup.
//
// A PLAINTEXT connection has no negotiation to bound, so nothing is installed
// and a plaintext group pays nothing. A STALLED peer is cut off at the budget,
// because tls.NewListener does not negotiate in Accept — the handshake runs on
// the connection's own goroutine, which is the right design, and without a bound
// a peer that opens a socket and says nothing pins a goroutine, an in-flight
// token, a registry entry and a slot under the group's ceiling, for free and
// forever. And the deadline is CLEARED afterwards: it belongs to the
// negotiation, and leaving it in place would silently bound the handler's first
// read by whatever was left of the handshake budget.
func Test_boundHandshake(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// plaintext gives the handler a socket with no negotiation.
		plaintext bool
		// budget is the group's handshake bound.
		budget time.Duration
		// stalled leaves the peer silent, so no ClientHello ever arrives.
		stalled bool
	}
	tests := []tc{
		{name: "a plaintext connection", plaintext: true},
		{name: "a plaintext connection under a budget", plaintext: true, budget: time.Millisecond},
		{name: "a peer that never sends a ClientHello", budget: 150 * time.Millisecond, stalled: true},
		{name: "a peer that negotiates", budget: 5 * time.Second},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		bounds := corenet.TimeoutsValue{Handshake: corenet.DurationValue(c.budget)}

		if c.plaintext {
			socket := &fakeSocket{}
			//: nothing to negotiate, so nothing is installed and the group pays
			//: nothing for a bound it cannot use.
			if err := boundHandshake(t.Context(), &conn{Conn: socket}, bounds); err != nil {
				t.Fatalf("boundHandshake on a plaintext connection = %v, want nil", err)
			}
			if socket.reads()+socket.writes() != 0 {
				t.Errorf("a plaintext connection had %d deadlines installed",
					socket.reads()+socket.writes())
			}
			return
		}

		serverSide, peer := tlsPair(t)
		if !c.stalled {
			//: a real client on the other end, negotiating against our identity.
			go negotiate(t.Context(), peer)
		}

		started := time.Now()
		err := boundHandshake(t.Context(), &conn{Conn: serverSide}, bounds)

		if c.stalled {
			if err == nil {
				t.Fatal("a peer that never sent a ClientHello completed the negotiation")
			}
			//: cut off at the budget rather than held for the life of the process.
			if waited := time.Since(started); waited > c.budget+3*time.Second {
				t.Fatalf("the stalled negotiation ran for %v under a %v budget", waited, c.budget)
			}
			return
		}
		if err != nil {
			t.Fatalf("boundHandshake = %v, want a completed negotiation", err)
		}
		//: the deadline belonged to the negotiation and is gone, so the
		//: handler's first read is bounded by the group's own budgets and not
		//: by whatever was left of this one.
		blocked := make(chan error, 1)
		go func() {
			_, rerr := serverSide.Read(make([]byte, 1))
			blocked <- rerr
		}()
		select {
		case rerr := <-blocked:
			t.Fatalf("a read after the negotiation returned %v immediately — the "+
				"handshake deadline is still installed and silently bounds the handler", rerr)
		case <-time.After(300 * time.Millisecond):
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_handshakeHandler_ServeConn pins the ORDER: the negotiation completes
// before the group's handler sees the connection, and a failed one never
// reaches it at all.
//
// Goroutine lifecycle: one client goroutine per case that negotiates, ended by
// the handshake or by the pipe closing at cleanup.
//
// The wrapper sits INSIDE the connection ceiling on purpose. Negotiating first
// would let a flood of connections each pay for a full handshake before the
// ceiling had a chance to refuse them, which is the opposite of what the ceiling
// is for.
func Test_handshakeHandler_ServeConn(t *testing.T) {
	t.Parallel()
	failure := errors.New("handler failed")

	type tc struct {
		// name describes the case.
		name string
		// stalled leaves the peer silent, so the negotiation never completes.
		stalled bool
		// handlerErr is what the group's handler reports once reached.
		handlerErr error
		// wantReached is whether the handler must run at all.
		wantReached bool
	}
	tests := []tc{
		{name: "a peer that negotiates", wantReached: true},
		{name: "a handler that fails after a good negotiation", handlerErr: failure, wantReached: true},
		//: a connection that never negotiated is not a connection the handler
		//: should ever see; the engine closes it on the way out.
		{name: "a peer that never sends a ClientHello", stalled: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var reached bool
		serverSide, peer := tlsPair(t)
		if !c.stalled {
			go negotiate(t.Context(), peer)
		}
		handler := handshakeHandler{
			next: corenet.ConnHandlerFunc(func(context.Context, corenet.Conn) error {
				reached = true
				return c.handlerErr
			}),
			timeouts: corenet.TimeoutsValue{
				Handshake: corenet.DurationValue(150 * time.Millisecond),
			},
		}

		err := handler.ServeConn(t.Context(), &conn{Conn: serverSide})

		if reached != c.wantReached {
			t.Fatalf("the handler ran = %v, want %v", reached, c.wantReached)
		}
		if !c.wantReached {
			if err == nil {
				t.Fatal("a connection whose negotiation failed was reported as served")
			}
			return
		}
		//: the handler's own outcome travels back unchanged.
		if !errors.Is(err, c.handlerErr) {
			t.Fatalf("ServeConn = %v, want %v", err, c.handlerErr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
