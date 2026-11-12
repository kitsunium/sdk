package server_test

import (
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// TestHTTPCounts_netHTTP holds #966 on net/http's engine: on a TLS group, one
// HTTP/2 and one HTTP/1.1 request count one each on the listener's row, and a
// SETTINGS frame whose length is no multiple of six — what net/http's
// CountError names frame_settings_mod_6 — counts frame_size once. A header
// list past MaxHeaderBytes is answered 431 by net/http without CountError,
// and is not counted on this engine.
func TestHTTPCounts_netHTTP(t *testing.T) {
	id, pool := alpnIdentity(t)
	srv := server.New()
	srv.Group("api", server.Listen("tcp", "127.0.0.1:0"), server.TLS(id)).HandleHTTP(protoEcho)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })
	addr := srv.State().Listeners[0].Address
	if major, err := protoMajor(t, addr, pool, false, true); err != nil || major != 2 {
		t.Fatalf("h2 request: major %d, %v", major, err)
	}
	if major, err := protoMajor(t, addr, pool, true, false); err != nil || major != 1 {
		t.Fatalf("HTTP/1.1 request: major %d, %v", major, err)
	}
	row := srv.State().Listeners[0]
	if len(row.ALPN) != 2 || row.ALPN[0] != "h2" || row.ALPN[1] != "http/1.1" {
		t.Fatalf("ALPN %q, want [h2 http/1.1]", row.ALPN)
	}
	if row.HTTP1Requests != 1 || row.HTTP2Requests != 1 || len(row.HTTP2Errors) != 0 {
		t.Fatalf("row after two requests: http1 %d http2 %d errors %+v, want 1, 1, none", row.HTTP1Requests, row.HTTP2Requests, row.HTTP2Errors)
	}
	h := dialH2(t, addr, pool)
	h.frame(t, 0x4, 0, 0, make([]byte, 5))
	if !h.closedWithin(5 * time.Second) {
		t.Fatal("a SETTINGS frame of 5 bytes left the connection open")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		row = srv.State().Listeners[0]
		if len(row.HTTP2Errors) == 1 && row.HTTP2Errors[0] == (corehttp2.ErrorCountValue{Type: corehttp2.ErrorTypeFrameSize, Count: 1}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("errors %+v, want frame_size 1", row.HTTP2Errors)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got, err := corehttp2.ErrorTypeFrameSize.MarshalText(); err != nil || string(got) != "frame_size" {
		t.Fatalf("MarshalText = %q, %v", got, err)
	}
	var _ corenet.ListenerStateValue = row
}
