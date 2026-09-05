// Package server — the net/http adapter.
package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	stdnet "net"
	"net/http"
	"sync"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/worker"
)

// httpDaemonSettleTimeout bounds how long a test waits for the adapter's
// net/http goroutine to exit. It is generous on purpose: the assertion is
// "it terminates at all", not "it terminates quickly".
const httpDaemonSettleTimeout time.Duration = 3 * time.Second

// servePhase names what happens around a connection's arrival at the adapter.
type servePhase int

const (
	// servePhaseServed lets net/http serve the connection to completion.
	servePhaseServed servePhase = iota
	// servePhaseStopped shuts the adapter down before the connection arrives.
	servePhaseStopped
	// servePhaseDraining cancels the caller's context while the hand-off is
	// still parked.
	servePhaseDraining
)

// httpAdapter is the engine's ConnHandler for an http.Handler group; the
// assertion is what keeps a signature change from silently detaching it.
var _ corenet.ConnHandler = (*httpAdapter)(nil)

// startHTTPGroup starts a server serving one http.Handler and returns it with
// its adapter and bound address, after one request has forced the adapter to
// launch its net/http goroutine.
func startHTTPGroup(t *testing.T) (srv *Server, adapter *httpAdapter, addr string) {
	t.Helper()
	srv = New()
	group := srv.Group("api", Listen("tcp", "127.0.0.1:0"))
	group.HandleHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		//: a dropped write would make the request look served when it was not.
		if _, err := io.WriteString(w, "ok"); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	addr = srv.State().Listeners[0].Address
	//: one real request is what makes the adapter build its bridge and start
	//: the goroutine whose lifetime this file is about.
	req, rerr := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/", nil)
	if rerr != nil {
		t.Fatalf("build request: %v", rerr)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, cerr := io.Copy(io.Discard, resp.Body); cerr != nil {
		t.Fatalf("drain body: %v", cerr)
	}
	if cerr := resp.Body.Close(); cerr != nil {
		t.Fatalf("close body: %v", cerr)
	}
	//: the adapter must actually have launched, or the test proves nothing.
	if launchedDaemon(group.httpAdapter) == nil {
		t.Fatal("the adapter served a request without launching its net/http goroutine")
	}
	return srv, group.httpAdapter, addr
}

// launchedDaemon reads the goroutine owner the adapter launched, if any. The
// read takes the adapter's own lock rather than adding an accessor to the
// production type for a test's benefit.
func launchedDaemon(a *httpAdapter) *worker.LoopDaemon {
	a.mu.RLock()
	defer a.mu.RUnlock()
	//: nil means launch refused, so there is no goroutine to account for.
	return a.daemon
}

// closeQuietly closes c and reports a failure rather than discarding it.
func closeQuietly(t *testing.T, c io.Closer) {
	t.Helper()
	if err := c.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

// Test_newHTTPAdapter pins that the bridge is built LAZILY.
//
// It cannot be built at declaration: the bridge reports an address to
// http.Server, and the only honest one is the address a real connection was
// accepted on — which is not known until one arrives. Building it early would
// mean inventing an address, and http.Server logs it.
func Test_newHTTPAdapter(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// nilHandler leaves the application handler unset.
		nilHandler bool
	}
	tests := []tc{
		{name: "an application handler"},
		//: net/http substitutes its own default for a nil handler, so this is
		//: not a case the adapter has to refuse.
		{name: "no handler at all", nilHandler: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var handler http.Handler
		if !c.nilHandler {
			//: a comparable handler type, so the assertion below can be identity
			//: rather than a proxy for it.
			handler = markerHandler{}
		}

		adapter := newHTTPAdapter(handler)

		if adapter.handler != handler {
			t.Fatal("the adapter does not hold the handler it was given")
		}
		//: built on first use, when a real connection reveals which listener
		//: accepted it.
		if adapter.bridge != nil || adapter.server != nil || adapter.daemon != nil {
			t.Error("the adapter built its bridge before any connection arrived")
		}
		//: the waiter table exists from the start, because register runs before
		//: the hand-off and must never find a nil map.
		if adapter.waiters == nil {
			t.Error("the adapter has no waiter table")
		}
		if adapter.stopped {
			t.Error("a fresh adapter is already stopped")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// markerHandler is a comparable http.Handler, so a test can assert the adapter
// holds the very handler it was given rather than an equivalent one.
type markerHandler struct{}

// ServeHTTP implements http.Handler.
func (markerHandler) ServeHTTP(http.ResponseWriter, *http.Request) {
	//: nothing under test drives a request through this handler.
}

// Test_rawSocket pins that net/http receives the CONCRETE socket.
//
// net/http type-asserts on *tls.Conn to populate Request.TLS. Handing it our
// pooled wrapper would make every request on an HTTPS listener arrive looking
// like plaintext — which is exactly the shape a handler checks to decide whether
// a caller was authenticated.
func Test_rawSocket(t *testing.T) {
	t.Parallel()
	plain := &fakeSocket{}
	secured := tls.Server(plain, &tls.Config{MinVersion: tls.VersionTLS12})

	type tc struct {
		// name describes the case.
		name string
		// given is the connection the engine hands the adapter.
		given corenet.Conn
		// want is the socket net/http must receive.
		want stdnet.Conn
	}
	tests := []tc{
		{name: "a pooled plaintext connection", given: &conn{Conn: plain}, want: plain},
		//: the concrete type is what net/http asserts on to populate Request.TLS.
		{name: "a pooled TLS connection", given: &conn{Conn: secured}, want: secured},
		//: a wrapper the pool has already reset is handed back as itself rather
		//: than as a nil socket.
		{name: "a reclaimed wrapper", given: &conn{}, want: &conn{}},
		{name: "a connection that is not pooled at all", given: &notPooled{}, want: &notPooled{}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := rawSocket(c.given)

		if got == nil {
			t.Fatal("rawSocket returned no socket at all")
		}
		pooled, isPooled := c.given.(*conn)
		//: a non-pooled implementation, or one the pool has reset, is already
		//: the socket itself.
		if !isPooled || pooled.Conn == nil {
			if got != stdnet.Conn(c.given) {
				t.Fatalf("rawSocket = %T, want the connection itself", got)
			}
			return
		}
		if got != c.want {
			t.Fatalf("rawSocket = %T, want %T — net/http would see a plaintext "+
				"connection where a TLS one was accepted", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// notPooled is a corenet.Conn the engine did not mint, which rawSocket must hand
// back untouched.
type notPooled struct {
	stdnet.Conn
}

// ID implements corenet.Conn.
func (n *notPooled) ID() uint64 {
	//: no identifier; nothing under test reads it.
	return 0
}

// Group implements corenet.Conn.
func (n *notPooled) Group() string {
	//: no group; nothing under test reads it.
	return ""
}

// Buffer implements corenet.Conn.
func (n *notPooled) Buffer() []byte {
	//: no scratch buffer was ever requested for this connection.
	return nil
}

// Test_httpAdapter_register pins that the completion channel exists BEFORE the
// hand-off.
//
// net/http can reach a terminal ConnState the instant it takes a connection, so
// a waiter registered afterwards would be registered for a connection net/http
// has already finished with — and the engine goroutine holding it would then
// wait for a signal that has already been sent.
func Test_httpAdapter_register(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// count is how many connections are registered at once.
		count int
	}
	tests := []tc{
		{name: "one connection", count: 1},
		{name: "several connections", count: 16},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		adapter := newHTTPAdapter(http.NotFoundHandler())
		sockets := make([]stdnet.Conn, 0, c.count)
		channels := make([]chan struct{}, 0, c.count)

		for range c.count {
			socket := &fakeSocket{}
			sockets = append(sockets, socket)
			channels = append(channels, adapter.register(socket))
		}

		adapter.mu.RLock()
		waiting := len(adapter.waiters)
		adapter.mu.RUnlock()
		if waiting != c.count {
			t.Fatalf("%d connections registered, want %d", waiting, c.count)
		}
		for i, done := range channels {
			//: open, or the engine goroutine would return before net/http had
			//: finished with the connection.
			select {
			case <-done:
				t.Fatalf("the channel for connection %d is already closed", i)
			default:
			}
			//: each connection has its OWN channel; sharing one would release
			//: every waiter as soon as any connection finished.
			adapter.mu.RLock()
			registered := adapter.waiters[sockets[i]]
			adapter.mu.RUnlock()
			if registered != done {
				t.Fatalf("connection %d is registered against a different channel", i)
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

// Test_httpAdapter_unregister pins that a finished connection leaves the table.
//
// The table is keyed by connection and lives for the group's lifetime, so an
// entry that is never removed is a leak per connection served — of the map entry
// and of the socket it keys on, which the pool would otherwise have reclaimed.
func Test_httpAdapter_unregister(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// registered registers the connection first.
		registered bool
		// twice unregisters a second time.
		twice bool
	}
	tests := []tc{
		{name: "a registered connection", registered: true},
		//: ServeConn defers this, and onConnState may already have removed the
		//: entry, so a second removal is the expected case rather than a fault.
		{name: "a connection already removed", registered: true, twice: true},
		{name: "a connection that was never registered"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		adapter := newHTTPAdapter(http.NotFoundHandler())
		socket := &fakeSocket{}
		if c.registered {
			adapter.register(socket)
		}

		adapter.unregister(socket)
		if c.twice {
			adapter.unregister(socket)
		}

		adapter.mu.RLock()
		_, present := adapter.waiters[socket]
		waiting := len(adapter.waiters)
		adapter.mu.RUnlock()
		if present || waiting != 0 {
			t.Fatalf("the connection is still registered (%d entries left)", waiting)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_httpAdapter_onConnState pins which states mean "net/http is FINISHED".
//
// Idle and active are keep-alive transitions: releasing the engine goroutine on
// one of those would let the pool reclaim a connection net/http is still serving
// the next request on. Only closed and hijacked are terminal — and the entry is
// deleted under the lock so a second terminal state cannot close the channel
// twice, which is a panic on net/http's own goroutine.
func Test_httpAdapter_onConnState(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// states are the transitions net/http reports, in order.
		states []http.ConnState
		// registered registers the connection first.
		registered bool
		// wantReleased is whether the engine goroutine must be released.
		wantReleased bool
	}
	tests := []tc{
		{name: "a new connection", states: []http.ConnState{http.StateNew}, registered: true},
		{
			//: the connection is being reused for another request.
			name: "a keep-alive cycle", registered: true,
			states: []http.ConnState{http.StateNew, http.StateActive, http.StateIdle},
		},
		{
			name: "a closed connection", registered: true, wantReleased: true,
			states: []http.ConnState{http.StateNew, http.StateActive, http.StateClosed},
		},
		{
			//: a hijacked connection belongs to the handler now, and net/http
			//: will never report anything else about it.
			name: "a hijacked connection", registered: true, wantReleased: true,
			states: []http.ConnState{http.StateActive, http.StateHijacked},
		},
		{
			//: closing a closed channel is a panic on net/http's goroutine.
			name: "two terminal states", registered: true, wantReleased: true,
			states: []http.ConnState{http.StateClosed, http.StateClosed},
		},
		{
			//: ServeConn may already have unregistered it on the drain path.
			name:   "a connection nobody is waiting on",
			states: []http.ConnState{http.StateClosed},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		adapter := newHTTPAdapter(http.NotFoundHandler())
		socket := &fakeSocket{}
		var done chan struct{}
		if c.registered {
			done = adapter.register(socket)
		}

		for _, state := range c.states {
			adapter.onConnState(socket, state)
		}

		if !c.registered {
			//: reaching here at all is the property: an unknown connection must
			//: not panic on a nil channel.
			return
		}
		select {
		case <-done:
			if !c.wantReleased {
				t.Fatal("a keep-alive transition released the engine goroutine — " +
					"the pool would reclaim a connection net/http is still serving")
			}
		default:
			if c.wantReleased {
				t.Fatal("a terminal state did not release the engine goroutine")
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

// Test_httpAdapter_stop pins that MARKING and READING happen together.
//
// A launch racing this call must either happen entirely before it — and be in
// the snapshot — or see stopped and refuse. Anything in between leaves a
// goroutine nobody owns, because the engine deliberately does not track this one
// in its WaitGroup.
func Test_httpAdapter_stop(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// launched starts the adapter's server first.
		launched bool
		// twice stops the adapter a second time.
		twice bool
	}
	tests := []tc{
		{name: "an adapter that never saw a connection"},
		{name: "an adapter that launched", launched: true},
		//: Shutdown and Close can both run, in either order.
		{name: "an adapter stopped twice", launched: true, twice: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		adapter := newHTTPAdapter(http.NotFoundHandler())
		if c.launched {
			local, remote := stdnet.Pipe()
			defer closeQuietly(t, local)
			defer closeQuietly(t, remote)
			adapter.launch(local)
		}

		bridge, srv, daemon := adapter.stop()
		if c.twice {
			bridge, srv, daemon = adapter.stop()
		}

		adapter.mu.RLock()
		stopped := adapter.stopped
		adapter.mu.RUnlock()
		//: marked, so a launch arriving afterwards refuses.
		if !stopped {
			t.Fatal("stop did not mark the adapter, so a later connection would " +
				"start a goroutine nothing owns")
		}
		if !c.launched {
			//: nothing was ever started, so there is nothing to stop.
			if bridge != nil || srv != nil || daemon != nil {
				t.Fatalf("stop reported %v/%v/%v for an adapter that never launched",
					bridge, srv, daemon)
			}
			//: and a launch after the mark must refuse.
			local, remote := stdnet.Pipe()
			defer closeQuietly(t, local)
			defer closeQuietly(t, remote)
			adapter.launch(local)
			if launchedDaemon(adapter) != nil {
				t.Fatal("a connection arriving after stop started a server anyway")
			}
			return
		}
		//: whatever was launched before this point is in the snapshot, so the
		//: caller can close it.
		if bridge == nil || srv == nil || daemon == nil {
			t.Fatalf("stop reported %v/%v/%v for a launched adapter", bridge, srv, daemon)
		}
		swallowErr(bridge.Close())
		swallowErr(srv.Close())
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_httpAdapter_currentBridge pins the signal ServeConn reads to tell "no
// server yet" from "no server ever".
//
// A nil bridge after launch means launch refused because the adapter is already
// stopped, and ServeConn turns that into ServerClosed so the engine reclaims the
// connection now rather than parking on a hand-off nothing will take.
func Test_httpAdapter_currentBridge(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// phase is what happens around the adapter's first connection.
		phase servePhase
		// wantBridge is whether a bridge must be reported.
		wantBridge bool
	}
	tests := []tc{
		{name: "an adapter that never saw a connection", phase: servePhaseDraining},
		{name: "an adapter that launched", phase: servePhaseServed, wantBridge: true},
		//: launch refuses after stop, so there is no bridge to report.
		{name: "an adapter stopped before its first connection", phase: servePhaseStopped},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		adapter := newHTTPAdapter(http.NotFoundHandler())
		if c.phase == servePhaseStopped {
			adapter.stop()
		}
		//: the "never saw a connection" case is the one that does not launch.
		if c.phase != servePhaseDraining {
			local, remote := stdnet.Pipe()
			defer closeQuietly(t, local)
			defer closeQuietly(t, remote)
			adapter.launch(local)
		}

		bridge := adapter.currentBridge()

		if (bridge != nil) != c.wantBridge {
			t.Fatalf("currentBridge = %v, want a bridge = %v", bridge, c.wantBridge)
		}
		if bridge != nil {
			swallowErr(bridge.Close())
			adapter.closeNow()
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_httpAdapter_ServeConn pins that a connection nothing will serve is
// RECLAIMED rather than parked.
//
// The bridge is unbuffered, so a hand-off waits for http.Server to be ready. If
// the group drains first, nothing will ever take it — and without the
// ServerClosed answer the engine goroutine holding that connection would block
// for the life of the process, with its in-flight token, its registry entry and
// its slot under the group's ceiling.
//
// Goroutine lifecycle: the served case starts one goroutine to drive an HTTP
// request over the pipe; it ends with the request, and the pipe closes with the
// case.
func Test_httpAdapter_ServeConn(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// phase is what happens around the connection's arrival.
		phase servePhase
		// wantErr is the answer the engine must be given.
		wantErr error
	}
	tests := []tc{
		{name: "a connection net/http serves", phase: servePhaseServed},
		//: launch refused, so there is no server to hand it to.
		{name: "an adapter already shut down", phase: servePhaseStopped, wantErr: corenet.ServerClosed},
		//: draining — do not hold the drain open on an idle keep-alive.
		{name: "a drain while the hand-off waits", phase: servePhaseDraining},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		adapter := newHTTPAdapter(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if _, err := io.WriteString(w, "ok"); err != nil {
				t.Errorf("write response: %v", err)
			}
		}))
		t.Cleanup(adapter.closeNow)
		local, remote := stdnet.Pipe()
		t.Cleanup(func() { closeQuietly(t, remote) })
		if c.phase == servePhaseStopped {
			adapter.stop()
		}

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		if c.phase == servePhaseDraining {
			//: nothing drives the peer, so net/http parks on the request read
			//: and the engine goroutine parks on the completion channel.
			go func() {
				time.Sleep(50 * time.Millisecond)
				cancel()
			}()
		}
		if c.phase == servePhaseServed {
			go driveRequest(t, remote)
		}

		err := adapter.ServeConn(ctx, &conn{Conn: local})

		if !errors.Is(err, c.wantErr) {
			t.Fatalf("ServeConn = %v, want %v", err, c.wantErr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// driveRequest speaks one HTTP request over the peer end of a pipe and reads the
// answer, so net/http reaches a terminal state for that connection.
func driveRequest(t *testing.T, peer stdnet.Conn) {
	t.Helper()
	//: Connection: close is what makes net/http finish with the connection
	//: rather than hold it open for another request.
	if _, err := io.WriteString(peer,
		"GET / HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n"); err != nil {
		return
	}
	resp, err := http.ReadResponse(bufio.NewReader(peer), nil)
	//: the assertion belongs to the case; this end only has to complete the
	//: exchange so net/http closes the connection.
	if err != nil {
		return
	}
	swallowErr(resp.Body.Close())
}

// Test_httpAdapter_launch pins the window between "shutdown decided there was
// nothing to stop" and "a connection started a server anyway".
//
// shutdown read a.bridge, found nil, and returned. A connection already on its
// way through ServeConn then ran launch, built a bridge and an http.Server, and
// started a goroutine that nothing would ever close — the engine does not track
// it in its WaitGroup, deliberately, so Shutdown reported a clean drain while
// the daemon ran on. The two goroutines also touched a.bridge, a.server and
// a.daemon with no synchronisation at all, which -race reports directly.
//
// Goroutine lifecycle: exactly two per round, both joined before the round
// returns. They exist to put launch and shutdown on different goroutines, which
// is the only way to reach the window between them.
func Test_httpAdapter_launch(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// rounds is how many times the race is driven.
		rounds int
	}
	tests := []tc{
		{name: "launch against shutdown", rounds: 200},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		for range c.rounds {
			runLaunchShutdownRace(t)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// runLaunchShutdownRace drives one launch against one shutdown and asserts that
// whatever was started was also stopped.
//
// Goroutine lifecycle: exactly two, both owned by this function and joined on
// the WaitGroup before it returns.
func runLaunchShutdownRace(t *testing.T) {
	t.Helper()
	adapter := newHTTPAdapter(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	local, remote := stdnet.Pipe()
	defer closeQuietly(t, local)
	defer closeQuietly(t, remote)

	ctx, cancel := context.WithTimeout(t.Context(), httpDaemonSettleTimeout)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		adapter.shutdown(ctx)
	}()
	go func() {
		defer wg.Done()
		//: exactly what ServeConn does on the first connection of a group.
		adapter.start.Do(func() { adapter.launch(local) })
	}()
	wg.Wait()

	daemon := launchedDaemon(adapter)
	//: refusing to launch after shutdown is a correct outcome; there is then
	//: no goroutine to account for.
	if daemon == nil {
		return
	}
	select {
	//: a daemon that was started must also have been stopped.
	case <-daemon.Done():
	case <-time.After(httpDaemonSettleTimeout):
		t.Fatal("shutdown returned while a net/http goroutine was starting, and " +
			"nothing will ever close it — the adapter leaked a server per group")
	}
}

// Test_httpAdapter_shutdown pins that the DRAINING path ends the adapter's
// net/http goroutine, and waits for it within the caller's budget.
//
// Stopping the embedded server first is what releases net/http's keep-alive
// goroutines: without it they hold the engine's drain open to its full budget on
// every shutdown of an idle HTTP server, which looks exactly like a stuck
// handler.
func Test_httpAdapter_shutdown(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// served forces the adapter to launch by serving one real request.
		served bool
	}
	tests := []tc{
		{name: "an adapter that served a request", served: true},
		//: an adapter that never saw a connection has nothing to stop, and must
		//: not block waiting for a goroutine that was never started.
		{name: "an adapter that never saw a connection"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if !c.served {
			adapter := newHTTPAdapter(http.NotFoundHandler())
			ctx, cancel := context.WithTimeout(t.Context(), httpDaemonSettleTimeout)
			defer cancel()

			adapter.shutdown(ctx)

			if launchedDaemon(adapter) != nil {
				t.Fatal("an adapter that never served launched a goroutine on shutdown")
			}
			return
		}
		srv, adapter, _ := startHTTPGroup(t)
		ctx, cancel := context.WithTimeout(t.Context(), httpDaemonSettleTimeout)
		defer cancel()

		if err := srv.Shutdown(ctx); err != nil {
			t.Fatalf("shutdown: %v", err)
		}

		select {
		//: the goroutine returned.
		case <-launchedDaemon(adapter).Done():
		case <-time.After(httpDaemonSettleTimeout):
			t.Fatal("Shutdown returned but the embedded http.Server is still running")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_httpAdapter_closeNow pins that Close ends the adapter's net/http
// goroutine too.
//
// Close severs the live sockets and forgets the listeners, but the http.Server
// driving an HTTP group runs over a bridge listener of its own, and nothing in
// Close was closing it. The daemon stayed parked in Accept for the life of the
// process, holding its http.Server and every keep-alive goroutine with it. This
// is not a race: it happened on every Close of an HTTP group.
func Test_httpAdapter_closeNow(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// served forces the adapter to launch by serving one real request.
		served bool
	}
	tests := []tc{
		{name: "an adapter that served a request", served: true},
		{name: "an adapter that never saw a connection"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if !c.served {
			adapter := newHTTPAdapter(http.NotFoundHandler())

			adapter.closeNow()

			//: nothing to close, and nothing started on the way out.
			if launchedDaemon(adapter) != nil {
				t.Fatal("an adapter that never served launched a goroutine on close")
			}
			return
		}
		srv, adapter, _ := startHTTPGroup(t)

		if err := srv.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}

		select {
		//: the goroutine returned, which is the whole assertion.
		case <-launchedDaemon(adapter).Done():
		case <-time.After(httpDaemonSettleTimeout):
			t.Fatal("Close returned but the embedded http.Server is still running — " +
				"its bridge listener was never closed, so the goroutine outlives the server")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
