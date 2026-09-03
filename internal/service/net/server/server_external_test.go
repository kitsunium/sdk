package server_test

import (
	"bufio"
	"context"
	"io"
	stdnet "net"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// startEcho starts an echo server on an ephemeral port and returns it with the
// address it actually bound.
func startEcho(t *testing.T) (srv *server.Server, addr string) {
	t.Helper()
	srv = server.New()
	srv.Group("echo", server.Listen("tcp", "127.0.0.1:0")).
		HandleFunc(func(_ context.Context, c corenet.Conn) error {
			_, err := io.Copy(c, c)
			return err
		})
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })
	state := srv.State()
	if len(state.Listeners) != 1 {
		t.Fatalf("listeners = %d, want 1", len(state.Listeners))
	}
	return srv, state.Listeners[0].Address
}

// roundTrip sends one line and reads the echoed reply.
func roundTrip(t *testing.T, addr, line string) string {
	t.Helper()
	c, err := stdnet.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() {
		if cerr := c.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	}()
	if _, werr := io.WriteString(c, line+"\n"); werr != nil {
		t.Fatalf("write: %v", werr)
	}
	got, rerr := bufio.NewReader(c).ReadString('\n')
	if rerr != nil {
		t.Fatalf("read: %v", rerr)
	}
	return got
}

// TestServerEchoesOverTCP is the end-to-end proof that the engine actually
// serves: a real listener, a real dial, a real byte round trip.
func TestServerEchoesOverTCP(t *testing.T) {
	t.Parallel()
	_, addr := startEcho(t)
	if got := roundTrip(t, addr, "hello"); got != "hello\n" {
		t.Fatalf("echo = %q, want %q", got, "hello\n")
	}
}

// TestStateReportsTheBoundAddress pins that State reports the address the
// kernel actually chose, not the ":0" that was requested. A caller cannot dial
// what it asked for; it has to dial what it got.
func TestStateReportsTheBoundAddress(t *testing.T) {
	t.Parallel()
	srv, addr := startEcho(t)
	state := srv.State()
	if state.Phase != corenet.PhaseServing {
		t.Fatalf("phase = %v, want serving", state.Phase)
	}
	if state.Listeners[0].Address != addr || addr == "127.0.0.1:0" {
		t.Fatalf("address = %q, want the kernel-assigned port", state.Listeners[0].Address)
	}
	if state.Degraded() {
		t.Fatal("a plain TCP listener reported degradation")
	}
}

// TestCountersTrackConnections pins that State's counters move, since they are
// what an operator reads to tell a busy server from a stuck one.
func TestCountersTrackConnections(t *testing.T) {
	t.Parallel()
	srv, addr := startEcho(t)
	for range 3 {
		roundTrip(t, addr, "x")
	}
	waitFor(t, func() bool { return srv.State().TotalConns == 3 })
	waitFor(t, func() bool { return srv.State().ActiveConns == 0 })
}

// TestStartRefusesASecondStart pins that a double Start cannot silently bind a
// second set of listeners on the same ports.
func TestStartRefusesASecondStart(t *testing.T) {
	t.Parallel()
	srv, _ := startEcho(t)
	if err := srv.Start(context.Background()); !errs.HasCode(err, corenet.CodeAlreadyStarted) {
		t.Fatalf("expected ALREADY_STARTED, got %v", err)
	}
}

// TestDeclarationErrorsSurfaceAtStart pins the ergonomic trade: Group returns no
// error so the declaration chain stays readable, which is only acceptable
// because Start reports what went wrong.
func TestDeclarationErrorsSurfaceAtStart(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		build func(*server.Server)
		want  errs.Code
	}{
		{
			name: "duplicate group name",
			build: func(s *server.Server) {
				s.Group("api", server.Listen("tcp", "127.0.0.1:0")).HandleFunc(noopHandler)
				s.Group("api", server.Listen("tcp", "127.0.0.1:0")).HandleFunc(noopHandler)
			},
			want: corenet.CodeGroupDuplicate,
		},
		{
			name: "group with no handler",
			build: func(s *server.Server) {
				s.Group("api", server.Listen("tcp", "127.0.0.1:0"))
			},
			want: corenet.CodeHandlerMissing,
		},
		{
			name: "group with no address",
			build: func(s *server.Server) {
				s.Group("api").HandleFunc(noopHandler)
			},
			want: corenet.CodeInvalidAddress,
		},
		{
			name: "unsupported network",
			build: func(s *server.Server) {
				s.Group("api", server.Listen("carrier-pigeon", "nest")).HandleFunc(noopHandler)
			},
			want: corenet.CodeUnsupportedNetwork,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := server.New()
			tc.build(srv)
			err := srv.Start(context.Background())
			if !errs.HasCode(err, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, err)
			}
		})
	}
}

// TestShutdownDrainsInFlightWork pins the graceful path: a connection already
// being served runs to completion instead of being cut mid-response.
//
// Goroutine lifecycle: one goroutine runs Shutdown so the test can keep driving
// the connection that is blocking it. It reports on a buffered channel, so it
// cannot block on send, and the test always receives from that channel before
// returning.
func TestShutdownDrainsInFlightWork(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	finished := make(chan struct{})
	srv := server.New()
	srv.Group("slow", server.Listen("tcp", "127.0.0.1:0")).
		HandleFunc(func(_ context.Context, c corenet.Conn) error {
			<-release
			_, err := io.WriteString(c, "done\n")
			close(finished)
			return err
		})
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	addr := srv.State().Listeners[0].Address

	c, err := stdnet.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { closeOrFail(t, c) }()
	waitFor(t, func() bool { return srv.State().ActiveConns == 1 })

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done <- srv.Shutdown(ctx)
	}()
	//: the handler is still running, so the drain must be waiting on it.
	close(release)
	<-finished
	if serr := <-done; serr != nil {
		t.Fatalf("shutdown reported %v, want a clean drain", serr)
	}
	if phase := srv.State().Phase; phase != corenet.PhaseStopped {
		t.Fatalf("phase = %v, want stopped", phase)
	}
}

// TestShutdownReportsAnExpiredBudget pins that an overrun is reported rather
// than waited out forever — a shutdown that never returns is worse than one
// that admits it gave up.
func TestShutdownReportsAnExpiredBudget(t *testing.T) {
	t.Parallel()
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	srv := server.New()
	srv.Group("stuck", server.Listen("tcp", "127.0.0.1:0")).
		HandleFunc(func(_ context.Context, _ corenet.Conn) error {
			<-stuck
			return nil
		})
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	addr := srv.State().Listeners[0].Address
	c, err := stdnet.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { closeOrFail(t, c) }()
	waitFor(t, func() bool { return srv.State().ActiveConns == 1 })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if serr := srv.Shutdown(ctx); !errs.HasCode(serr, corenet.CodeDrainTimeout) {
		t.Fatalf("expected DRAIN_TIMEOUT, got %v", serr)
	}
}

// TestStoppedServerRefusesNewConnections pins that Close actually releases the
// port, so a restart on the same address is possible.
func TestStoppedServerRefusesNewConnections(t *testing.T) {
	t.Parallel()
	srv, addr := startEcho(t)
	if err := srv.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := stdnet.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
		t.Fatal("the listener still accepted a connection after Close")
	}
}

// TestPanickingHandlerClosesOnlyItsConnection pins the containment rule: one
// malformed peer must never be able to take the process down.
func TestPanickingHandlerClosesOnlyItsConnection(t *testing.T) {
	t.Parallel()
	srv := server.New()
	srv.Group("boom", server.Listen("tcp", "127.0.0.1:0")).
		HandleFunc(func(_ context.Context, c corenet.Conn) error {
			//: read one byte so the client knows the handler ran.
			buf := make([]byte, 1)
			swallowReadErr(c, buf)
			panic("handler exploded")
		})
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })
	addr := srv.State().Listeners[0].Address

	first, err := stdnet.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, werr := io.WriteString(first, "x"); werr != nil {
		t.Fatalf("write: %v", werr)
	}
	closeOrFail(t, first)

	//: the server must still be accepting after the panic.
	waitFor(t, func() bool { return srv.State().ActiveConns == 0 })
	second, err := stdnet.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("the server stopped accepting after a handler panic: %v", err)
	}
	if cerr := second.Close(); cerr != nil {
		t.Errorf("close: %v", cerr)
	}
}

// noopHandler is a handler that does nothing, for declaration-error tests.
func noopHandler(context.Context, corenet.Conn) error {
	return nil
}

// waitFor polls cond until it holds or the test times out. Polling beats a
// fixed sleep: it is both faster in the common case and far less flaky.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition never became true within the deadline")
}

// closeOrFail closes c and reports a close error rather than discarding it: a
// failing close on a server socket usually means the connection was already
// broken, which is exactly what a test needs to know.
func closeOrFail(t *testing.T, c io.Closer) {
	t.Helper()
	//: a close failure is reported, never swallowed.
	if err := c.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

// swallowReadErr reads one byte purely to prove the handler ran; the outcome is
// irrelevant because the handler panics immediately afterwards.
func swallowReadErr(c corenet.Conn, buf []byte) {
	//: the read exists for its side effect, not its result.
	if _, err := c.Read(buf); err != nil {
		return
	}
}
