// Package server — the net/http adapter.
package server

import (
	"context"
	stdnet "net"
	"net/http"
	"sync"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/worker"
)

// httpAdapter serves connections through an http.Server mounted on our listener.
//
// This is the shape ADR 0029 D3 commits to: our unified listener, our limits,
// our TLS identity and our drain, with net/http doing the protocol. net/http
// handles HTTP/1.1 and HTTP/2 and has a decade of hardening behind it; replacing
// it would be a regression in correctness and security for no gain in
// throughput.
//
// Completion is tracked through http.Server's ConnState hook rather than by
// wrapping the connection. Wrapping was the obvious design and it is wrong twice
// over: it puts the pooled wrapper in two goroutines at once, and it hides the
// concrete *tls.Conn that net/http type-asserts on to populate Request.TLS — so
// every request on an HTTPS listener would arrive looking like plaintext.
type httpAdapter struct {
	// handler is the application's http.Handler.
	handler http.Handler
	// start guarantees the serving goroutine is launched at most once.
	start sync.Once
	// mu guards waiters AND the lifecycle fields below. They are written by the
	// first connection's goroutine and read by whichever goroutine shuts the
	// server down, so an unguarded read is a genuine race, not a formality.
	mu sync.RWMutex
	// waiters maps a handed-over connection to the ServeConn blocked on it.
	waiters map[stdnet.Conn]chan struct{}
	// bridge feeds accepted connections to http.Server.
	bridge *chanListener
	// server is the net/http instance driving the protocol.
	server *http.Server
	// daemon owns the goroutine running http.Server.Serve. Using the kernel's
	// own loop primitive rather than a bare "go" keeps the goroutine's owner
	// and its termination explicit, and gives shutdown a channel to wait on.
	daemon *worker.LoopDaemon
	// stopped records that the adapter has been shut down. A connection that
	// reaches launch afterwards must not start a server: the engine does not
	// track this goroutine in its WaitGroup, so nothing else would ever end it.
	stopped bool
}

// newHTTPAdapter wraps an http.Handler as a ConnHandler.
func newHTTPAdapter(h http.Handler) *httpAdapter {
	//: the bridge is built on first use, when a real connection reveals which
	//: listener accepted it.
	return &httpAdapter{
		handler: h,
		waiters: make(map[stdnet.Conn]chan struct{}, expectedLiveConns),
	}
}

// ServeConn implements corenet.ConnHandler.
//
// Goroutine lifecycle: the first connection starts one goroutine running
// http.Server.Serve over the bridge listener. It lives for the group's lifetime
// and exits when the bridge closes, which Accept reports as net.ErrClosed — the
// termination http.Server expects.
func (a *httpAdapter) ServeConn(ctx context.Context, c corenet.Conn) error {
	raw := rawSocket(c)
	a.start.Do(func() { a.launch(raw) })
	bridge := a.currentBridge()
	//: the adapter was shut down before this connection could start one, so
	//: launch refused and there is no server to hand it to.
	if bridge == nil {
		//: nothing will serve it, so let the engine reclaim it now.
		return corenet.ServerClosed
	}
	done := a.register(raw)
	defer a.unregister(raw)
	//: the bridge closed before the hand-off landed, so nothing will serve this
	//: connection and the engine should reclaim it now.
	if !bridge.offer(raw) {
		//: nothing will serve it, so let the engine reclaim it now.
		return corenet.ServerClosed
	}
	select {
	//: net/http has finished with the connection.
	case <-done:
		//: net/http closed the connection; the engine may reclaim it.
		return nil
	//: the server is draining; release the connection rather than wait on a
	//: keep-alive that may never see another request.
	case <-ctx.Done():
		//: draining — do not hold the drain open on an idle keep-alive.
		return nil
	}
}

// register creates the completion channel for a handed-over connection.
func (a *httpAdapter) register(raw stdnet.Conn) chan struct{} {
	done := make(chan struct{})
	a.mu.Lock()
	a.waiters[raw] = done
	a.mu.Unlock()
	//: registered before the hand-off, so ConnState can never fire first.
	return done
}

// unregister drops the completion channel once ServeConn is done with it.
func (a *httpAdapter) unregister(raw stdnet.Conn) {
	a.mu.Lock()
	delete(a.waiters, raw)
	a.mu.Unlock()
}

// onConnState releases the ServeConn waiting on a finished connection.
func (a *httpAdapter) onConnState(raw stdnet.Conn, state http.ConnState) {
	//: only a terminal state means net/http is finished with the connection;
	//: idle and active are keep-alive transitions we must not act on.
	if state != http.StateClosed && state != http.StateHijacked {
		//: a keep-alive transition, not a completion.
		return
	}
	a.mu.Lock()
	done, waiting := a.waiters[raw]
	//: delete under the lock so a second terminal state cannot double-close.
	if waiting {
		delete(a.waiters, raw)
	}
	a.mu.Unlock()
	//: release the engine goroutine holding this connection.
	if waiting {
		close(done)
	}
}

// launch starts the net/http serving goroutine for this adapter.
//
// Goroutine lifecycle: exactly one is started, on the first connection, and it
// is owned by the adapter. It terminates when shutdown closes the bridge, which
// makes Accept report net.ErrClosed — the termination http.Server expects. It
// is not tracked by the engine's WaitGroup on purpose: it must outlive the
// individual ServeConn calls it serves, and shutdown is what ends it.
func (a *httpAdapter) launch(raw stdnet.Conn) {
	a.mu.Lock()
	defer a.mu.Unlock()
	//: shutdown has already decided there was nothing to stop. Starting a
	//: server now would leave a goroutine nobody owns, because the engine
	//: deliberately does not track this one in its WaitGroup.
	if a.stopped {
		//: refuse; ServeConn turns the absent bridge into ServerClosed.
		return
	}
	bridge := newChanListener(raw.LocalAddr())
	srv := &http.Server{Handler: a.handler, ConnState: a.onConnState}
	a.bridge = bridge
	a.server = srv
	//: the daemon owns the goroutine and publishes its termination on Done,
	//: which is what shutdown waits on before declaring the group stopped. It
	//: closes over the locals rather than reading the fields back, so the
	//: serving goroutine never touches adapter state it does not hold the lock
	//: for.
	a.daemon = worker.Start(func(_ <-chan struct{}) {
		//: Serve always returns a non-nil error; ErrClosed on our drain path is
		//: the expected one and carries nothing worth reporting.
		swallowErr(srv.Serve(bridge))
	})
}

// shutdown stops the embedded http.Server gracefully, releasing its keep-alive
// goroutines, and waits for the serving goroutine within the caller's budget.
func (a *httpAdapter) shutdown(ctx context.Context) {
	bridge, srv, daemon := a.stop()
	//: an adapter that never saw a connection has nothing to stop.
	if bridge == nil {
		//: never served a connection, so there is no http.Server to stop.
		return
	}
	swallowErr(bridge.Close())
	//: the graceful path lets in-flight requests finish within the caller's
	//: budget; the engine's own drain bounds how long that can take.
	swallowErr(srv.Shutdown(ctx))
	select {
	//: the serving goroutine has exited.
	case <-daemon.Done():
	//: the caller's budget ran out first; the engine's drain reports that.
	case <-ctx.Done():
	}
}

// closeNow stops the embedded http.Server immediately, without a drain.
//
// It is Close's counterpart to shutdown: Close severs live sockets rather than
// waiting for them, so the embedded server is closed the same way instead of
// being left to finish. Without this, Close ended every listener the engine
// knew about and left the http.Server running on its bridge — a goroutine, an
// http.Server and its keep-alive machinery outliving the server that owned
// them, on every Close of an HTTP group.
func (a *httpAdapter) closeNow() {
	bridge, srv, _ := a.stop()
	//: an adapter that never saw a connection has nothing to stop.
	if bridge == nil {
		//: never served a connection, so there is no http.Server to close.
		return
	}
	swallowErr(bridge.Close())
	//: Close is immediate by contract, so the connections go with it rather
	//: than being drained; the daemon is not waited on for the same reason.
	swallowErr(srv.Close())
}

// stop marks the adapter shut down and snapshots what has to be stopped.
//
// Marking and reading happen together under the lock so a launch racing this
// call either happens entirely before it — and is therefore in the snapshot —
// or sees stopped and refuses. The lock is released before anything is closed
// or waited on, because onConnState takes it too and http.Server fires that
// hook from inside Shutdown.
func (a *httpAdapter) stop() (bridge *chanListener, srv *http.Server, daemon *worker.LoopDaemon) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopped = true
	//: whatever was launched before this point, if anything.
	return a.bridge, a.server, a.daemon
}

// currentBridge reports the bridge listener the adapter launched, if any.
func (a *httpAdapter) currentBridge() *chanListener {
	a.mu.RLock()
	defer a.mu.RUnlock()
	//: nil means launch refused because the adapter is already stopped.
	return a.bridge
}

// rawSocket returns the underlying socket behind a pooled connection.
//
// net/http must receive the concrete connection — a *tls.Conn on an HTTPS
// listener — because it type-asserts on it to populate Request.TLS. Handing it
// our pooled wrapper would make every HTTPS request look like plaintext.
func rawSocket(c corenet.Conn) stdnet.Conn {
	pooled, ok := c.(*conn)
	//: a non-pooled implementation is already the socket itself.
	if !ok || pooled.Conn == nil {
		//: a non-pooled implementation is already the socket itself.
		return c
	}
	//: the concrete socket, so net/http can recognise a *tls.Conn.
	return pooled.Conn
}
