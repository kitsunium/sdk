// Package server_test — the stream group's declaration surface.
package server_test

import (
	"context"
	"io"
	stdnet "net"
	"net/http"
	"slices"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// echoConnHandler is an echo handler as a named type, for Handle.
type echoConnHandler struct{}

// ServeConn implements corenet.ConnHandler.
func (echoConnHandler) ServeConn(_ context.Context, c corenet.Conn) error {
	_, err := io.Copy(c, c)
	return err
}

// TestStreamGroup_Name pins the group's identity, which is the dimension every
// log line, metric and State row is tagged with. A group that reported a
// different name from the one it was declared with would make those three
// disagree about the same traffic.
func TestStreamGroup_Name(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// declared is the name the caller gives the group.
		declared string
	}
	tests := []tc{
		{name: "a simple name", declared: "api"},
		{name: "a path-like name", declared: "internal/admin"},
		{name: "an empty name", declared: ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		seen := make(chan string, 1)
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		group := srv.Group(c.declared, server.Listen("tcp", "127.0.0.1:0"))
		group.HandleFunc(func(_ context.Context, conn corenet.Conn) error {
			seen <- conn.Group()
			return nil
		})

		if group.Name() != c.declared {
			t.Fatalf("Name = %q, want %q", group.Name(), c.declared)
		}
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		//: the same name reaches the connection and State, or the three views
		//: disagree about the same traffic.
		if got := srv.State().Listeners[0].Group; got != c.declared {
			t.Errorf("State reports group %q, want %q", got, c.declared)
		}
		conn, err := stdnet.DialTimeout("tcp", srv.State().Listeners[0].Address, 2*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer closeOrFail(t, conn)
		select {
		case got := <-seen:
			if got != c.declared {
				t.Fatalf("the handler saw group %q, want %q", got, c.declared)
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

// TestStreamGroup_Handle pins that the LAST handler wins and that the call
// chains.
//
// Replacing rather than appending is what makes a group's handler unambiguous:
// two handlers would need a rule for which one runs, and any such rule is a
// worse answer than "the one you set last".
func TestStreamGroup_Handle(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// replacements is how many times the handler is replaced before the
		// echo handler is installed.
		replacements int
	}
	tests := []tc{
		{name: "one handler"},
		{name: "a replaced handler", replacements: 1},
		{name: "several replacements", replacements: 3},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		group := srv.Group("echo", server.Listen("tcp", "127.0.0.1:0"))
		for range c.replacements {
			group.Handle(corenet.ConnHandlerFunc(func(context.Context, corenet.Conn) error {
				return nil
			}))
		}

		chained := group.Handle(echoConnHandler{})

		//: returned for chaining, so declaring a group stays one expression.
		if chained != group {
			t.Fatal("Handle did not return the group for chaining")
		}
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		//: the last handler is the one that serves.
		addr := srv.State().Listeners[0].Address
		if got := roundTrip(t, addr, "hello"); got != "hello\n" {
			t.Fatalf("echo = %q, want %q — an earlier handler is still installed", got, "hello\n")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestStreamGroup_HandleFunc pins the shorthand. The overwhelmingly common case
// is one function, and making that case require a named type is the papercut
// that decides whether the API feels light — so it has to be exactly equivalent
// to Handle rather than a second, subtly different path.
func TestStreamGroup_HandleFunc(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// line is the payload echoed through the group.
		line string
	}
	tests := []tc{
		{name: "a short line", line: "hello"},
		{name: "a longer line", line: "the quick brown fox jumps over the lazy dog"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		group := srv.Group("echo", server.Listen("tcp", "127.0.0.1:0"))

		chained := group.HandleFunc(echoHandler)

		if chained != group {
			t.Fatal("HandleFunc did not return the group for chaining")
		}
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		addr := srv.State().Listeners[0].Address
		if got := roundTrip(t, addr, c.line); got != c.line+"\n" {
			t.Fatalf("echo = %q, want %q", got, c.line+"\n")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestStreamGroup_HandleHTTP pins the split ADR 0029 D3 commits to: our unified
// listener, our limits, our TLS identity and our drain, with net/http doing the
// protocol.
//
// Reimplementing HTTP would mean owning request smuggling defences, HTTP/2 flow
// control and HPACK — to lose, not gain, throughput. What has to be true is that
// the group is still a group: it binds the same way, it is reported the same way
// in State, and it stops with the rest of the server.
func TestStreamGroup_HandleHTTP(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// path is the request target.
		path string
		// status is what the handler answers with.
		status int
		// body is the payload it writes.
		body string
	}
	tests := []tc{
		{name: "a 200 with a body", path: "/", status: http.StatusOK, body: "ok"},
		{name: "a 404", path: "/missing", status: http.StatusNotFound, body: "gone"},
		{name: "an empty body", path: "/", status: http.StatusNoContent},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		group := srv.Group("api", server.Listen("tcp", "127.0.0.1:0"))

		chained := group.HandleHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(c.status)
			if c.body != "" {
				if _, err := io.WriteString(w, c.body); err != nil {
					t.Errorf("write response: %v", err)
				}
			}
		}))

		if chained != group {
			t.Fatal("HandleHTTP did not return the group for chaining")
		}
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		addr := srv.State().Listeners[0].Address
		req, rerr := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+c.path, nil)
		if rerr != nil {
			t.Fatalf("build request: %v", rerr)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer closeOrFail(t, resp.Body)
		if resp.StatusCode != c.status {
			t.Fatalf("status = %d, want %d", resp.StatusCode, c.status)
		}
		payload, berr := io.ReadAll(resp.Body)
		if berr != nil {
			t.Fatalf("read body: %v", berr)
		}
		if string(payload) != c.body {
			t.Fatalf("body = %q, want %q", payload, c.body)
		}
		//: it is still a group of ours, counted like any other.
		if !waitFor(t, func() bool { return srv.State().TotalConns >= 1 }) {
			t.Fatal("the HTTP connection was never counted by the engine")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestStreamGroup_Use pins the middleware ORDER: the first middleware listed is
// the first to see a connection, which is the only ordering a reader will guess
// correctly from the declaration.
func TestStreamGroup_Use(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// labels name the middlewares, in declaration order.
		labels []string
		// separately applies them one Use call at a time rather than together.
		separately bool
	}
	tests := []tc{
		{name: "no middleware at all"},
		{name: "one middleware", labels: []string{"a"}},
		{name: "several in one call", labels: []string{"outer", "middle", "inner"}},
		//: repeated calls append, so the declaration reads the same either way.
		{name: "several in separate calls", labels: []string{"outer", "middle", "inner"}, separately: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		order := make(chan string, len(c.labels)+1)
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		group := srv.Group("api", server.Listen("tcp", "127.0.0.1:0"))
		group.HandleFunc(func(context.Context, corenet.Conn) error {
			order <- "handler"
			return nil
		})

		middlewares := make([]corenet.Middleware[corenet.ConnHandler], 0, len(c.labels))
		for _, label := range c.labels {
			middlewares = append(middlewares, func(next corenet.ConnHandler) corenet.ConnHandler {
				return corenet.ConnHandlerFunc(func(ctx context.Context, conn corenet.Conn) error {
					order <- label
					return next.ServeConn(ctx, conn)
				})
			})
		}
		var chained *server.StreamGroup
		if c.separately {
			for _, middleware := range middlewares {
				chained = group.Use(middleware)
			}
		} else {
			chained = group.Use(middlewares...)
		}
		if len(c.labels) > 0 && chained != group {
			t.Fatal("Use did not return the group for chaining")
		}

		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		conn, err := stdnet.DialTimeout("tcp", srv.State().Listeners[0].Address, 2*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer closeOrFail(t, conn)

		want := append(slices.Clone(c.labels), "handler")
		for i, expected := range want {
			select {
			case got := <-order:
				//: outermost first, exactly as the declaration reads.
				if got != expected {
					t.Fatalf("step %d ran %q, want %q", i, got, expected)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("the chain stopped after %d of %d steps", i, len(want))
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
