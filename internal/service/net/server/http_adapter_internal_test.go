package server

import (
	"context"
	"io"
	stdnet "net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/worker"
)

// httpDaemonSettleTimeout bounds how long a test waits for the adapter's
// net/http goroutine to exit. It is generous on purpose: the assertion is
// "it terminates at all", not "it terminates quickly".
const httpDaemonSettleTimeout time.Duration = 3 * time.Second

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
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	addr = srv.State().Listeners[0].Address
	//: one real request is what makes the adapter build its bridge and start
	//: the goroutine whose lifetime this file is about.
	resp, err := http.Get("http://" + addr + "/")
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

// TestCloseStopsTheEmbeddedHTTPServer pins that Close ends the adapter's
// net/http goroutine.
//
// Close severs the live sockets and forgets the listeners, but the http.Server
// driving an HTTP group runs over a bridge listener of its own, and nothing in
// Close was closing it. The daemon stayed parked in Accept for the life of the
// process, holding its http.Server and every keep-alive goroutine with it. This
// is not a race: it happened on every Close of an HTTP group.
func TestCloseStopsTheEmbeddedHTTPServer(t *testing.T) {
	t.Parallel()
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

// TestShutdownStopsTheEmbeddedHTTPServer is the same assertion on the draining
// path, which did close the bridge. It is here so the Close test above cannot
// be "fixed" by breaking Shutdown.
func TestShutdownStopsTheEmbeddedHTTPServer(t *testing.T) {
	t.Parallel()
	srv, adapter, _ := startHTTPGroup(t)
	ctx, cancel := context.WithTimeout(context.Background(), httpDaemonSettleTimeout)
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

// TestLaunchNeverOutlivesShutdown pins the window between "shutdown decided
// there was nothing to stop" and "a connection started a server anyway".
//
// shutdown read a.bridge, found nil, and returned. A connection already on its
// way through ServeConn then ran launch, built a bridge and an http.Server, and
// started a goroutine that nothing would ever close — the engine does not track
// it in its WaitGroup, deliberately, so Shutdown reported a clean drain while
// the daemon ran on. The two goroutines also touched a.bridge, a.server and
// a.daemon with no synchronisation at all, which -race reports directly.
func TestLaunchNeverOutlivesShutdown(t *testing.T) {
	t.Parallel()
	const rounds int = 200
	for range rounds {
		runLaunchShutdownRace(t)
	}
}

// runLaunchShutdownRace drives one launch against one shutdown and asserts that
// whatever was started was also stopped.
//
// Goroutine lifecycle: exactly two, both owned by this function and joined on
// the WaitGroup before it returns. They exist to put launch and shutdown on
// different goroutines, which is the only way to reach the window between them.
func runLaunchShutdownRace(t *testing.T) {
	t.Helper()
	adapter := newHTTPAdapter(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	local, remote := stdnet.Pipe()
	defer closeQuietly(t, local)
	defer closeQuietly(t, remote)

	ctx, cancel := context.WithTimeout(context.Background(), httpDaemonSettleTimeout)
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
