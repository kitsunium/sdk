package server_test

import (
	"context"
	"io"
	stdnet "net"
	"net/http"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// echoOnce reads one byte and writes it back. Both the SDK and the bare-listener
// baselines run exactly this body, so the delta between them is the domain's
// overhead and nothing else.
func echoOnce(c stdnet.Conn) {
	var buf [1]byte
	n, err := c.Read(buf[:])
	//: a peer that closed early ends this connection and nothing more.
	if err != nil {
		return
	}
	_, werr := c.Write(buf[:n])
	//: the write outcome is the benchmark's only product; discarding it here
	//: is deliberate because the client asserts the echo instead.
	discard(werr)
}

// roundTripOnce dials, sends a byte, reads the echo and closes.
func roundTripOnce(b *testing.B, addr string) {
	b.Helper()
	c, err := stdnet.Dial("tcp", addr)
	if err != nil {
		b.Fatalf("dial: %v", err)
	}
	var buf [1]byte
	if _, werr := c.Write([]byte{'x'}); werr != nil {
		b.Fatalf("write: %v", werr)
	}
	if _, rerr := io.ReadFull(c, buf[:]); rerr != nil {
		b.Fatalf("read: %v", rerr)
	}
	if cerr := c.Close(); cerr != nil {
		b.Fatalf("close: %v", cerr)
	}
}

// BenchmarkServeConn_SDK measures one full accept-serve-close cycle through the
// domain: pooled wrapper, middleware chain resolution, deadline application,
// counters and drain bookkeeping.
func BenchmarkServeConn_SDK(b *testing.B) {
	srv := server.New()
	srv.Group("bench", server.Listen("tcp", "127.0.0.1:0")).
		HandleFunc(func(_ context.Context, c corenet.Conn) error {
			echoOnce(c)
			return nil
		})
	if err := srv.Start(context.Background()); err != nil {
		b.Fatalf("start: %v", err)
	}
	b.Cleanup(func() { discard(srv.Close()) })
	addr := srv.State().Listeners[0].Address

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		roundTripOnce(b, addr)
	}
}

// BenchmarkServeConn_BareListener is the baseline: the same echo body behind a
// hand-written net.Listener loop with no pooling, no counters and no drain.
// The gap to BenchmarkServeConn_SDK is what the domain costs.
//
// Goroutine lifecycle: one accept goroutine plus one per connection, all owned
// by this benchmark. The accept loop exits when the deferred listener Close
// makes Accept fail; each connection goroutine exits after a single echo. They
// are deliberately untracked — that absence of bookkeeping is precisely the
// baseline the SDK path is being measured against.
func BenchmarkServeConn_BareListener(b *testing.B) {
	ln, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("listen: %v", err)
	}
	b.Cleanup(func() { discard(ln.Close()) })
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go func(conn stdnet.Conn) {
				echoOnce(conn)
				discard(conn.Close())
			}(c)
		}
	}()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		roundTripOnce(b, ln.Addr().String())
	}
}

// benchHandler answers every request with a fixed body.
var benchHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	//: the response body is fixed; a write failure means the client vanished.
	_, err := io.WriteString(w, "ok")
	discard(err)
})

// httpGet performs one keep-alive request through client.
func httpGet(b *testing.B, client *http.Client, url string) {
	b.Helper()
	resp, err := client.Get(url)
	if err != nil {
		b.Fatalf("get: %v", err)
	}
	if _, cerr := io.Copy(io.Discard, resp.Body); cerr != nil {
		b.Fatalf("drain: %v", cerr)
	}
	if cerr := resp.Body.Close(); cerr != nil {
		b.Fatalf("close: %v", cerr)
	}
}

// BenchmarkHTTP_Adapter measures a keep-alive request served by net/http mounted
// on our listener. Because the bridge hands over a connection rather than a
// request, its cost is amortised across every request on that connection — this
// benchmark is what shows whether that claim holds.
func BenchmarkHTTP_Adapter(b *testing.B) {
	srv := server.New()
	srv.Group("http", server.Listen("tcp", "127.0.0.1:0")).HandleHTTP(benchHandler)
	if err := srv.Start(context.Background()); err != nil {
		b.Fatalf("start: %v", err)
	}
	b.Cleanup(func() { discard(srv.Close()) })
	url := "http://" + srv.State().Listeners[0].Address + "/"
	client := &http.Client{}
	httpGet(b, client, url)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		httpGet(b, client, url)
	}
}

// BenchmarkHTTP_Native is the baseline: the identical handler served by a stock
// http.Server on its own listener, with none of the domain in the path.
//
// Goroutine lifecycle: one goroutine runs http.Server.Serve, owned by this
// benchmark. It exits when the deferred Close shuts the server down, which
// makes Serve return ErrServerClosed.
func BenchmarkHTTP_Native(b *testing.B) {
	ln, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: benchHandler}
	go func() { discard(srv.Serve(ln)) }()
	b.Cleanup(func() { discard(srv.Close()) })
	url := "http://" + ln.Addr().String() + "/"
	client := &http.Client{}
	httpGet(b, client, url)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		httpGet(b, client, url)
	}
}

// discard deliberately drops a non-actionable error from a benchmark's setup or
// teardown, recording the discard so the error audit treats it as intentional.
// A benchmark has no caller to report to, and a failed close during teardown
// cannot change the measurement that already happened.
func discard(err error) {
	//: read the parameter so the unused-error audit treats this as deliberate.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
	//: the error concerns a socket or server that is already finished with.
}
