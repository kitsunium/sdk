package server_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	stdnet "net"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/server"
)

// TestTheShortestUsefulServer is the API's own acceptance test.
//
// The requirement behind this domain was to be able to plug a handler in very
// easily. That is not measurable by reading the godoc, so it is measured here:
// the body below is the complete, unabridged code needed to run a working TCP
// server, and it is five statements. If a future change makes this example grow
// — an extra construction step, an error to thread, a type to declare — the
// API has regressed in the one dimension that motivated it, whatever else it
// gained.
func TestTheShortestUsefulServer(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	srv := server.New()
	srv.Group("echo", server.Listen("tcp", "127.0.0.1:0")).
		HandleFunc(func(_ context.Context, c server.Conn) error {
			_, err := io.Copy(c, c)
			return err
		})
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { closeOrFail(t, srv) }()

	//: everything below is the assertion, not part of the example.
	if got := echo(t, srv.State().Listeners[0].Address, "hello"); got != "hello\n" {
		t.Fatalf("echo = %q, want %q", got, "hello\n")
	}
}

// echo dials the server, sends one line and returns the reply.
func echo(t *testing.T, addr, line string) string {
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
	reply, rerr := bufio.NewReader(c).ReadString('\n')
	if rerr != nil {
		t.Fatalf("read: %v", rerr)
	}
	return reply
}

// TestServeStartsBlocksAndDrains pins the one-call entry point: a caller with
// nothing else to do on the main goroutine gets startup, blocking and graceful
// shutdown without assembling them.
//
// Goroutine lifecycle: one goroutine runs Serve, which blocks until the context
// is cancelled. It reports on a buffered channel so it cannot block on send,
// and the test always receives from it before returning.
func TestServeStartsBlocksAndDrains(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	srv := server.New(server.WithDrainTimeout(2 * time.Second))
	srv.Group("echo", server.Listen("tcp", "127.0.0.1:0")).
		HandleFunc(func(_ context.Context, c server.Conn) error {
			_, err := io.Copy(c, c)
			return err
		})

	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()

	//: Serve binds before it blocks, so the port is open once the phase moves.
	waitFor(t, func() bool { return srv.State().Phase == server.PhaseServing })
	if got := echo(t, srv.State().Listeners[0].Address, "ping"); got != "ping\n" {
		t.Fatalf("echo = %q", got)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve returned %v, want a clean drain", err)
	}
	if phase := srv.State().Phase; phase != server.PhaseStopped {
		t.Fatalf("phase = %v, want stopped", phase)
	}
}

// TestMiddlewaresWrapOutermostFirst pins the ordering through the public API,
// since a middleware chain that runs in an unexpected order is a class of bug
// that only shows up under load.
func TestMiddlewaresWrapOutermostFirst(t *testing.T) {
	t.Parallel()
	order := make(chan string, 8)
	tag := func(name string) server.Middleware {
		return func(next server.Handler) server.Handler {
			return server.HandlerFunc(func(ctx context.Context, c server.Conn) error {
				order <- name
				return next.ServeConn(ctx, c)
			})
		}
	}
	srv := server.New()
	srv.Group("api", server.Listen("tcp", "127.0.0.1:0")).
		Use(tag("outer"), tag("inner")).
		HandleFunc(func(_ context.Context, c server.Conn) error {
			order <- "handler"
			_, err := io.WriteString(c, "ok\n")
			return err
		})
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { closeOrFail(t, srv) }()

	if got := echo(t, srv.State().Listeners[0].Address, "go"); got != "ok\n" {
		t.Fatalf("reply = %q", got)
	}
	for _, want := range []string{"outer", "inner", "handler"} {
		select {
		case got := <-order:
			if got != want {
				t.Fatalf("ran %q where %q was expected", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("never reached %q", want)
		}
	}
}

// TestUnixSocketUsesTheSameEngine pins the "same thing everywhere" promise: a
// Unix socket is one option change away from a TCP port, with no other
// difference in the handler or the wiring.
func TestUnixSocketUsesTheSameEngine(t *testing.T) {
	t.Parallel()
	sock := t.TempDir() + "/api.sock"
	srv := server.New()
	srv.Group("api", server.Listen("unix", sock)).
		HandleFunc(func(_ context.Context, c server.Conn) error {
			_, err := io.WriteString(c, "unix\n")
			return err
		})
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { closeOrFail(t, srv) }()

	c, err := stdnet.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { closeOrFail(t, c) }()
	reply, rerr := bufio.NewReader(c).ReadString('\n')
	if rerr != nil {
		t.Fatalf("read: %v", rerr)
	}
	if reply != "unix\n" {
		t.Fatalf("reply = %q, want %q", reply, "unix\n")
	}
}

// TestOneGroupServesTwoFamilies pins the reason groups exist: one handler
// answering on both a TCP port and a Unix socket, wired once.
func TestOneGroupServesTwoFamilies(t *testing.T) {
	t.Parallel()
	sock := t.TempDir() + "/dual.sock"
	srv := server.New()
	srv.Group("dual", server.Listen("tcp", "127.0.0.1:0"), server.Listen("unix", sock)).
		HandleFunc(func(_ context.Context, c server.Conn) error {
			_, err := io.WriteString(c, c.Group()+"\n")
			return err
		})
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { closeOrFail(t, srv) }()

	state := srv.State()
	if len(state.Listeners) != 2 {
		t.Fatalf("listeners = %d, want 2", len(state.Listeners))
	}
	for _, listener := range state.Listeners {
		if listener.Group != "dual" {
			t.Fatalf("listener group = %q, want \"dual\"", listener.Group)
		}
	}
}

// TestSentinelsAreMatchableThroughTheFacade pins that a consumer can branch on
// a failure without importing internal/*, which Go's firewall forbids anyway.
func TestSentinelsAreMatchableThroughTheFacade(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		build func(*server.Server)
		want  error
	}{
		{
			name:  "no handler",
			build: func(s *server.Server) { s.Group("api", server.Listen("tcp", "127.0.0.1:0")) },
			want:  server.HandlerMissing,
		},
		{
			name: "duplicate group",
			build: func(s *server.Server) {
				s.Group("api", server.Listen("tcp", "127.0.0.1:0")).HandleFunc(noop)
				s.Group("api", server.Listen("tcp", "127.0.0.1:0")).HandleFunc(noop)
			},
			want: server.GroupDuplicate,
		},
		{
			name:  "unsupported network",
			build: func(s *server.Server) { s.Group("api", server.Listen("smoke", "signal")).HandleFunc(noop) },
			want:  server.UnsupportedNetwork,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := server.New()
			tc.build(srv)
			if err := srv.Start(context.Background()); !errors.Is(err, tc.want) {
				t.Fatalf("errors.Is(err, %v) = false, got %v", tc.want, err)
			}
		})
	}
}

// noop is a handler that does nothing, for declaration-error tests.
func noop(context.Context, server.Conn) error {
	return nil
}

// waitFor polls cond until it holds or the test times out. Polling beats a
// fixed sleep: faster in the common case and far less flaky.
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
