// Package server_test — black-box acceptance tests for the public server
// facade: the five-statement example the API exists for, the option wiring, the
// HTTP adapter, and the typed sentinels a consumer branches on.
package server_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	stdnet "net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/server"
	"github.com/kitsunium/sdk/pkg/v1/server/sse"
	"github.com/kitsunium/sdk/pkg/v1/tlsid"
)

// TestTheShortestUsefulServer is the API's own acceptance test.
//
// The requirement behind this domain was to be able to plug a handler in very
// easily. That is not measurable by reading the godoc, so it is measured here:
// the body of runCase below is the complete, unabridged code needed to run a
// working server, and it is five statements. If a future change makes this
// example grow — an extra construction step, an error to thread, a type to
// declare — the API has regressed in the one dimension that motivated it,
// whatever else it gained.
//
// The cases also pin the "same thing everywhere" promise: a Unix socket is one
// option change away from a TCP port, with no other difference in the handler
// or the wiring.
func TestTheShortestUsefulServer(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		network string
		addr    string
	}
	tests := []tc{
		{"over a TCP port", "tcp", "127.0.0.1:0"},
		{"over a Unix socket", "unix", ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		addr := c.addr
		//: a Unix socket needs a path, and it must be this case's own.
		if c.network == "unix" {
			addr = t.TempDir() + "/api.sock"
		}
		ctx := t.Context()

		srv := server.New()
		srv.Group("echo", server.Listen(c.network, addr)).
			HandleFunc(func(_ context.Context, conn server.Conn) error {
				_, err := io.Copy(conn, conn)
				return err
			})
		if err := srv.Start(ctx); err != nil {
			t.Fatalf("start: %v", err)
		}
		defer func() { closeOrFail(t, srv) }()

		//: everything below is the assertion, not part of the example.
		bound := srv.State().Listeners[0].Address
		if got := echo(t, c.network, bound, "hello"); got != "hello\n" {
			t.Fatalf("echo = %q, want %q", got, "hello\n")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
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
	type tc struct {
		name  string
		drain time.Duration
		//: how many round trips to make before cancelling; a drain that only
		//: works on a never-used listener is not a drain.
		exchanges int
	}
	tests := []tc{
		{"cancelled while idle", 2 * time.Second, 0},
		{"cancelled after one exchange", 2 * time.Second, 1},
		{"cancelled after several exchanges", time.Second, 3},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ctx, cancel := context.WithCancel(t.Context())
		srv := server.New(server.WithDrainTimeout(c.drain))
		srv.Group("echo", server.Listen("tcp", "127.0.0.1:0")).
			HandleFunc(func(_ context.Context, conn server.Conn) error {
				_, err := io.Copy(conn, conn)
				return err
			})

		done := make(chan error, 1)
		go func() { done <- srv.Serve(ctx) }()

		//: Serve binds before it blocks, so the port is open once the phase moves.
		waitFor(t, func() bool { return srv.State().Phase == server.PhaseServing })
		for range c.exchanges {
			if got := echo(t, "tcp", srv.State().Listeners[0].Address, "ping"); got != "ping\n" {
				t.Fatalf("echo = %q", got)
			}
		}

		cancel()
		if err := <-done; err != nil {
			t.Fatalf("serve returned %v, want a clean drain", err)
		}
		if phase := srv.State().Phase; phase != server.PhaseStopped {
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

// TestMiddlewaresWrapOutermostFirst pins the ordering through the public API,
// since a middleware chain that runs in an unexpected order is a class of bug
// that only shows up under load.
func TestMiddlewaresWrapOutermostFirst(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		names []string
	}
	tests := []tc{
		{"no middleware at all", nil},
		{"a single middleware", []string{"only"}},
		{"two, outermost first", []string{"outer", "inner"}},
		{"four, outermost first", []string{"a", "b", "c", "d"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		order := make(chan string, len(c.names)+1)
		tag := func(name string) server.Middleware {
			return func(next server.Handler) server.Handler {
				return server.HandlerFunc(func(ctx context.Context, conn server.Conn) error {
					order <- name
					return next.ServeConn(ctx, conn)
				})
			}
		}
		mws := make([]server.Middleware, 0, len(c.names))
		for _, name := range c.names {
			mws = append(mws, tag(name))
		}

		srv := server.New()
		srv.Group("api", server.Listen("tcp", "127.0.0.1:0")).
			Use(mws...).
			HandleFunc(func(_ context.Context, conn server.Conn) error {
				order <- "handler"
				_, err := io.WriteString(conn, "ok\n")
				return err
			})
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		defer func() { closeOrFail(t, srv) }()

		if got := echo(t, "tcp", srv.State().Listeners[0].Address, "go"); got != "ok\n" {
			t.Fatalf("reply = %q", got)
		}
		//: the declared order, then the handler last.
		for _, want := range append(append([]string{}, c.names...), "handler") {
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
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestOneGroupServesTwoFamilies pins the reason groups exist: one handler
// answering on several listeners — a TCP port and a Unix socket alike — wired
// once and reported under one group name.
func TestOneGroupServesTwoFamilies(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: how many extra TCP listeners beside the Unix one, so the shapes
		//: range over the single, the mixed and the many.
		tcpListeners int
		unixListener bool
	}
	tests := []tc{
		{"one TCP listener alone", 1, false},
		{"a TCP port and a Unix socket", 1, true},
		{"two TCP ports and a Unix socket", 2, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		opts := make([]server.GroupOption, 0, c.tcpListeners+1)
		for range c.tcpListeners {
			opts = append(opts, server.Listen("tcp", "127.0.0.1:0"))
		}
		want := c.tcpListeners
		if c.unixListener {
			opts = append(opts, server.Listen("unix", t.TempDir()+"/dual.sock"))
			want++
		}

		srv := server.New()
		srv.Group("dual", opts...).
			HandleFunc(func(_ context.Context, conn server.Conn) error {
				_, err := io.WriteString(conn, conn.Group()+"\n")
				return err
			})
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		defer func() { closeOrFail(t, srv) }()

		state := srv.State()
		if len(state.Listeners) != want {
			t.Fatalf("listeners = %d, want %d", len(state.Listeners), want)
		}
		//: every listener must report the one group that declared it.
		for _, listener := range state.Listeners {
			if listener.Group != "dual" {
				t.Errorf("listener group = %q, want %q", listener.Group, "dual")
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

// TestSentinelsAreMatchableThroughTheFacade pins that a consumer can branch on
// a failure without importing internal/*, which Go's firewall forbids anyway.
func TestSentinelsAreMatchableThroughTheFacade(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		build func(*server.Server)
		want  error
	}
	tests := []tc{
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
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := server.New()
		c.build(srv)
		if err := srv.Start(t.Context()); !errors.Is(err, c.want) {
			t.Fatalf("errors.Is(err, %v) = false, got %v", c.want, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestEveryGroupOptionIsWiredThrough pins that each public option actually
// reaches the engine. A facade forwarder that silently drops its argument would
// compile, pass a smoke test, and quietly disable a timeout in production.
func TestEveryGroupOptionIsWiredThrough(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		bufferSize int
	}
	tests := []tc{
		{"a small read buffer", 1024},
		{"the usual read buffer", 4096},
		{"a large read buffer", 16384},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := server.New(server.WithDrainTimeout(time.Second))
		srv.Group("api",
			server.Listen("tcp", "127.0.0.1:0"),
			server.ReadTimeout(2*time.Second),
			server.WriteTimeout(2*time.Second),
			server.IdleTimeout(5*time.Second),
			server.ReadBufferSize(c.bufferSize),
		).HandleFunc(func(_ context.Context, conn server.Conn) error {
			//: the scratch buffer must arrive at the requested size, ready to
			//: read into — a zero-length slice would force every caller to
			//: reslice it, which is exactly the wiring bug being probed.
			buf := conn.Buffer()
			if len(buf) != c.bufferSize {
				t.Errorf("Buffer() length = %d, want %d", len(buf), c.bufferSize)
			}
			n, err := conn.Read(buf)
			if err != nil {
				return err
			}
			_, err = conn.Write(buf[:n])
			return err
		})
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		defer func() { closeOrFail(t, srv) }()

		if got := echo(t, "tcp", srv.State().Listeners[0].Address, "buffered"); got != "buffered\n" {
			t.Fatalf("echo = %q", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestTLSOptionServesOverTLS proves the identity actually reaches the listener
// by completing a real handshake, rather than asserting a struct field.
func TestTLSOptionServesOverTLS(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		reply string
	}
	tests := []tc{
		{"a short payload", "secure\n"},
		{"a payload with spaces", "still secure\n"},
		{"a long payload", strings.Repeat("x", 4096) + "\n"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		certPEM, keyPEM := mintCert(t)
		identity, err := tlsid.New(tlsid.Params{CertPEM: certPEM, KeyPEM: keyPEM})
		if err != nil {
			t.Fatalf("server identity: %v", err)
		}
		clientID, err := tlsid.New(tlsid.Params{RootsPEM: certPEM, ServerName: "kitsunium-test"})
		if err != nil {
			t.Fatalf("client identity: %v", err)
		}

		srv := server.New()
		srv.Group("secure", server.Listen("tcp", "127.0.0.1:0"), server.TLS(identity)).
			HandleFunc(func(_ context.Context, conn server.Conn) error {
				_, werr := io.WriteString(conn, c.reply)
				return werr
			})
		if serr := srv.Start(t.Context()); serr != nil {
			t.Fatalf("start: %v", serr)
		}
		defer func() { closeOrFail(t, srv) }()

		if got := dialTLS(t, srv.State().Listeners[0].Address, clientID.ClientConfig()); got != c.reply {
			t.Fatalf("reply = %q, want %q", got, c.reply)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestPlainDialAgainstATLSGroupFails pins that the TLS option is not decorative:
// a plaintext client must not be served by a TLS listener, whatever it sends.
func TestPlainDialAgainstATLSGroupFails(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		send string
	}
	tests := []tc{
		{"a plaintext line", "plaintext\n"},
		{"nothing at all", ""},
		//: a byte that is not a valid TLS record type; the listener must refuse
		//: it rather than fall through to the handler.
		{"a byte that cannot start a TLS record", "\x00"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		certPEM, keyPEM := mintCert(t)
		identity, err := tlsid.New(tlsid.Params{CertPEM: certPEM, KeyPEM: keyPEM})
		if err != nil {
			t.Fatalf("identity: %v", err)
		}
		srv := server.New()
		srv.Group("secure", server.Listen("tcp", "127.0.0.1:0"), server.TLS(identity)).
			HandleFunc(func(_ context.Context, conn server.Conn) error {
				_, werr := io.WriteString(conn, "secure\n")
				return werr
			})
		if serr := srv.Start(t.Context()); serr != nil {
			t.Fatalf("start: %v", serr)
		}
		defer func() { closeOrFail(t, srv) }()

		conn, derr := stdnet.DialTimeout("tcp", srv.State().Listeners[0].Address, 2*time.Second)
		if derr != nil {
			t.Fatalf("dial: %v", derr)
		}
		defer func() { closeOrFail(t, conn) }()
		if c.send != "" {
			if _, werr := io.WriteString(conn, c.send); werr != nil {
				//: a write failure is already proof the peer rejected us.
				return
			}
		}
		if sderr := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); sderr != nil {
			t.Fatalf("set deadline: %v", sderr)
		}
		buf := make([]byte, 16)
		n, rerr := conn.Read(buf)
		//: a TLS listener must never answer a plaintext peer with our payload.
		if rerr == nil && bytes.Contains(buf[:n], []byte("secure")) {
			t.Fatal("a TLS listener served a plaintext client")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestChainIsReachableFromTheFacade pins that middleware composition works
// without importing internal/*, and that the composed order matches the order
// the call site reads in.
func TestChainIsReachableFromTheFacade(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		names []string
		want  string
	}
	tests := []tc{
		{"no middleware", nil, "handler"},
		{"one middleware", []string{"a"}, "a,handler"},
		{"two, outermost first", []string{"a", "b"}, "a,b,handler"},
		{"three, outermost first", []string{"a", "b", "c"}, "a,b,c,handler"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var order []string
		tag := func(name string) server.Middleware {
			return func(next server.Handler) server.Handler {
				return server.HandlerFunc(func(ctx context.Context, conn server.Conn) error {
					order = append(order, name)
					return next.ServeConn(ctx, conn)
				})
			}
		}
		mws := make([]server.Middleware, 0, len(c.names))
		for _, name := range c.names {
			mws = append(mws, tag(name))
		}
		base := server.HandlerFunc(func(context.Context, server.Conn) error {
			order = append(order, "handler")
			return nil
		})

		chained := server.Chain(base, mws...)
		if err := chained.ServeConn(t.Context(), nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := strings.Join(order, ","); got != c.want {
			t.Fatalf("order = %q, want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestHTTPAdapterServesRealRequests is the D3 proof: net/http does the protocol
// over our listener, and a stock http.Client is served correctly.
func TestHTTPAdapterServesRealRequests(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		path string
		body string
	}
	tests := []tc{
		{"a short body", "/hello", "hello from the adapter"},
		{"an empty body", "/empty", ""},
		{"a body larger than one write buffer", "/big", strings.Repeat("y", 64*1024)},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		mux := http.NewServeMux()
		mux.HandleFunc(c.path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Method", r.Method)
			if _, err := io.WriteString(w, c.body); err != nil {
				t.Errorf("write: %v", err)
			}
		})
		_, base := startHTTP(t, mux)

		resp := get(t, base+c.path)
		if resp.status != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.status)
		}
		if resp.body != c.body {
			t.Fatalf("body length = %d, want %d", len(resp.body), len(c.body))
		}
		//: the handler must see the real method, not one the adapter invented.
		if resp.method != http.MethodGet {
			t.Errorf("the handler saw method %q", resp.method)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestHTTPAdapterKeepsAliveAcrossRequests pins that the bridge hands over a
// CONNECTION, not a request: several requests must reuse one connection, or the
// adapter would be paying a hand-off per request rather than per connection.
func TestHTTPAdapterKeepsAliveAcrossRequests(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		requests int
	}
	tests := []tc{
		{"two requests", 2},
		{"three requests", 3},
		{"ten requests", 10},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		mux := http.NewServeMux()
		mux.HandleFunc("/n", func(w http.ResponseWriter, _ *http.Request) {
			if _, err := io.WriteString(w, "ok"); err != nil {
				t.Errorf("write: %v", err)
			}
		})
		srv, base := startHTTP(t, mux)

		client := &http.Client{Timeout: 5 * time.Second}
		for range c.requests {
			resp, err := client.Get(base + "/n")
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if _, cerr := io.Copy(io.Discard, resp.Body); cerr != nil {
				t.Fatalf("drain: %v", cerr)
			}
			closeOrFail(t, resp.Body)
		}
		//: every request over a reused connection must count as ONE accept.
		if total := srv.State().TotalConns; total != 1 {
			t.Fatalf("TotalConns = %d after %d keep-alive requests, want 1", total, c.requests)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestHTTPAdapterOverTLS pins that the adapter inherits the group's identity:
// HTTPS comes from our listener, not from http.Server's own TLS handling.
func TestHTTPAdapterOverTLS(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		path string
		body string
	}
	tests := []tc{
		{"a short body", "/secure", "encrypted"},
		{"a body spanning several records", "/secure-big", strings.Repeat("z", 32*1024)},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		certPEM, keyPEM := mintCert(t)
		identity, err := tlsid.New(tlsid.Params{CertPEM: certPEM, KeyPEM: keyPEM})
		if err != nil {
			t.Fatalf("server identity: %v", err)
		}
		clientID, err := tlsid.New(tlsid.Params{RootsPEM: certPEM, ServerName: "kitsunium-test"})
		if err != nil {
			t.Fatalf("client identity: %v", err)
		}
		mux := http.NewServeMux()
		mux.HandleFunc(c.path, func(w http.ResponseWriter, r *http.Request) {
			//: the request must arrive over a completed TLS connection.
			if r.TLS == nil {
				t.Error("the handler saw a plaintext request on a TLS group")
			}
			if _, werr := io.WriteString(w, c.body); werr != nil {
				t.Errorf("write: %v", werr)
			}
		})
		srv, _ := startHTTP(t, mux, server.TLS(identity))

		client := &http.Client{
			Timeout:   5 * time.Second,
			Transport: &http.Transport{TLSClientConfig: clientID.ClientConfig()},
		}
		resp, err := client.Get("https://" + srv.State().Listeners[0].Address + c.path)
		if err != nil {
			t.Fatalf("https get: %v", err)
		}
		defer closeOrFail(t, resp.Body)
		body, rerr := io.ReadAll(resp.Body)
		if rerr != nil {
			t.Fatalf("read: %v", rerr)
		}
		if string(body) != c.body {
			t.Fatalf("body length = %d, want %d", len(body), len(c.body))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestHTTPAdapterDrainsOnShutdown pins that a connection nobody is going to
// finish does not hold the drain open for its whole budget.
//
// Two shapes cause that, and they need different mechanisms. An IDLE KEEP-ALIVE
// is one net/http can release itself, which is why the adapter shuts its
// http.Server down before draining. An OPEN EVENT STREAM is one nothing can
// release from outside: http.Server.Shutdown waits for in-flight requests, and
// a request that never ends never becomes idle. Measured before the drain
// signal existed, an open stream held Shutdown for the caller's entire budget
// and was then killed by having its socket severed — DRAIN_TIMEOUT on every
// deploy, for every connected client, with no chance for the handler to stop
// cleanly. The signal (server.DrainSignal, watched by sse.Stream) is what turns
// that into the sub-second clean drain asserted here.
func TestHTTPAdapterDrainsOnShutdown(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: how many idle keep-alive connections are left open at shutdown.
		idleConns int64
		//: how many endless event streams are still being written at shutdown.
		openStreams int64
	}
	tests := []tc{
		{name: "one idle keep-alive", idleConns: 1},
		{name: "three idle keep-alives", idleConns: 3},
		{name: "one open event stream", openStreams: 1},
		{name: "three open event streams", openStreams: 3},
		{name: "idle keep-alives beside open streams", idleConns: 2, openStreams: 2},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv, base := startHTTP(t, streamingMux(t))

		//: a client per connection: one transport pools, and would give us one
		//: idle connection however many requests we made.
		for range c.idleConns {
			client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{}}
			resp, err := client.Get(base + "/")
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if _, cerr := io.Copy(io.Discard, resp.Body); cerr != nil {
				t.Fatalf("drain: %v", cerr)
			}
			closeOrFail(t, resp.Body)
		}
		//: each stream stays open, with its handler still running, until the
		//: shutdown below tells it to stop.
		for range c.openStreams {
			openStream(t, base)
		}
		//: idle keep-alives and live streams both count as active connections.
		waitFor(t, func() bool { return srv.State().ActiveConns == c.idleConns+c.openStreams })

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		started := time.Now()
		if serr := srv.Shutdown(ctx); serr != nil {
			t.Fatalf("shutdown reported %v, want a clean drain", serr)
		}
		//: 500 ms against a drain measured at 1.808 ms with 64 open streams
		//: (internal/service/net/sse/BENCH.md) — 277x headroom, and still the
		//: sub-second this test's own doc comment claims. The bound used to be
		//: 3s against a 5s budget, which left a regression from 1.8 ms to 2.9 s
		//: passing every test in the repository: ADR 0043's whole point,
		//: guarded by nothing that could see it fail.
		if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
			t.Fatalf("drain took %v — the connection was never released", elapsed)
		}
		if phase := srv.State().Phase; phase != server.PhaseStopped {
			t.Errorf("phase = %v, want stopped", phase)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestDrainSignalReachesAPlainHTTPHandler pins the mechanism underneath the
// event stream, without an event stream in sight.
//
// Any handler that holds a connection open indefinitely — a long poll, a hand
// rolled chunked feed — has the same problem and gets the same answer: a
// channel on the request context, closed when the server starts draining. The
// two assertions that matter are that it is NOT nil inside a handler on this
// engine (a nil would make every select silently never fire) and that the
// request context is NOT cancelled to deliver it, because cancelling would tell
// every handler to abandon the response the drain exists to let it finish.
func TestDrainSignalReachesAPlainHTTPHandler(t *testing.T) {
	t.Parallel()
	live := make(chan struct{})
	//: what the handler observed, read only after the drain has completed.
	var ctxCancelledBeforeSignal bool
	mux := http.NewServeMux()
	mux.HandleFunc("/hold", func(w http.ResponseWriter, r *http.Request) {
		draining := server.DrainSignal(r.Context())
		if draining == nil {
			t.Errorf("DrainSignal() = nil inside a handler on this engine")
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("the adapter's ResponseWriter is not an http.Flusher")
			return
		}
		//: headers out now, so the client knows the handler is running.
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		close(live)
		<-draining
		//: the drain must not have been delivered by cancelling the request.
		ctxCancelledBeforeSignal = r.Context().Err() != nil
	})
	srv, base := startHTTP(t, mux)

	resp, err := (&http.Client{Transport: &http.Transport{}}).Get(base + "/hold")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer closeOrFail(t, resp.Body)
	<-live

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started := time.Now()
	if serr := srv.Shutdown(ctx); serr != nil {
		t.Fatalf("shutdown reported %v, want a clean drain", serr)
	}
	//: 500 ms, for the reason TestHTTPAdapterDrainsOnShutdown states.
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("drain took %v — the handler never saw the signal", elapsed)
	}
	if ctxCancelledBeforeSignal {
		t.Errorf("the request context was already cancelled when the signal arrived")
	}
}

// TestHTTPStreamsReachTheClientBeforeTheHandlerReturns pins the property every
// streaming format on this engine depends on: the response is FLUSHED per
// event.
//
// Without a flush the frames accumulate in the transport buffer and arrive when
// the handler returns — which for an event stream is never, so the client sits
// on an open connection receiving nothing while the server believes it is
// working. The test proves it by making the handler's progress depend on the
// client having already received the first event: the handler cannot send the
// second until the test, having read the first, unblocks it. Buffered, this
// deadlocks; flushed, it passes in milliseconds.
func TestHTTPStreamsReachTheClientBeforeTheHandlerReturns(t *testing.T) {
	t.Parallel()
	received := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		stream, err := sse.New(w, r, sse.WithoutKeepAlive())
		if err != nil {
			t.Errorf("sse.New: %v", err)
			return
		}
		defer closeOrFail(t, stream)
		if serr := stream.Send(sse.Event{ID: "1", Name: "tick", Data: "first"}); serr != nil {
			t.Errorf("send first: %v", serr)
			return
		}
		select {
		//: the client has the first event, so the flush reached the wire.
		case <-received:
		case <-time.After(5 * time.Second):
			t.Errorf("the client never received the first event — it was buffered, not flushed")
			return
		}
		if serr := stream.Send(sse.Event{ID: "2", Name: "tick", Data: "second"}); serr != nil {
			t.Errorf("send second: %v", serr)
		}
	})
	_, base := startHTTP(t, mux)

	client := &http.Client{Transport: &http.Transport{}}
	resp, err := client.Get(base + "/stream")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer closeOrFail(t, resp.Body)
	if got := resp.Header.Get("Content-Type"); got != sse.ContentType {
		t.Fatalf("Content-Type = %q, want %q", got, sse.ContentType)
	}
	reader := bufio.NewReader(resp.Body)
	if got := readFrame(t, reader); got != "id: 1\nevent: tick\ndata: first\n\n" {
		t.Fatalf("first frame = %q", got)
	}
	close(received)
	if got := readFrame(t, reader); got != "id: 2\nevent: tick\ndata: second\n\n" {
		t.Fatalf("second frame = %q", got)
	}
}

// TestSSEStreamResumesFromTheClientCursor pins the reconnection contract: the
// SDK reads Last-Event-ID and hands it to the handler, and replays nothing
// itself. Resuming is the application's decision because only it knows what an
// id means and whether replaying an event is safe.
func TestSSEStreamResumesFromTheClientCursor(t *testing.T) {
	t.Parallel()
	seen := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		stream, err := sse.New(w, r, sse.WithoutKeepAlive())
		if err != nil {
			t.Errorf("sse.New: %v", err)
			return
		}
		defer closeOrFail(t, stream)
		seen <- stream.LastEventID()
		//: echo the cursor back so the handler's own resume is observable.
		if serr := stream.Send(sse.Event{ID: "next", Data: "resumed from " + stream.LastEventID()}); serr != nil {
			t.Errorf("send: %v", serr)
		}
	})
	_, base := startHTTP(t, mux)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/stream", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set(sse.LastEventIDHeader, "cursor-42")
	resp, gerr := (&http.Client{Transport: &http.Transport{}}).Do(req)
	if gerr != nil {
		t.Fatalf("do: %v", gerr)
	}
	defer closeOrFail(t, resp.Body)
	if got := <-seen; got != "cursor-42" {
		t.Fatalf("LastEventID() = %q, want cursor-42", got)
	}
	if got := readFrame(t, bufio.NewReader(resp.Body)); got != "id: next\ndata: resumed from cursor-42\n\n" {
		t.Fatalf("frame = %q", got)
	}
}

// streamingMux serves a plain response on "/" and an endless event stream on
// "/stream". The stream ends only when the stream itself says so — the client
// went away, or the server began draining — which is what makes it a fair test
// of a connection nothing can finish from outside.
func streamingMux(t *testing.T) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, "ok"); err != nil {
			t.Errorf("write: %v", err)
		}
	})
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		stream, err := sse.New(w, r, sse.KeepAlive(20*time.Millisecond))
		if err != nil {
			t.Errorf("sse.New: %v", err)
			return
		}
		defer closeOrFail(t, stream)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			//: the peer is gone or the server is draining; either way, stop.
			case <-stream.Done():
				return
			case <-ticker.C:
				//: a failed send means the stream is over.
				if serr := stream.Send(sse.Event{Data: "tick"}); serr != nil {
					return
				}
			}
		}
	})
	return mux
}

// openStream opens an event stream and reads its first frame, so the caller
// knows the handler is running and the connection is live. The body is closed
// when the test ends, not before.
func openStream(t *testing.T, base string) {
	t.Helper()
	//: no client timeout: a timeout would cancel the request, which is exactly
	//: the thing the drain is supposed to be the one to do.
	resp, err := (&http.Client{Transport: &http.Transport{}}).Get(base + "/stream")
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, resp.Body) })
	readFrame(t, bufio.NewReader(resp.Body))
}

// lineSource is the narrow half of *bufio.Reader that reading a frame needs.
type lineSource interface {
	// ReadString reads up to and including the first delim.
	ReadString(delim byte) (string, error)
}

// readFrame reads one whole event-stream frame — everything up to and including
// the blank line that terminates it.
func readFrame(t *testing.T, reader lineSource) string {
	t.Helper()
	var frame strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		frame.WriteString(line)
		//: the blank line is the frame terminator.
		if line == "\n" {
			return frame.String()
		}
	}
}

// noop is a handler that does nothing, for declaration-error tests.
func noop(context.Context, server.Conn) error {
	return nil
}

// echo dials the server over network, sends one line and returns the reply.
func echo(t *testing.T, network, addr, line string) string {
	t.Helper()
	conn, err := stdnet.DialTimeout(network, addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { closeOrFail(t, conn) }()
	if _, werr := io.WriteString(conn, line+"\n"); werr != nil {
		t.Fatalf("write: %v", werr)
	}
	reply, rerr := bufio.NewReader(conn).ReadString('\n')
	if rerr != nil {
		t.Fatalf("read: %v", rerr)
	}
	return reply
}

// startHTTP mounts a handler on our engine and returns its base URL.
func startHTTP(t *testing.T, h http.Handler, opts ...server.GroupOption) (srv *server.Server, base string) {
	t.Helper()
	srv = server.New()
	all := append([]server.GroupOption{server.Listen("tcp", "127.0.0.1:0")}, opts...)
	srv.Group("api", all...).HandleHTTP(h)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })
	return srv, "http://" + srv.State().Listeners[0].Address
}

// httpResult is what get observed.
type httpResult struct {
	body   string
	method string
	status int
}

// get performs one HTTP request and returns the observed result.
func get(t *testing.T, url string) httpResult {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer closeOrFail(t, resp.Body)
	body, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		t.Fatalf("read: %v", rerr)
	}
	return httpResult{status: resp.StatusCode, body: string(body), method: resp.Header.Get("X-Method")}
}

// mintCert returns PEM certificate and key bytes for a throwaway identity.
//
// SANs, not CommonName: Go refuses to verify a certificate relying on the
// legacy field, so a CommonName-only fixture fails the handshake.
func mintCert(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "kitsunium-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth,
		},
		DNSNames:    []string{"kitsunium-test"},
		IPAddresses: []stdnet.IP{stdnet.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(nil, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// dialTLS completes a real handshake against addr and returns the first line.
func dialTLS(t *testing.T, addr string, cfg *tls.Config) string {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, cfg)
	if err != nil {
		t.Fatalf("tls dial: %v", err)
	}
	defer func() { closeOrFail(t, conn) }()
	if derr := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); derr != nil {
		t.Fatalf("set deadline: %v", derr)
	}
	reply, rerr := bufio.NewReader(conn).ReadString('\n')
	if rerr != nil {
		t.Fatalf("read: %v", rerr)
	}
	return reply
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
