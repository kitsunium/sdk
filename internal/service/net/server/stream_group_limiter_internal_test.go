// Package server — the per-group connection ceiling.
package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	stdnet "net"
	"net/http"
	"runtime"
	"sync"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// slotWait bounds how long a ceiling test waits for a slot, a socket or a
// serve goroutine. It is a failure deadline, never a synchronisation delay:
// every wait it bounds ends the instant its condition holds.
const slotWait time.Duration = 5 * time.Second

// Test_newConnLimiter pins that ZERO means "no ceiling" rather than "a ceiling
// of zero".
//
// The difference is a working server and a dead one: a limiter built for zero
// would refuse every connection, and every group that never configured MaxConns
// is exactly that case. Returning nil also keeps the policy off the hot path
// entirely for the common group.
func Test_newConnLimiter(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// maxConns is the group's configured ceiling.
		maxConns int
		// wantLimiter is whether a ceiling must be installed.
		wantLimiter bool
	}
	tests := []tc{
		//: no ceiling, so the hot path skips the policy entirely.
		{name: "unset means no ceiling", maxConns: 0},
		{name: "a negative ceiling means no ceiling", maxConns: -1},
		{name: "a ceiling of one", maxConns: 1, wantLimiter: true},
		{name: "a large ceiling", maxConns: 4096, wantLimiter: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		limiter := newConnLimiter(corenet.LimitsValue{MaxConns: c.maxConns})

		if !c.wantLimiter {
			if limiter != nil {
				t.Fatalf("a group with MaxConns=%d got a ceiling — it would refuse "+
					"every connection", c.maxConns)
			}
			return
		}
		if limiter == nil {
			t.Fatalf("a group with MaxConns=%d got no ceiling", c.maxConns)
		}
		//: the semaphore's capacity IS the ceiling; any other size admits a
		//: different number of connections than the one the operator set.
		if got := cap(limiter.slots); got != c.maxConns {
			t.Errorf("the ceiling admits %d connections at once, want %d", got, c.maxConns)
		}
		//: the ceiling is carried alongside so a refusal can name the number it
		//: hit, which is what an operator needs to decide whether to raise it.
		if limiter.ceiling != c.maxConns {
			t.Errorf("the ceiling records %d, want %d", limiter.ceiling, c.maxConns)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Server_admit pins that a connection turned away is answered in the NET
// domain's terms and counted.
//
// Goroutine lifecycle: one goroutine per occupied slot, each blocked in a
// handler until the case's release channel closes on the way out. Every one is
// confirmed started before the admission under test, so the ceiling is genuinely
// full rather than racily so.
//
// A connection ceiling is exactly a reject-mode semaphore — the resilience
// bulkhead's shape, which it used to reuse — and the refusal is the net
// domain's own CONN_LIMIT_REACHED: a net consumer has no reason to know the
// resilience domain exists. And the refusal is counted, because RejectedConns
// is how an operator tells a saturated server from an idle one.
func Test_Server_admit(t *testing.T) {
	t.Parallel()
	failure := errors.New("handler failed")

	type tc struct {
		// name describes the case.
		name string
		// ceiling is the group's configured maximum; zero means none.
		ceiling int
		// occupy holds this many slots before the admission under test.
		occupy int
		// handlerErr is what the handler reports once admitted.
		handlerErr error
		// wantAdmitted is whether the handler must run at all.
		wantAdmitted bool
	}
	tests := []tc{
		{name: "no ceiling at all", wantAdmitted: true},
		{name: "a free slot", ceiling: 2, wantAdmitted: true},
		{name: "the last free slot", ceiling: 2, occupy: 1, wantAdmitted: true},
		//: accepted by the kernel, then refused by the ceiling.
		{name: "no slot left", ceiling: 2, occupy: 2},
		{name: "a ceiling of one, already taken", ceiling: 1, occupy: 1},
		{name: "a handler that fails", ceiling: 2, handlerErr: failure, wantAdmitted: true},
		{name: "a handler that fails with no ceiling", handlerErr: failure, wantAdmitted: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := New()
		limiter := newConnLimiter(corenet.LimitsValue{MaxConns: c.ceiling})
		release := make(chan struct{})
		defer close(release)
		held := make(chan struct{}, c.occupy)
		//: occupy slots with handlers that stay in them for the test's duration.
		for range c.occupy {
			go func() {
				swallowErr(srv.admit(t.Context(), limiter,
					&conn{Conn: &fakeSocket{}},
					corenet.ConnHandlerFunc(func(context.Context, corenet.Conn) error {
						held <- struct{}{}
						<-release
						return nil
					})))
			}()
		}
		for range c.occupy {
			select {
			case <-held:
			case <-time.After(serveDeadline):
				t.Fatal("a slot-holding handler never started")
			}
		}

		var ran bool
		err := srv.admit(t.Context(), limiter, &conn{Conn: &fakeSocket{}},
			corenet.ConnHandlerFunc(func(context.Context, corenet.Conn) error {
				ran = true
				return c.handlerErr
			}))

		if !c.wantAdmitted {
			if ran {
				t.Fatal("the handler ran for a connection past the ceiling")
			}
			//: the domain's own sentinel, so a net consumer never meets a
			//: resilience one it has no reason to know about.
			if !errs.HasCode(err, corenet.CodeConnLimitReached) {
				t.Fatalf("admit = %v, want CONN_LIMIT_REACHED", err)
			}
			//: the refusal names the ceiling it hit.
			var named bool
			for _, f := range errs.FieldsOf(err) {
				if f.Key() == "limit" {
					named = true
				}
			}
			if !named {
				t.Errorf("the refusal does not name the ceiling: %v", errs.FieldsOf(err))
			}
			//: counted, or an operator cannot see saturation at all.
			if srv.State().RejectedConns != 1 {
				t.Errorf("RejectedConns = %d, want 1", srv.State().RejectedConns)
			}
			return
		}
		if !ran {
			t.Fatal("an admitted connection never reached the handler")
		}
		//: the handler's own outcome travels back unchanged.
		if !errors.Is(err, c.handlerErr) {
			t.Fatalf("admit = %v, want %v", err, c.handlerErr)
		}
		//: an admitted connection is not a rejection.
		if srv.State().RejectedConns != 0 {
			t.Errorf("RejectedConns = %d for an admitted connection", srv.State().RejectedConns)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Server_admit_ReleasesItsSlot pins that a served connection gives its slot
// back. A ceiling that leaked slots would wedge the group after MaxConns
// connections, which is far worse than having no ceiling at all.
func Test_Server_admit_ReleasesItsSlot(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// ceiling is the group's configured maximum.
		ceiling int
		// rounds is how many connections pass through it, one after another.
		rounds int
	}
	tests := []tc{
		{name: "a ceiling of one", ceiling: 1, rounds: 20},
		{name: "a ceiling of four", ceiling: 4, rounds: 20},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := New()
		limiter := newConnLimiter(corenet.LimitsValue{MaxConns: c.ceiling})
		handler := corenet.ConnHandlerFunc(func(context.Context, corenet.Conn) error { return nil })

		//: strictly sequential, so every slot is free again before the next
		//: connection asks for one — a leak wedges the group well before the end.
		for i := range c.rounds {
			if err := srv.admit(t.Context(), limiter, &conn{Conn: &fakeSocket{}}, handler); err != nil {
				t.Fatalf("connection %d of %d was refused under a ceiling of %d: %v — "+
					"a slot leaked", i, c.rounds, c.ceiling, err)
			}
		}
		if rejected := srv.State().RejectedConns; rejected != 0 {
			t.Fatalf("RejectedConns = %d for sequential traffic under a ceiling of %d, want 0",
				rejected, c.ceiling)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Server_admit_HijackedConnectionKeepsItsSlot pins the whole life of a
// hijacked connection's slot under a ceiling of one: while the connection is
// open, admission refuses with CONN_LIMIT_REACHED; once its handler closes
// it, the slot comes back and a new connection is served.
//
// The slot used to be held only while ServeConn ran, and ServeConn returns at
// the hijack — so every upgraded WebSocket gave its slot back while it stayed
// open, and MaxConns bounded nothing an upgrade reached. The refusal is asked
// of admit directly, so it is the domain's own code that is checked and not
// merely a closed socket.
//
// The TLS case is not a repeat. On a TLS listener the tracker sits UNDER the
// *tls.Conn net/http hands the handler, so the slot comes back only if
// tls.Conn.Close reaches it, and it is held at all only if the engine looks
// beneath the TLS layer to find it.
//
// Nothing here waits on the clock. The engine's serve goroutine is awaited
// through ActiveConns before the refusal is asked for, and the slot's return
// through the semaphore itself — a claim that completes the instant Close
// gives the slot back. The deadlines only bound a failure.
//
// MUTATION-CHECKED, four ways. Returning every slot when ServeConn returns —
// the defect — fails both cases with:
//
//	admit = <nil> (handler ran: true) while a hijacked connection held the only slot, want CONN_LIMIT_REACHED
//
// a trackedConn.Close that never returns the slot fails both with:
//
//	the slot never came back within 5s after the hijacked connection closed
//
// a hijackedTracker that does not look beneath the TLS layer fails the TLS
// case alone, with the admit message above; and a listener that puts the
// tracker ABOVE TLS instead of under it fails the TLS case with:
//
//	the hijacking handler answered "HELD plain\n", want "HELD tls\n"
func Test_Server_admit_HijackedConnectionKeepsItsSlot(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// secured serves the group over TLS, where the tracker is under the
		// *tls.Conn rather than being the socket net/http sees.
		secured bool
		// held is what the hijacking handler answers, which says whether
		// net/http saw the request as TLS.
		held string
	}
	tests := []tc{
		{name: "a plaintext listener", held: "HELD plain\n"},
		{name: "a TLS listener", secured: true, held: "HELD tls\n"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		release := make(chan struct{})
		closed := make(chan struct{})
		releaseHandler := sync.OnceFunc(func() { close(release) })
		//: registered before the server, so it runs after the server is gone
		//: and still frees a handler an early failure left holding its socket.
		t.Cleanup(func() {
			releaseHandler()
			select {
			case <-closed:
			case <-time.After(slotWait):
			}
		})
		srv := newTestServer(t)
		opts := []GroupOption{Listen("tcp", "127.0.0.1:0"), MaxConns(1)}
		if c.secured {
			opts = append(opts, TLS(testIdentity(t)))
		}
		group := srv.Group("api", opts...).HandleHTTP(holdingMux(t, release, closed))
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		addr := srv.State().Listeners[0].Address
		if line := askCeilinged(t, addr, c.secured, "/take"); line != c.held {
			t.Fatalf("the hijacking handler answered %q, want %q", line, c.held)
		}
		//: the engine is done with the hijacked connection, so whatever it
		//: does with the slot, it has done.
		awaitNoActiveConns(t, srv)

		var ran bool
		err := srv.admit(t.Context(), group.limiter, &conn{Conn: &fakeSocket{}},
			corenet.ConnHandlerFunc(func(context.Context, corenet.Conn) error {
				ran = true
				return nil
			}))
		if ran || !errs.HasCode(err, corenet.CodeConnLimitReached) {
			t.Fatalf("admit = %v (handler ran: %v) while a hijacked connection held the only slot, "+
				"want CONN_LIMIT_REACHED", err, ran)
		}

		releaseHandler()
		select {
		case <-closed:
		case <-time.After(slotWait):
			t.Fatalf("the hijacking handler never closed its socket within %s", slotWait)
		}
		//: the slot comes back when the socket closes — awaited by claiming it.
		select {
		case group.limiter.slots <- struct{}{}:
			group.limiter.release()
		case <-time.After(slotWait):
			t.Fatalf("the slot never came back within %s after the hijacked connection closed", slotWait)
		}
		if line := askCeilinged(t, addr, c.secured, "/ok"); line != "HTTP/1.1 200 OK\r\n" {
			t.Fatalf("a connection after the hijacked one closed got %q, want %q", line, "HTTP/1.1 200 OK\r\n")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// holdingMux serves /take by hijacking the connection, writing one line on it
// and holding it until release closes, then closing it and closing closed; and
// /ok with an empty 200.
//
// The line says whether the request arrived with Request.TLS set, because a
// tracker placed above the TLS layer would still carry HTTP — tls.Conn
// handshakes on its first Read — while hiding the *tls.Conn net/http asserts
// on, and every HTTPS request would arrive looking like plaintext.
func holdingMux(t *testing.T, release <-chan struct{}, closed chan<- struct{}) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/take", func(w http.ResponseWriter, r *http.Request) {
		defer close(closed)
		held := "HELD plain\n"
		if r.TLS != nil {
			held = "HELD tls\n"
		}
		socket, buffered, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		//: proof for the client that the connection is now the handler's.
		if _, werr := buffered.WriteString(held); werr != nil {
			t.Errorf("write: %v", werr)
		}
		if ferr := buffered.Flush(); ferr != nil {
			t.Errorf("flush: %v", ferr)
		}
		<-release
		if cerr := socket.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})
	mux.HandleFunc("/ok", func(http.ResponseWriter, *http.Request) {})
	//: both routes the test drives.
	return mux
}

// askCeilinged sends one GET for path on a fresh connection to addr, over TLS
// when secured, and returns the first line of whatever comes back.
func askCeilinged(t *testing.T, addr string, secured bool, path string) string {
	t.Helper()
	var (
		socket stdnet.Conn
		err    error
	)
	if secured {
		socket, err = tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // a self-signed test identity
	} else {
		socket, err = stdnet.Dial("tcp", addr)
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	//: closed when the test ends, so a hijacked connection stays open until the
	//: handler, not the client, decides it is over.
	t.Cleanup(func() { swallowErr(socket.Close()) })
	if derr := socket.SetDeadline(time.Now().Add(slotWait)); derr != nil {
		t.Fatalf("deadline: %v", derr)
	}
	if _, werr := io.WriteString(socket, "GET "+path+" HTTP/1.1\r\nHost: x\r\n\r\n"); werr != nil {
		t.Fatalf("write: %v", werr)
	}
	line, rerr := bufio.NewReader(socket).ReadString('\n')
	if rerr != nil {
		t.Fatalf("read: %v", rerr)
	}
	//: the status line, or the hijacking handler's own line.
	return line
}

// awaitNoActiveConns returns once the server reports no connection being
// served — once every serve goroutine has finished, since ActiveConns drops
// only after a connection's slot and socket are settled.
//
// It yields rather than sleeps: the condition, not an interval, ends the wait,
// and slotWait only bounds a failure.
func awaitNoActiveConns(t *testing.T, srv *Server) {
	t.Helper()
	deadline := time.Now().Add(slotWait)
	for srv.State().ActiveConns != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("ActiveConns = %d after %s, want 0 — a serve goroutine never returned",
				srv.State().ActiveConns, slotWait)
		}
		runtime.Gosched()
	}
}
