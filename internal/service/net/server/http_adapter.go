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
	// bridge feeds accepted connections to http.Server.
	bridge *chanListener
	// server is the net/http instance driving the protocol.
	server *http.Server
	// start guarantees the serving goroutine is launched exactly once.
	start sync.Once
	// mu guards waiters.
	mu sync.Mutex
	// waiters maps a handed-over connection to the ServeConn blocked on it.
	waiters map[stdnet.Conn]chan struct{}
	// daemon owns the goroutine running http.Server.Serve. Using the kernel's
	// own loop primitive rather than a bare "go" keeps the goroutine's owner
	// and its termination explicit, and gives shutdown a channel to wait on.
	daemon *worker.LoopDaemon
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
	done := a.register(raw)
	defer a.unregister(raw)
	//: the bridge closed before the hand-off landed, so nothing will serve this
	//: connection and the engine should reclaim it now.
	if !a.bridge.offer(raw) {
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
	a.bridge = newChanListener(raw.LocalAddr())
	a.server = &http.Server{Handler: a.handler, ConnState: a.onConnState}
	//: the daemon owns the goroutine and publishes its termination on Done,
	//: which is what shutdown waits on before declaring the group stopped.
	a.daemon = worker.Start(a.serve)
}

// serve runs http.Server until the bridge closes.
//
// The stop channel is unused: http.Server is interrupted by closing its
// listener, not by a signal, so shutdown closes the bridge and Serve returns on
// its own. The daemon still owns the goroutine and reports its exit.
func (a *httpAdapter) serve(_ <-chan struct{}) {
	//: Serve always returns a non-nil error; ErrClosed on our drain path is the
	//: expected one and carries nothing worth reporting.
	swallowErr(a.server.Serve(a.bridge))
}

// shutdown stops the embedded http.Server, releasing its keep-alive goroutines.
func (a *httpAdapter) shutdown(ctx context.Context) {
	//: an adapter that never saw a connection has nothing to stop.
	if a.bridge == nil {
		//: never served a connection, so there is no http.Server to stop.
		return
	}
	swallowErr(a.bridge.Close())
	//: the graceful path lets in-flight requests finish within the caller's
	//: budget; the engine's own drain bounds how long that can take.
	swallowErr(a.server.Shutdown(ctx))
	select {
	//: the serving goroutine has exited.
	case <-a.daemon.Done():
	//: the caller's budget ran out first; the engine's drain reports that.
	case <-ctx.Done():
	}
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
