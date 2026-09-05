// Package server_test — the start, serve and drain lifecycle as a caller drives it.
package server_test

import (
	"context"
	"io"
	stdnet "net"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// TestServer_Start pins the ergonomic trade the declaration API makes, and that
// Start returns only once every port is OPEN.
//
// Group and PacketGroup return no error so the declaration chain stays readable,
// which is only acceptable because Start reports what went wrong. And a caller
// that gets a nil error knows the ports are open — which is what makes a test
// able to dial immediately, and a supervisor able to report readiness honestly.
func TestServer_Start(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// build declares the groups under test.
		build func(*server.Server)
		// wantCode is the refusal, or zero when the server must come up.
		wantCode errs.Code
	}
	tests := []tc{
		{
			name: "a complete group",
			build: func(s *server.Server) {
				s.Group("api", server.Listen("tcp", "127.0.0.1:0")).HandleFunc(noopHandler)
			},
		},
		{
			name: "a mixed server",
			build: func(s *server.Server) {
				s.Group("api", server.Listen("tcp", "127.0.0.1:0")).HandleFunc(noopHandler)
				s.PacketGroup("dns", server.Listen("udp", "127.0.0.1:0")).HandleFunc(noopPacket)
			},
		},
		{
			name: "a duplicate group name",
			build: func(s *server.Server) {
				s.Group("api", server.Listen("tcp", "127.0.0.1:0")).HandleFunc(noopHandler)
				s.Group("api", server.Listen("tcp", "127.0.0.1:0")).HandleFunc(noopHandler)
			},
			wantCode: corenet.CodeGroupDuplicate,
		},
		{
			//: a group with no handler would accept connections and drop them.
			name:     "a group with no handler",
			build:    func(s *server.Server) { s.Group("api", server.Listen("tcp", "127.0.0.1:0")) },
			wantCode: corenet.CodeHandlerMissing,
		},
		{
			//: adoption does not weaken the "a group must listen somewhere" rule.
			name:     "a group with neither an address nor an inherited socket",
			build:    func(s *server.Server) { s.Group("api").HandleFunc(noopHandler) },
			wantCode: corenet.CodeInvalidAddress,
		},
		{
			name: "an unsupported network",
			build: func(s *server.Server) {
				s.Group("api", server.Listen("carrier-pigeon", "nest")).HandleFunc(noopHandler)
			},
			wantCode: corenet.CodeUnsupportedNetwork,
		},
		{
			name: "a datagram group with no handler",
			build: func(s *server.Server) {
				s.PacketGroup("dns", server.Listen("udp", "127.0.0.1:0"))
			},
			wantCode: corenet.CodeHandlerMissing,
		},
		{
			name: "a stream family on a datagram group",
			build: func(s *server.Server) {
				s.PacketGroup("dns", server.Listen("tcp", "127.0.0.1:0")).HandleFunc(noopPacket)
			},
			wantCode: corenet.CodeUnsupportedNetwork,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		c.build(srv)

		err := srv.Start(t.Context())

		if c.wantCode != 0 {
			if !errs.HasCode(err, c.wantCode) {
				t.Fatalf("Start = %v, want code %v", err, c.wantCode)
			}
			return
		}
		if err != nil {
			t.Fatalf("Start = %v, want nil", err)
		}
		//: the ports are open by the time Start returns, so this dial cannot
		//: race the bind.
		for _, listener := range srv.State().Listeners {
			if listener.Network != "tcp" {
				continue
			}
			conn, derr := stdnet.DialTimeout("tcp", listener.Address, 2*time.Second)
			if derr != nil {
				t.Fatalf("Start returned before %s was accepting: %v", listener.Address, derr)
			}
			closeOrFail(t, conn)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestServer_Start_RefusesASecondStart pins that a double Start cannot silently
// bind a second set of listeners on the same ports — which on a platform with
// SO_REUSEPORT would actually succeed, and split incoming traffic between two
// sets of accept loops for the same server.
func TestServer_Start_RefusesASecondStart(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// starts is how many times Start is called after the first.
		starts int
	}
	tests := []tc{
		{name: "a second start", starts: 1},
		{name: "several more starts", starts: 3},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv, _ := startEcho(t)

		for i := range c.starts {
			err := srv.Start(t.Context())
			if !errs.HasCode(err, corenet.CodeAlreadyStarted) {
				t.Fatalf("start %d = %v, want ALREADY_STARTED", i, err)
			}
		}

		//: and the original listeners are untouched.
		if got := len(srv.State().Listeners); got != 1 {
			t.Fatalf("State reports %d listeners after the refused starts, want 1", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestServer_Serve pins the one-call entry point: it starts, blocks until the
// caller's context is cancelled, and then drains — within a budget INDEPENDENT
// of that context.
//
// Goroutine lifecycle: one per case runs Serve, since it blocks by contract. It
// reports on a buffered channel so it cannot block on send, and the case always
// receives from that channel before returning.
//
// The independence is the whole point. Deriving the drain budget from the
// cancelled context would leave shutdown no time at all at the very moment it is
// needed, so a graceful stop would never be graceful.
func TestServer_Serve(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// build declares the groups under test.
		build func(*server.Server)
		// wantCode is the refusal Serve must report, or zero when it must run.
		wantCode errs.Code
	}
	tests := []tc{
		{
			name: "a server that runs and drains",
			build: func(s *server.Server) {
				s.Group("echo", server.Listen("tcp", "127.0.0.1:0")).HandleFunc(echoHandler)
			},
		},
		{
			//: a failed start has already released whatever it bound, so Serve
			//: reports it rather than blocking on a server that is not running.
			name:     "a server that cannot start",
			build:    func(s *server.Server) { s.Group("api").HandleFunc(noopHandler) },
			wantCode: corenet.CodeInvalidAddress,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := server.New(server.WithDrainTimeout(5 * time.Second))
		t.Cleanup(func() { closeOrFail(t, srv) })
		c.build(srv)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		done := make(chan error, 1)
		//: Goroutine lifecycle: one, owned by this case. It reports on a
		//: buffered channel so it cannot block on send, and the case always
		//: receives from that channel before returning.
		go func() { done <- srv.Serve(ctx) }()

		if c.wantCode != 0 {
			select {
			case err := <-done:
				if !errs.HasCode(err, c.wantCode) {
					t.Fatalf("Serve = %v, want code %v", err, c.wantCode)
				}
			case <-time.After(serveDeadline):
				t.Fatal("Serve blocked on a server that never started")
			}
			return
		}
		//: it is serving, which is what "blocks until cancelled" has to mean.
		if !waitFor(t, func() bool { return srv.State().Phase == corenet.PhaseServing }) {
			t.Fatalf("phase = %v, want serving", srv.State().Phase)
		}
		addr := srv.State().Listeners[0].Address
		if got := roundTrip(t, addr, "hello"); got != "hello\n" {
			t.Fatalf("echo = %q, want %q", got, "hello\n")
		}

		cancel()

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Serve = %v, want a clean drain", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Serve did not return after its context was cancelled")
		}
		//: the drain ran on its own budget rather than on the cancelled one.
		if phase := srv.State().Phase; phase != corenet.PhaseStopped {
			t.Fatalf("phase = %v, want stopped", phase)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestServer_Shutdown pins the graceful path: a connection already being served
// runs to completion instead of being cut mid-response.
//
// Goroutine lifecycle: one goroutine runs Shutdown so the case can keep driving
// the connection that is blocking it. It reports on a buffered channel, so it
// cannot block on send, and the case always receives from that channel before
// returning.
func TestServer_Shutdown(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// replies is how many lines the handler writes before returning.
		replies int
	}
	tests := []tc{
		{name: "one in-flight response", replies: 1},
		{name: "several in-flight responses", replies: 4},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		release := make(chan struct{}, 1)
		finished := make(chan struct{})
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		srv.Group("slow", server.Listen("tcp", "127.0.0.1:0")).
			HandleFunc(func(_ context.Context, conn corenet.Conn) error {
				<-release
				for range c.replies {
					if _, err := io.WriteString(conn, "done\n"); err != nil {
						close(finished)
						return err
					}
				}
				close(finished)
				return nil
			})
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		addr := srv.State().Listeners[0].Address

		conn, err := stdnet.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer func() { closeOrFail(t, conn) }()
		if !waitFor(t, func() bool { return srv.State().ActiveConns == 1 }) {
			t.Fatal("the connection was never picked up by a handler")
		}

		done := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			done <- srv.Shutdown(ctx)
		}()
		//: the handler is still running, so the drain must be waiting on it.
		release <- struct{}{}
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Fatal("the handler never finished after being released")
		}

		if serr := <-done; serr != nil {
			t.Fatalf("shutdown reported %v, want a clean drain", serr)
		}
		//: the response the handler wrote before the drain must have reached the
		//: peer, or "graceful" means nothing.
		for range c.replies {
			readLine(t, conn, "done\n")
		}
		if phase := srv.State().Phase; phase != corenet.PhaseStopped {
			t.Fatalf("phase = %v, want stopped", phase)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestServer_Shutdown_ReportsAnExpiredBudget pins that an overrun is reported
// rather than waited out forever.
//
// A handler blocked on something other than its socket cannot be killed, so a
// shutdown that waited would never return — which is worse than one that admits
// it gave up, because nothing above it can tell the two apart.
func TestServer_Shutdown_ReportsAnExpiredBudget(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// budget is how long the drain is given.
		budget time.Duration
	}
	tests := []tc{
		{name: "a short budget", budget: 100 * time.Millisecond},
		{name: "an immediate budget", budget: time.Millisecond},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		stuck := make(chan struct{})
		t.Cleanup(func() { close(stuck) })
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		srv.Group("stuck", server.Listen("tcp", "127.0.0.1:0")).
			HandleFunc(func(_ context.Context, _ corenet.Conn) error {
				<-stuck
				return nil
			})
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		conn, err := stdnet.DialTimeout("tcp", srv.State().Listeners[0].Address, 2*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer func() { closeOrFail(t, conn) }()
		if !waitFor(t, func() bool { return srv.State().ActiveConns == 1 }) {
			t.Fatal("the connection was never picked up by a handler")
		}

		ctx, cancel := context.WithTimeout(t.Context(), c.budget)
		defer cancel()
		serr := srv.Shutdown(ctx)

		if !errs.HasCode(serr, corenet.CodeDrainTimeout) {
			t.Fatalf("Shutdown = %v, want DRAIN_TIMEOUT", serr)
		}
		//: the phase moves anyway: the server is stopped whether or not every
		//: handler agreed to finish.
		if phase := srv.State().Phase; phase != corenet.PhaseStopped {
			t.Fatalf("phase = %v, want stopped", phase)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestServer_Close pins that Close actually RELEASES the port, so a restart on
// the same address is possible — and that it is immediate rather than a drain
// with no budget.
func TestServer_Close(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// requests is how many connections are served before the close.
		requests int
		// closes is how many times Close is called.
		closes int
	}
	tests := []tc{
		{name: "an idle server", closes: 1},
		{name: "after some traffic", requests: 3, closes: 1},
		//: Shutdown and Close can both run, in either order, so a second close
		//: is the expected case rather than a fault.
		{name: "closed twice", closes: 2},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := server.New()
		srv.Group("echo", server.Listen("tcp", "127.0.0.1:0")).HandleFunc(echoHandler)
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		addr := srv.State().Listeners[0].Address
		for range c.requests {
			roundTrip(t, addr, "x")
		}

		for i := range c.closes {
			if err := srv.Close(); err != nil {
				t.Fatalf("close %d = %v, want nil", i, err)
			}
		}

		if phase := srv.State().Phase; phase != corenet.PhaseStopped {
			t.Fatalf("phase = %v, want stopped", phase)
		}
		//: the port is genuinely free, which is what makes a restart on the same
		//: address possible.
		if _, err := stdnet.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
			t.Fatal("the listener still accepted a connection after Close")
		}
		again, err := stdnet.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("%s is still held after Close: %v", addr, err)
		}
		closeOrFail(t, again)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
