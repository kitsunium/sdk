//go:build linux

// Package server — Shards(0) (#856) under the HTTP/2 adapter (ADR 0220): one
// listener per loop, the codec wrapping the engine that serves them.
package server

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/http1"
	"github.com/kitsunium/sdk/internal/service/net/http2"
)

// TestLoopShards_http2Adapter pins the two branches together: a loop group of
// two loops on Shards(0) behind http2.HandleLoop reports one listener row of
// Shards 2 carrying the HTTP/2 bounds, both rows reuseport-loops and
// http2-loop on, answers h2c by prior knowledge on sixteen connections — the
// kernel's hash spreads them over both listeners — and HTTP/1.1 beside it, and
// drains within its budget.
func TestLoopShards_http2Adapter(t *testing.T) {
	hello := corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) { res.Body = []byte("ok") })
	srv := newServer(envOf(nil))
	srv.Group("loop", Listen("tcp", "127.0.0.1:0"), Plaintext(), Shards(0), Loop(corenet.LoopValue{Loops: 2, OutputCap: 128 << 10})).
		HandleLoop(http2.HandleLoop(hello, http1.Parser{}))
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { swallowErr(srv.Close()) })
	st := srv.State()
	if len(st.Listeners) != 1 || st.Listeners[0].Shards != 2 || st.Listeners[0].HTTP2 == nil {
		t.Fatalf("listeners = %+v, want one row of Shards 2 with the HTTP/2 bounds", st.Listeners)
	}
	if n := len(srv.listeners); n != 2 {
		t.Fatalf("bound %d listeners, want 2", n)
	}
	rows := st.Loops[0].Capabilities
	if !hasCapability(rows, corenet.CapabilityReuseportLoops, statusOn) || !hasCapability(rows, corenet.CapabilityHTTP2Loop, statusOn) {
		t.Fatalf("capabilities = %+v, want reuseport-loops and http2-loop on", rows)
	}
	addr := st.Listeners[0].Address
	for range 16 {
		resp := doH2c(t, h2cClient(t), "http://"+addr+"/")
		body, err := io.ReadAll(resp.Body)
		if err != nil || resp.ProtoMajor != 2 || resp.StatusCode != http.StatusOK || string(body) != "ok" {
			t.Fatalf("h2c: %d %s %q %v", resp.ProtoMajor, resp.Status, body, err)
		}
	}
	assertHTTP1OK(t, addr)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}
