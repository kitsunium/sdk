// Package server_test — the functional options, observed through what they change.
package server_test

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	stdnet "net"
	"os"
	"os/exec"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

const (
	// readBudget is the per-operation bound the timeout tests configure. It is
	// short so the tests are quick, and comfortably longer than a loopback
	// round trip.
	readBudget time.Duration = 300 * time.Millisecond
	// childAddrEnv marks the re-executed half of the adoption test and carries
	// the address the inherited socket is bound to.
	childAddrEnv string = "KITSUNIUM_ADOPT_CHILD_ADDR"
)

// TestMain runs the ADOPTING half of the socket-activation test when the parent
// marks it, and the ordinary suite otherwise.
//
// Adoption cannot be staged in-process: sd_listen_fds(3) reads from descriptor
// 3, and a Go test binary already holds that descriptor. The child therefore has
// to be a real exec — and doing it here rather than as a test function is what
// keeps the suite free of a test that skips itself on every ordinary run.
func TestMain(m *testing.M) {
	//: the marker is only ever set by runAdoptChild below.
	if addr := os.Getenv(childAddrEnv); addr != "" {
		os.Exit(serveInheritedSocket(addr))
	}
	os.Exit(m.Run())
}

// serveInheritedSocket is the child half: it adopts the socket it was handed at
// fd 3 and proves it serves traffic on it. It returns the process exit code.
func serveInheritedSocket(addr string) int {
	srv := server.New()
	srv.Group("api", server.Adopt("api")).HandleFunc(echoHandler)
	if err := srv.Start(context.Background()); err != nil {
		return reportChild("adopting an inherited socket failed: %v", err)
	}
	defer func() {
		//: the child is about to exit either way, so a close failure is worth
		//: printing but cannot change the outcome already decided above.
		if cerr := srv.Close(); cerr != nil {
			reportChild("close: %v", cerr)
		}
	}()

	state := srv.State()
	if len(state.Listeners) != 1 {
		return reportChild("listeners = %d, want 1", len(state.Listeners))
	}
	//: the distinction matters to an operator: an adopted socket survived a
	//: restart, a freshly bound one did not.
	if !state.Listeners[0].Adopted {
		return reportChild("an inherited socket was not reported as adopted")
	}
	//: a different address would mean the engine bound its own socket instead
	//: of taking over the one it was handed — the silent fallback we forbid.
	if state.Listeners[0].Address != addr {
		return reportChild("adopted address = %q, want the inherited %q",
			state.Listeners[0].Address, addr)
	}
	if err := echoInherited(addr, "inherited"); err != nil {
		return reportChild("the adopted socket did not serve: %v", err)
	}
	return 0
}

// reportChild prints the child's failure and yields a non-zero exit code.
func reportChild(format string, args ...any) int {
	//: stderr is what CombinedOutput hands back to the parent's assertion; the
	//: child has no *testing.T to report through.
	if _, err := fmt.Fprintf(os.Stderr, "adopt child: "+format+"\n", args...); err != nil {
		return 2
	}
	return 1
}

// echoOnce dials addr, sends one line and checks it comes back.
func echoInherited(addr, line string) error {
	conn, err := stdnet.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return err
	}
	defer func() {
		//: the caller's own result is what decides the child's exit code, so a
		//: close failure is reported rather than allowed to mask it.
		if cerr := conn.Close(); cerr != nil {
			reportChild("close: %v", cerr)
		}
	}()
	if _, werr := io.WriteString(conn, line+"\n"); werr != nil {
		return werr
	}
	if derr := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); derr != nil {
		return derr
	}
	buf := make([]byte, len(line)+1)
	if _, rerr := io.ReadFull(conn, buf); rerr != nil {
		return rerr
	}
	echoed := string(buf)
	//: the bytes must come back unchanged, or the socket is not the one we
	//: think it is.
	if echoed != line+"\n" {
		return errors.New("echo mismatch: " + echoed)
	}
	return nil
}

// TestWithDrainTimeout pins that the budget Serve drains within is the one the
// caller configured, and that it is INDEPENDENT of the cancelled context.
//
// Goroutine lifecycle: one per case runs Serve, since it blocks by contract. It
// reports on a buffered channel so it cannot block on send, and the case always
// receives from that channel before returning.
//
// Deriving it from the cancelled context would leave shutdown no time at all at
// the very moment it is needed, so a graceful stop would never be graceful.
func TestWithDrainTimeout(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// budget is the configured drain timeout.
		budget time.Duration
		// stuck holds a handler open past the budget.
		stuck bool
		// wantCode is the outcome Serve must report.
		wantCode errs.Code
	}
	tests := []tc{
		{name: "a generous budget with nothing in flight", budget: 5 * time.Second},
		{name: "a short budget with nothing in flight", budget: 50 * time.Millisecond},
		//: a handler blocked on something other than its socket cannot be
		//: killed, so the budget has to end the wait and say so.
		{name: "a stuck handler past the budget", budget: 100 * time.Millisecond, stuck: true, wantCode: corenet.CodeDrainTimeout},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		hold := make(chan struct{})
		t.Cleanup(func() { close(hold) })
		srv := server.New(server.WithDrainTimeout(c.budget))
		t.Cleanup(func() { closeOrFail(t, srv) })
		srv.Group("api", server.Listen("tcp", "127.0.0.1:0")).
			HandleFunc(func(_ context.Context, _ corenet.Conn) error {
				<-hold
				return nil
			})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		done := make(chan error, 1)
		//: Goroutine lifecycle: one, owned by this case. It reports on a
		//: buffered channel and the case always receives before returning.
		go func() { done <- srv.Serve(ctx) }()
		if !waitFor(t, func() bool { return srv.State().Phase == corenet.PhaseServing }) {
			t.Fatal("the server never reached the serving phase")
		}
		if c.stuck {
			conn, derr := stdnet.DialTimeout("tcp", srv.State().Listeners[0].Address, 2*time.Second)
			if derr != nil {
				t.Fatalf("dial: %v", derr)
			}
			defer closeOrFail(t, conn)
			if !waitFor(t, func() bool { return srv.State().ActiveConns == 1 }) {
				t.Fatal("the connection was never picked up by a handler")
			}
		}

		started := time.Now()
		cancel()

		select {
		case err := <-done:
			if c.wantCode == 0 {
				if err != nil {
					t.Fatalf("Serve = %v, want a clean drain", err)
				}
				return
			}
			if !errs.HasCode(err, c.wantCode) {
				t.Fatalf("Serve = %v, want code %v", err, c.wantCode)
			}
			//: it gave up at roughly the configured budget rather than waiting
			//: out the stuck handler.
			if waited := time.Since(started); waited > c.budget+5*time.Second {
				t.Errorf("the drain took %v under a %v budget", waited, c.budget)
			}
		case <-time.After(15 * time.Second):
			t.Fatalf("Serve never returned under a %v drain budget", c.budget)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestListen pins that the option is variadic BY REPETITION: calling it twice
// binds two addresses to the same handler, which is how one service reaches both
// a TCP port and a Unix socket without the caller assembling the wiring twice.
func TestListen(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// tcpAddrs is how many TCP addresses the group binds.
		tcpAddrs int
		// unixSockets is how many Unix sockets it binds as well.
		unixSockets int
	}
	tests := []tc{
		{name: "one TCP address", tcpAddrs: 1},
		{name: "two TCP addresses", tcpAddrs: 2},
		{name: "a TCP port and a Unix socket", tcpAddrs: 1, unixSockets: 1},
		{name: "only a Unix socket", unixSockets: 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		opts := make([]server.GroupOption, 0, c.tcpAddrs+c.unixSockets)
		for range c.tcpAddrs {
			opts = append(opts, server.Listen("tcp", "127.0.0.1:0"))
		}
		dir := t.TempDir()
		for i := range c.unixSockets {
			opts = append(opts, server.Listen("unix", dir+"/sock"+string(rune('a'+i))))
		}
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		srv.Group("echo", opts...).HandleFunc(echoHandler)

		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}

		state := srv.State()
		//: one row per address, in declaration order.
		if len(state.Listeners) != c.tcpAddrs+c.unixSockets {
			t.Fatalf("State reports %d listeners, want %d",
				len(state.Listeners), c.tcpAddrs+c.unixSockets)
		}
		//: every address serves the same handler, which is the point.
		for i, listener := range state.Listeners {
			conn, err := stdnet.DialTimeout(listener.Network, listener.Address, 2*time.Second)
			if err != nil {
				t.Fatalf("dial listener %d (%s %s): %v", i, listener.Network, listener.Address, err)
			}
			if _, werr := io.WriteString(conn, "hello\n"); werr != nil {
				t.Fatalf("write to listener %d: %v", i, werr)
			}
			readLine(t, conn, "hello\n")
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

// TestTLS pins that one option covers both TLS and mutual TLS, so the two cannot
// drift apart in configuration — and that a group carrying an identity really
// does negotiate rather than serving plaintext under a secure-looking name.
func TestTLS(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// plaintextPeer connects without negotiating.
		plaintextPeer bool
	}
	tests := []tc{
		{name: "a peer that negotiates"},
		//: a plaintext peer must not be served; the listener speaks TLS.
		{name: "a peer that sends plaintext", plaintextPeer: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		srv.Group("api",
			server.Listen("tcp", "127.0.0.1:0"),
			server.TLS(selfSignedIdentity(t)),
			server.HandshakeTimeout(readBudget),
		).HandleFunc(echoHandler)
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		addr := srv.State().Listeners[0].Address

		if c.plaintextPeer {
			conn, err := stdnet.DialTimeout("tcp", addr, 2*time.Second)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer closeOrFail(t, conn)
			if _, werr := io.WriteString(conn, "hello\n"); werr != nil {
				t.Fatalf("write: %v", werr)
			}
			if derr := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); derr != nil {
				t.Fatalf("set deadline: %v", derr)
			}
			//: the listener answers a TLS alert or closes; either way it must
			//: not echo the plaintext back.
			buf := make([]byte, 6)
			if _, rerr := io.ReadFull(conn, buf); rerr == nil && string(buf) == "hello\n" {
				t.Fatal("a TLS listener echoed plaintext — the identity is not in effect")
			}
			return
		}
		conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec
		if err != nil {
			t.Fatalf("tls dial: %v", err)
		}
		defer closeOrFail(t, conn)
		if _, werr := io.WriteString(conn, "hello\n"); werr != nil {
			t.Fatalf("write: %v", werr)
		}
		readLine(t, conn, "hello\n")
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestReadTimeout pins the difference between the two readings of the option,
// which is the whole reason it exists.
//
// A deadline installed once when the connection is accepted is a budget for the
// connection's entire life: a peer that keeps sending, slowly, stays inside it
// until it expires and is then cut off mid-conversation even though it was never
// idle. A per-read bound is the documented promise — each read gets the budget
// afresh — so a peer that answers within the budget every time is never cut off,
// however long the conversation runs. And making it per-read must not make it
// toothless: a peer that says nothing at all is still cut off.
func TestReadTimeout(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// exchanges is how many round trips the peer drives, each inside the
		// budget but adding up past it.
		exchanges int
		// silent leaves the peer saying nothing at all.
		silent bool
	}
	tests := []tc{
		{name: "a slow but active peer", exchanges: 4},
		{name: "a longer conversation", exchanges: 8},
		{name: "a peer that says nothing", silent: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		failures := make(chan error, 1)
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		srv.Group("echo",
			server.Listen("tcp", "127.0.0.1:0"),
			server.ReadTimeout(readBudget),
		).HandleFunc(func(_ context.Context, conn corenet.Conn) error {
			buf := make([]byte, 1)
			//: read once per exchange; every read must succeed.
			for range max(c.exchanges, 1) {
				if _, err := io.ReadFull(conn, buf); err != nil {
					failures <- err
					return err
				}
				if _, err := conn.Write(buf); err != nil {
					failures <- err
					return err
				}
			}
			close(failures)
			return nil
		})
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}

		conn, derr := stdnet.DialTimeout("tcp", srv.State().Listeners[0].Address, 2*time.Second)
		if derr != nil {
			t.Fatalf("dial: %v", derr)
		}
		defer closeOrFail(t, conn)

		if c.silent {
			select {
			case err := <-failures:
				//: a silent peer must trip the deadline, not linger.
				if err == nil {
					t.Fatal("a peer that sent nothing was never cut off")
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("a peer that sent nothing was still connected long after its %v budget",
					readBudget)
			}
			return
		}
		//: each gap is inside the budget, but they add up past it — which is
		//: exactly what separates the two readings.
		gap := readBudget / 2
		reply := make([]byte, 1)
		for i := range c.exchanges {
			time.Sleep(gap)
			if _, err := conn.Write([]byte{byte('a' + i%26)}); err != nil {
				t.Fatalf("write %d: %v", i, err)
			}
			if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatalf("set deadline: %v", err)
			}
			if _, err := io.ReadFull(conn, reply); err != nil {
				t.Fatalf("exchange %d was cut off after %v of a %v per-read budget — "+
					"the deadline is bounding the connection's life, not each read: %v",
					i, time.Duration(i+1)*gap, readBudget, err)
			}
		}
		//: the handler must have completed every exchange without a timeout.
		if err, failed := <-failures; failed {
			t.Fatalf("handler failed: %v", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestWriteTimeout pins that a peer which stops READING cannot pin a handler
// forever.
//
// It is the mirror of the read bound and it is not covered by it: a peer that
// opens a connection, sends a request and then never reads the answer leaves the
// handler blocked in Write once the socket buffers fill, with no read in flight
// for the read budget to bound.
func TestWriteTimeout(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// budget is the configured write bound; zero leaves writes unbounded.
		budget time.Duration
		// payload is how many bytes the handler tries to write.
		payload int
	}
	tests := []tc{
		//: far more than any socket buffer, so the write genuinely blocks.
		{name: "a bounded write to a peer that never reads", budget: readBudget, payload: 8 << 20},
		{name: "a tighter bound", budget: 100 * time.Millisecond, payload: 8 << 20},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		outcome := make(chan error, 1)
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		srv.Group("firehose",
			server.Listen("tcp", "127.0.0.1:0"),
			server.WriteTimeout(c.budget),
		).HandleFunc(func(_ context.Context, conn corenet.Conn) error {
			_, err := conn.Write(make([]byte, c.payload))
			outcome <- err
			return err
		})
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}

		//: connect and then never read, which is what fills the buffers.
		conn, derr := stdnet.DialTimeout("tcp", srv.State().Listeners[0].Address, 2*time.Second)
		if derr != nil {
			t.Fatalf("dial: %v", derr)
		}
		defer closeOrFail(t, conn)

		select {
		case err := <-outcome:
			//: the bound is what ends it; without one the handler would still
			//: be blocked when the test times out.
			if err == nil {
				t.Fatalf("a %d-byte write to a peer that never read completed — "+
					"the write bound is not in effect", c.payload)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("the handler is still blocked in Write long after its %v budget", c.budget)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestIdleTimeout pins the bound that applies when a peer is neither reading nor
// writing.
//
// It combines with the read budget rather than replacing it: a read may not run
// longer than the read timeout, and it may not leave the connection silent past
// the idle one, so the tighter of the two is the honest bound. Installing them
// one after the other — as an earlier version did — simply overwrote the first.
func TestIdleTimeout(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// idle is the configured idle bound.
		idle time.Duration
		// read is the configured read bound; zero leaves it unset.
		read time.Duration
	}
	tests := []tc{
		{name: "only an idle bound", idle: readBudget},
		//: the idle bound is the tighter of the two here.
		{name: "a tighter idle bound", idle: readBudget, read: 10 * time.Second},
		{name: "a looser idle bound", idle: 10 * time.Second, read: readBudget},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		outcome := make(chan error, 1)
		opts := []server.GroupOption{
			server.Listen("tcp", "127.0.0.1:0"),
			server.IdleTimeout(c.idle),
		}
		if c.read > 0 {
			opts = append(opts, server.ReadTimeout(c.read))
		}
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		srv.Group("idle", opts...).HandleFunc(func(_ context.Context, conn corenet.Conn) error {
			_, err := io.ReadFull(conn, make([]byte, 1))
			outcome <- err
			return err
		})
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}

		conn, derr := stdnet.DialTimeout("tcp", srv.State().Listeners[0].Address, 2*time.Second)
		if derr != nil {
			t.Fatalf("dial: %v", derr)
		}
		defer closeOrFail(t, conn)

		select {
		case err := <-outcome:
			//: an idle peer is cut off by whichever bound is tighter.
			if err == nil {
				t.Fatal("an idle connection was never cut off")
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("an idle connection outlived both its %v idle and %v read bounds",
				c.idle, c.read)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestHandshakeTimeout pins the phase that had no bound at all.
//
// The listener does not negotiate in Accept — the handshake runs on the
// connection's own goroutine, deliberately, so one slow peer cannot stall the
// accept path. The consequence is that a peer which opens a TCP connection to a
// TLS listener and then says nothing was holding a goroutine, an in-flight token
// and a slot under the group's connection ceiling indefinitely: no handler had
// run, so no read or idle deadline applied.
func TestHandshakeTimeout(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// budget is the configured handshake bound.
		budget time.Duration
	}
	tests := []tc{
		{name: "a short budget", budget: readBudget},
		{name: "a tighter budget", budget: 100 * time.Millisecond},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		reached := make(chan struct{}, 1)
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		//: a short explicit budget keeps the test quick; that a group
		//: configuring NOTHING still gets a bound is pinned by
		//: Test_handshakeBudget.
		srv.Group("tls",
			server.Listen("tcp", "127.0.0.1:0"),
			server.TLS(selfSignedIdentity(t)),
			server.HandshakeTimeout(c.budget),
		).HandleFunc(func(_ context.Context, _ corenet.Conn) error {
			reached <- struct{}{}
			return nil
		})
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}

		conn, derr := stdnet.DialTimeout("tcp", srv.State().Listeners[0].Address, 2*time.Second)
		if derr != nil {
			t.Fatalf("dial: %v", derr)
		}
		defer closeOrFail(t, conn)

		//: say nothing: a ClientHello never arrives, so the negotiation stalls.
		if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatalf("set deadline: %v", err)
		}
		_, err := conn.Read(make([]byte, 1))

		//: the server must give up on the negotiation and close, which the peer
		//: observes as EOF or a reset rather than as its own deadline expiring.
		if err == nil {
			t.Fatal("the server answered a peer that never sent a ClientHello")
		}
		if errors.Is(err, os.ErrDeadlineExceeded) || isTimeout(err) {
			t.Fatalf("the stalled handshake was never bounded: the peer's own %v "+
				"deadline expired first, so the connection was still held", 5*time.Second)
		}
		//: and no handler may have run for a connection that never negotiated.
		select {
		case <-reached:
			t.Fatal("the handler ran for a connection whose handshake never completed")
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

// isTimeout reports whether err is a timeout in the net sense.
func isTimeout(err error) bool {
	var netErr stdnet.Error
	//: a deadline the caller set on its own socket reports itself this way.
	if errors.As(err, &netErr) {
		//: only a genuine timeout counts.
		return netErr.Timeout()
	}
	//: anything else is the server closing, which is the outcome under test.
	return false
}

// TestReadBufferSize pins that the scratch buffer arrives at FULL LENGTH, and
// that a group which asked for none gets none.
//
// A zero-length slice with capacity would force every handler to reslice it,
// which is exactly the papercut Buffer exists to remove; an allocation for a
// group that never calls Buffer is a cost paid per connection for nothing.
func TestReadBufferSize(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// size is the buffer the group asks for; zero means none.
		size int
	}
	tests := []tc{
		{name: "no buffer requested"},
		{name: "a small buffer", size: 64},
		//: larger than the pool's guaranteed minimum, so the engine has to
		//: allocate rather than hand back a short one.
		{name: "a large buffer", size: 1 << 20},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sizes := make(chan int, 1)
		opts := []server.GroupOption{server.Listen("tcp", "127.0.0.1:0")}
		if c.size > 0 {
			opts = append(opts, server.ReadBufferSize(c.size))
		}
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		srv.Group("api", opts...).HandleFunc(func(_ context.Context, conn corenet.Conn) error {
			sizes <- len(conn.Buffer())
			return nil
		})
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}

		conn, derr := stdnet.DialTimeout("tcp", srv.State().Listeners[0].Address, 2*time.Second)
		if derr != nil {
			t.Fatalf("dial: %v", derr)
		}
		defer closeOrFail(t, conn)

		select {
		case got := <-sizes:
			//: full length, ready to read into.
			if got != c.size {
				t.Fatalf("Buffer has length %d, want %d", got, c.size)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the connection was never served")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestMaxPacketSize pins the three boundaries around the ceiling, and the
// property that matters most: an oversized datagram is never delivered as a
// truncated prefix.
//
// Truncation is the dangerous outcome precisely because it is invisible. The
// kernel discards the tail, so the handler receives bytes that read exactly like
// a complete message and has no way to know otherwise — a length-prefixed or
// delimited protocol will simply parse something that was never sent. The drop
// is observable instead, through State.
func TestMaxPacketSize(t *testing.T) {
	t.Parallel()
	const ceiling int = 64
	type tc struct {
		// name describes where the datagram sits relative to the ceiling.
		name string
		// size is the payload length sent, in bytes.
		size int
		// wantServed is whether the handler must receive it whole.
		wantServed bool
	}
	tests := []tc{
		{name: "an empty datagram", size: 0, wantServed: true},
		{name: "one byte under the ceiling", size: ceiling - 1, wantServed: true},
		{name: "exactly at the ceiling", size: ceiling, wantServed: true},
		{name: "one byte over the ceiling", size: ceiling + 1},
		{name: "far over the ceiling", size: 4096},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		seen := make(chan int, 4)
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		srv.PacketGroup("sized",
			server.Listen("udp", "127.0.0.1:0"),
			server.MaxPacketSize(ceiling),
		).HandleFunc(func(_ context.Context, p corenet.Packet) error {
			seen <- len(p.Data())
			return nil
		})
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}

		conn, derr := stdnet.DialTimeout("udp", srv.State().Listeners[0].Address, 2*time.Second)
		if derr != nil {
			t.Fatalf("dial: %v", derr)
		}
		defer closeOrFail(t, conn)
		if _, werr := conn.Write(make([]byte, c.size)); werr != nil {
			t.Fatalf("write: %v", werr)
		}

		select {
		case got := <-seen:
			//: an oversized datagram must not reach the handler at all.
			if !c.wantServed {
				t.Fatalf("a %d-byte datagram was delivered as %d bytes under a %d-byte "+
					"ceiling — a truncated prefix is indistinguishable from a whole message",
					c.size, got, ceiling)
			}
			//: a datagram within the ceiling must arrive entire.
			if got != c.size {
				t.Fatalf("a %d-byte datagram arrived as %d bytes", c.size, got)
			}
		case <-time.After(2 * time.Second):
			//: silence is the correct outcome only for an oversized datagram.
			if c.wantServed {
				t.Fatalf("a %d-byte datagram was dropped under a %d-byte ceiling", c.size, ceiling)
			}
			//: and the drop has to be visible, or it is just a quieter truncation.
			if got := srv.State().OversizedPackets; got != 1 {
				t.Fatalf("State().OversizedPackets = %d, want 1 — the drop was not reported", got)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestBatchSize pins that batching changes the SYSCALL COUNT and nothing else:
// every datagram queued before a read must still be served, in order, and none
// may be dropped or duplicated by the buffer reuse.
//
// Where the platform cannot batch, State reports the degradation rather than
// hiding it — a silent fallback is indistinguishable from a working one.
func TestBatchSize(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// batch is the configured batch size.
		batch int
		// datagrams is how many are queued before the reads.
		datagrams int
	}
	tests := []tc{
		{name: "batching disabled", batch: 1, datagrams: 8},
		{name: "the platform default", batch: 0, datagrams: 8},
		{name: "an explicit batch", batch: 8, datagrams: 8},
		{name: "a batch larger than the traffic", batch: 32, datagrams: 4},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		seen := make(chan string, 64)
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		srv.PacketGroup("collect",
			server.Listen("udp", "127.0.0.1:0"),
			server.BatchSize(c.batch),
		).HandleFunc(func(_ context.Context, p corenet.Packet) error {
			//: copy: the payload is only valid until this returns.
			seen <- string(p.Data())
			return nil
		})
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}

		conn, err := stdnet.DialTimeout("udp", srv.State().Listeners[0].Address, 2*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer closeOrFail(t, conn)
		for i := range c.datagrams {
			if _, werr := conn.Write([]byte{byte('a' + i)}); werr != nil {
				t.Fatalf("write %d: %v", i, werr)
			}
		}

		got := make(map[string]int, c.datagrams)
		for range c.datagrams {
			select {
			case payload := <-seen:
				got[payload]++
			case <-time.After(5 * time.Second):
				t.Fatalf("only %d of %d datagrams were served", len(got), c.datagrams)
			}
		}
		//: every distinct payload exactly once — a reused buffer that leaked
		//: between slots would show up here as a duplicate.
		for i := range c.datagrams {
			payload := string([]byte{byte('a' + i)})
			if got[payload] != 1 {
				t.Fatalf("payload %q served %d times, want 1", payload, got[payload])
			}
		}
		//: whichever way the platform went, the report must be self-consistent.
		listener := srv.State().Listeners[0]
		if listener.Degraded != (listener.DegradedReason != "") {
			t.Fatalf("degraded=%v but reason=%q — the report contradicts itself",
				listener.Degraded, listener.DegradedReason)
		}
		if listener.Degraded != srv.State().Degraded() {
			t.Fatal("State.Degraded disagrees with its own listener")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestShards pins that sharding is either IN EFFECT or REPORTED.
//
// Several listeners on one address is what SO_REUSEPORT buys: the kernel
// load-balances incoming connections across them, so N accept loops never
// contend on one accept queue. Where the platform or the family cannot deliver
// it, the count collapses to one and State says why — a silent collapse would
// make a benchmark compare a sharded server against itself and report "no gain"
// for entirely the wrong reason.
func TestShards(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// shards is the requested shard count; zero means one per core.
		shards int
		// unixSocket binds a Unix socket, which cannot be shared.
		unixSocket bool
		// requests is how many connections are served afterwards.
		requests int
	}
	tests := []tc{
		{name: "an explicit shard count", shards: 4, requests: 8},
		{name: "a single shard", shards: 1, requests: 2},
		//: auto-sizing disappoints no expectation, so it is never a degradation.
		{name: "auto-sizing", shards: 0, requests: 2},
		//: SO_REUSEPORT is an IP-socket option; a second bind on a path fails.
		{name: "a unix socket asked to shard", shards: 4, unixSocket: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		network, addr := "tcp", "127.0.0.1:0"
		if c.unixSocket {
			network, addr = "unix", t.TempDir()+"/sharded.sock"
		}
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		srv.Group("sharded",
			server.Listen(network, addr),
			server.Shards(c.shards),
		).HandleFunc(echoHandler)
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}

		state := srv.State()
		//: one row per address, not one per shard — N rows would read as N
		//: separate addresses.
		if len(state.Listeners) != 1 {
			t.Fatalf("listeners = %d, want 1 row for 1 address", len(state.Listeners))
		}
		listener := state.Listeners[0]
		//: the flag and the reason must agree, or State contradicts itself.
		if listener.Degraded != (listener.DegradedReason != "") {
			t.Fatalf("degraded=%v but reason=%q — the report contradicts itself",
				listener.Degraded, listener.DegradedReason)
		}
		switch {
		//: a family that cannot shard must say so for an EXPLICIT request.
		case c.unixSocket:
			if listener.Shards != 1 {
				t.Fatalf("Shards = %d on a unix socket, want 1", listener.Shards)
			}
			if !listener.Degraded {
				t.Fatal("a unix socket silently ignored an explicit shard request")
			}
			if !srv.State().Degraded() {
				t.Fatal("State.Degraded did not surface the listener's degradation")
			}
		//: a degraded platform collapses to one and says why.
		case listener.Degraded:
			if listener.Shards != 1 {
				t.Fatalf("Shards = %d on a degraded listener, want 1", listener.Shards)
			}
		//: auto-sizing yields at least one, and is never reported as a fallback.
		case c.shards == 0:
			if listener.Shards < 1 {
				t.Fatalf("Shards = %d for auto-sizing, want at least 1", listener.Shards)
			}
		default:
			if listener.Shards != c.shards {
				t.Fatalf("Shards = %d, want %d — the request collapsed silently",
					listener.Shards, c.shards)
			}
		}
		//: and however many listeners there are, they serve.
		for range c.requests {
			if got := roundTrip(t, listener.Address, "sharded"); got != "sharded\n" {
				t.Fatalf("echo = %q", got)
			}
		}
		if c.requests > 0 && !waitFor(t, func() bool {
			return srv.State().TotalConns == uint64(c.requests)
		}) {
			t.Fatalf("TotalConns = %d, want %d — a shard's accepts went unaccounted",
				srv.State().TotalConns, c.requests)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestMaxConns pins that the ceiling turns connections AWAY rather than queuing
// them silently, and that a served connection gives its slot back.
//
// A refusal the peer can observe beats a timeout it cannot tell from a hang. A
// ceiling that leaked slots would wedge the group after MaxConns connections,
// which is far worse than having no ceiling at all.
func TestMaxConns(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// ceiling is the configured maximum; zero means none.
		ceiling int
		// rounds is how many sequential connections are served.
		rounds int
		// probeBeyond holds the ceiling full and dials one more.
		probeBeyond bool
	}
	tests := []tc{
		//: an unset ceiling means NO ceiling, not a ceiling of zero — the
		//: difference between a working server and a dead one.
		{name: "no ceiling by default", rounds: 3},
		{name: "a ceiling that releases its slots", ceiling: 4, rounds: 20},
		{name: "a ceiling of one, sequentially", ceiling: 1, rounds: 5},
		{name: "a connection past the ceiling", ceiling: 1, probeBeyond: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if !c.probeBeyond {
			//: startEcho supplies the address; only the ceiling varies here.
			var opts []server.GroupOption
			if c.ceiling > 0 {
				opts = append(opts, server.MaxConns(c.ceiling))
			}
			srv, addr := startEcho(t, opts...)
			//: far more connections than slots, so a leak wedges the server well
			//: before the loop ends.
			for i := range c.rounds {
				if got := roundTrip(t, addr, "ping"); got != "ping\n" {
					t.Fatalf("connection %d got %q, want %q — a slot leaked", i, got, "ping\n")
				}
			}
			//: sequential traffic under a ceiling must never be refused, but a
			//: slot is released when the handler returns, which can be just
			//: after the peer saw its echo — so a ceiling of one may legitimately
			//: refuse a reconnection that arrives inside that window.
			if c.ceiling > 1 && srv.State().RejectedConns != 0 {
				t.Fatalf("RejectedConns = %d for sequential traffic under a ceiling of %d, want 0",
					srv.State().RejectedConns, c.ceiling)
			}
			return
		}

		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		srv.Group("capped",
			server.Listen("tcp", "127.0.0.1:0"),
			server.MaxConns(c.ceiling),
		).HandleFunc(func(_ context.Context, conn corenet.Conn) error {
			if _, err := io.WriteString(conn, "held\n"); err != nil {
				return err
			}
			<-release
			return nil
		})
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		addr := srv.State().Listeners[0].Address

		//: the first connection claims the only slot and holds it.
		first, err := stdnet.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatalf("dial first: %v", err)
		}
		defer closeOrFail(t, first)
		readLine(t, first, "held\n")

		//: the second is accepted by the kernel, then refused by the ceiling.
		second, err := stdnet.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatalf("dial second: %v", err)
		}
		defer closeOrFail(t, second)
		if derr := second.SetReadDeadline(time.Now().Add(3 * time.Second)); derr != nil {
			t.Fatalf("set deadline: %v", derr)
		}
		if _, rerr := second.Read(make([]byte, 8)); rerr == nil {
			t.Fatal("the second connection was served despite a ceiling of one")
		}
		//: the refusal must be counted, or an operator cannot see saturation.
		if !waitFor(t, func() bool { return srv.State().RejectedConns >= 1 }) {
			t.Fatal("a refused connection was never counted")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestAdopt pins the property socket activation exists to provide: a service
// asked to adopt a socket the supervisor never passed must FAIL, never quietly
// bind its own.
//
// A silent fallback would lose the socket continuity that makes a zero-downtime
// restart possible, and would lose it invisibly — the process would look healthy
// while dropping every connection the outgoing one still held.
func TestAdopt(t *testing.T) {
	//: not parallel — it mutates the process environment.
	type tc struct {
		// name describes the case.
		name string
		// listenFDs and fdNames are what the supervisor published.
		listenFDs string
		fdNames   string
		// requested is the socket name the group asks for.
		requested string
	}
	tests := []tc{
		{name: "no activation at all", listenFDs: "", requested: "api"},
		{name: "a name the unit file did not publish", listenFDs: "1", fdNames: "published", requested: "requested"},
		{name: "an empty name list", listenFDs: "1", fdNames: "", requested: "api"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		t.Setenv("LISTEN_FDS", c.listenFDs)
		t.Setenv("LISTEN_FDNAMES", c.fdNames)
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		srv.Group("api", server.Adopt(c.requested)).HandleFunc(noopHandler)

		err := srv.Start(t.Context())

		if !errs.HasCode(err, corenet.CodeSocketAdoptFailed) {
			t.Fatalf("Start = %v, want SOCKET_ADOPT_FAILED", err)
		}
	}
	for _, c := range tests {
		//: serial, because each case rewrites the process environment.
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// TestAdopt_ServesTheInheritedSocket is the end-to-end proof, run across a real
// exec because that is the only place adoption exists.
//
// It cannot be staged in-process: sd_listen_fds(3) reads from descriptor 3, and
// a Go test binary already holds that descriptor. exec.Cmd.ExtraFiles places a
// file at exactly fd 3 in the child, which is what a supervisor does, so the
// child wakes up in the state a socket-activated service wakes up in — holding a
// socket it never bound. TestMain is what turns that child into an assertion
// rather than a test that skips itself on every ordinary run.
func TestAdopt_ServesTheInheritedSocket(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// socketName is the name published in LISTEN_FDNAMES.
		socketName string
	}
	tests := []tc{
		{name: "a socket published as api", socketName: "api"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ln := listenTCP(t)
		file, err := ln.File()
		if err != nil {
			t.Fatalf("listener file: %v", err)
		}
		defer closeOrFail(t, file)
		addr := ln.Addr().String()
		//: close the original so only the inherited descriptor stays live,
		//: exactly as after an exec where the parent's copy is gone.
		closeOrFail(t, ln)

		cmd := exec.CommandContext(t.Context(), os.Args[0])
		//: ExtraFiles[0] lands on fd 3 — where sd_listen_fds starts.
		cmd.ExtraFiles = []*os.File{file}
		//: LISTEN_PID is deliberately omitted. sdlisten treats an absent pid as
		//: "addressed to me", which is what its own Prepare emits, and a parent
		//: cannot know the child's pid before starting it anyway.
		cmd.Env = append(os.Environ(),
			"LISTEN_FDS=1",
			"LISTEN_FDNAMES="+c.socketName,
			childAddrEnv+"="+addr,
		)

		out, runErr := cmd.CombinedOutput()

		if runErr != nil {
			t.Fatalf("the adopting child failed: %v\n%s", runErr, out)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
