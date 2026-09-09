// Package server_test — what happens to a connection a handler takes over.
//
// The engine closes every connection it serves on the way out, which is right
// for every request/response handler and wrong for exactly one case: a handler
// that hijacks the response and keeps speaking on the socket. net/http reports
// StateHijacked the instant Hijack is called, the engine's ServeConn returns on
// that report, and the deferred close then severed a connection the handler had
// only just been handed.
//
// It was reachable with nothing but net/http — no SDK protocol involved — and
// nothing in the suite covered it, because no test had ever hijacked.
package server_test

import (
	"bufio"
	"context"
	stdnet "net"
	"net/http"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/service/net/server"
)

// hijackDeadline bounds the test's own reads so a severed socket fails the test
// instead of hanging the suite.
const hijackDeadline time.Duration = 5 * time.Second

// startHijackServer mounts h on a real listener and returns its address.
func startHijackServer(t *testing.T, h http.Handler) (srv *server.Server, addr string) {
	t.Helper()
	srv = server.New()
	srv.Group("api", server.Listen("tcp", "127.0.0.1:0")).HandleHTTP(h)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })
	//: the address actually bound, not the :0 requested.
	return srv, srv.State().Listeners[0].Address
}

// upgradeRequest writes a bare HTTP request that the handler will hijack.
func upgradeRequest(t *testing.T, addr string) (conn stdnet.Conn, reader *bufio.Reader) {
	t.Helper()
	conn, err := stdnet.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, conn) })
	if derr := conn.SetDeadline(time.Now().Add(hijackDeadline)); derr != nil {
		t.Fatalf("deadline: %v", derr)
	}
	if _, werr := conn.Write([]byte("GET /take HTTP/1.1\r\nHost: x\r\n\r\n")); werr != nil {
		t.Fatalf("write: %v", werr)
	}
	//: the connection and a reader over it.
	return conn, bufio.NewReader(conn)
}

// TestAHijackedConnectionSurvivesTheEngine is the regression guard for a defect
// that made every upgrade-based protocol impossible on this engine.
//
// The handler waits before writing, which is the whole test: the engine's
// ServeConn has already returned by then — net/http reported StateHijacked
// synchronously from inside Hijack — so if the engine still closed what it
// served, the read below reports EOF instead of the line the handler wrote.
//
// It is written with net/http alone. The defect was never about WebSocket; it
// was about ownership, and any handler that hijacks hit it identically.
func TestAHijackedConnectionSurvivesTheEngine(t *testing.T) {
	t.Parallel()
	taken := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/take", func(w http.ResponseWriter, _ *http.Request) {
		socket, buffered, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			close(taken)
			return
		}
		close(taken)
		//: the pause is load-bearing: without it the write could win a race
		//: against the engine's close and the test would pass on a broken
		//: engine roughly half the time.
		time.Sleep(200 * time.Millisecond)
		if _, werr := buffered.WriteString("STILL HERE\n"); werr != nil {
			t.Errorf("write: %v", werr)
		}
		if ferr := buffered.Flush(); ferr != nil {
			t.Errorf("flush: %v", ferr)
		}
		closeOrFail(t, socket)
	})
	_, addr := startHijackServer(t, mux)
	_, reader := upgradeRequest(t, addr)
	<-taken
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("the engine severed a hijacked connection: %v", err)
	}
	if line != "STILL HERE\n" {
		t.Fatalf("read %q, want %q", line, "STILL HERE\n")
	}
}

// TestAHijackedConnectionDoesNotHoldTheDrain pins the other half of the same
// ownership rule, and states the consequence out loud.
//
// A hijacked connection is not waited for, exactly as net/http's own Shutdown
// documents ("Shutdown does not attempt to close nor wait for hijacked
// connections such as WebSockets"). The drain SIGNAL is how such a handler is
// told to finish; the budget is not spent waiting to find out whether it did.
func TestAHijackedConnectionDoesNotHoldTheDrain(t *testing.T) {
	t.Parallel()
	taken := make(chan struct{})
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/take", func(w http.ResponseWriter, _ *http.Request) {
		socket, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			close(taken)
			return
		}
		close(taken)
		//: the handler outlives the shutdown below, which is precisely the
		//: shape that must not cost the drain its budget.
		<-release
		closeOrFail(t, socket)
	})
	srv, addr := startHijackServer(t, mux)
	_, _ = upgradeRequest(t, addr)
	<-taken

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started := time.Now()
	err := srv.Shutdown(ctx)
	elapsed := time.Since(started)
	close(release)
	if err != nil {
		t.Fatalf("shutdown reported %v, want a clean drain", err)
	}
	//: a budget-length drain would mean the engine was still waiting on a
	//: connection it no longer owns.
	if elapsed > 2*time.Second {
		t.Fatalf("shutdown took %v; a hijacked connection is not waited for", elapsed)
	}
}
