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
	"runtime"
	"sync"
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

// awaitNoActiveConns returns once the server reports no connection being
// served — once every serve goroutine has finished, since ActiveConns drops
// only after a connection's slot and socket are settled.
//
// It yields rather than sleeps: the condition, not an interval, is what ends
// the wait, and hijackDeadline only bounds a failure.
func awaitNoActiveConns(t *testing.T, srv *server.Server) {
	t.Helper()
	deadline := time.Now().Add(hijackDeadline)
	for srv.State().ActiveConns != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("ActiveConns = %d after %s, want 0 — a serve goroutine never returned",
				srv.State().ActiveConns, hijackDeadline)
		}
		runtime.Gosched()
	}
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

// TestAHijackedConnectionStillCountsAgainstTheCeiling pins the consequence of a
// hijack that ADR 0047 §D9 did not list: a connection a handler took over is no
// longer the engine's to close or to wait for, but it is still a connection —
// and MaxConns caps connections.
//
// The ceiling's slot used to be held only while ServeConn ran, and ServeConn
// returns at the hijack, so every upgraded WebSocket handed its slot back while
// it stayed open and MaxConns bounded nothing an upgrade reached. With a
// ceiling of one and a hijacked connection live, a second connection must be
// refused — closed unanswered and counted — rather than served.
//
// The same run pins that holding the slot changed neither half of §D9. Shutdown
// still returns at once instead of waiting on the hijacked connection. And the
// socket is still the handler's afterwards: the handler writes only once
// Shutdown has returned, and a clean Shutdown returns only after every serve
// goroutine the engine ran has finished — so an engine that closed the socket
// would have done it by then, with no sleep standing in for that ordering.
//
// It is written with net/http alone, like the two tests above: the defect is
// about ownership, and a WebSocket is only the most common hijacker.
//
// MUTATION-CHECKED. Against the pre-fix engine — and equally with releaseFor
// returning every slot the moment ServeConn returns — it fails 20 runs out of
// 20 with:
//
//	the second connection was served ("HTTP/1.1 404 Not Found\r\nContent-Type: text/plain; charset=utf-8\r") while a hijacked connection held the only slot
func TestAHijackedConnectionStillCountsAgainstTheCeiling(t *testing.T) {
	t.Parallel()
	taken := make(chan struct{})
	proceed := make(chan struct{})
	finished := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/take", func(w http.ResponseWriter, _ *http.Request) {
		defer close(finished)
		socket, buffered, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			close(taken)
			return
		}
		close(taken)
		//: the handler keeps the connection until the test has checked the
		//: ceiling and drained the server around it.
		<-proceed
		if _, werr := buffered.WriteString("STILL HERE\n"); werr != nil {
			t.Errorf("write: %v", werr)
		}
		if ferr := buffered.Flush(); ferr != nil {
			t.Errorf("flush: %v", ferr)
		}
		closeOrFail(t, socket)
	})
	srv := server.New()
	srv.Group("api", server.Listen("tcp", "127.0.0.1:0"), server.MaxConns(1)).HandleHTTP(mux)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })
	addr := srv.State().Listeners[0].Address
	_, reader := upgradeRequest(t, addr)
	proceedHandler := sync.OnceFunc(func() { close(proceed) })
	//: registered after the client's socket, so it runs before that socket
	//: closes: an early failure still lets the handler finish instead of
	//: leaving it parked on proceed for the rest of the binary's life.
	t.Cleanup(func() {
		proceedHandler()
		select {
		case <-finished:
		case <-time.After(hijackDeadline):
		}
	})
	<-taken
	//: the engine's serve goroutine returns at the hijack, concurrently with
	//: the handler; whatever it does with the slot, it has done once it is
	//: gone. Without this the check below would race it, and a ceiling that
	//: still gave the slot back could pass whenever the dial won.
	awaitNoActiveConns(t, srv)

	//: a whole request, so that a connection the ceiling wrongly admits is
	//: ANSWERED — a 404 from the mux — rather than merely left waiting.
	second, err := stdnet.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial second: %v", err)
	}
	defer closeOrFail(t, second)
	if derr := second.SetDeadline(time.Now().Add(hijackDeadline)); derr != nil {
		t.Fatalf("deadline: %v", derr)
	}
	_, werr := second.Write([]byte("GET /other HTTP/1.1\r\nHost: x\r\n\r\n"))
	answer := make([]byte, 64)
	n, rerr := second.Read(answer)
	//: a refused connection is closed unanswered: the write or the read fails.
	if werr == nil && rerr == nil {
		t.Fatalf("the second connection was served (%q) while a hijacked connection held the only slot",
			answer[:n])
	}
	//: counted, or an operator cannot see that the ceiling is full.
	if got := srv.State().RejectedConns; got != 1 {
		t.Fatalf("RejectedConns = %d, want 1", got)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started := time.Now()
	if serr := srv.Shutdown(ctx); serr != nil {
		t.Fatalf("shutdown reported %v, want a clean drain", serr)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("shutdown took %v; a hijacked connection under a ceiling is still not waited for", elapsed)
	}

	proceedHandler()
	line, lerr := reader.ReadString('\n')
	if lerr != nil {
		t.Fatalf("the engine severed a hijacked connection under a ceiling: %v", lerr)
	}
	if line != "STILL HERE\n" {
		t.Fatalf("read %q, want %q", line, "STILL HERE\n")
	}
	<-finished
}
