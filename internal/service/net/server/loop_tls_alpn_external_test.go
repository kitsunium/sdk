// Package server_test — the http1 codec with TLS against its identity's ALPN
// (ADR 0195, ADR 0198): the codec serves HTTP/1.1 alone, so an identity
// naming h2, or nothing the codec serves, is refused at Start as an HTTP
// group's would be, and one naming HTTP/1.1 among others is kept to it.
package server_test

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/http1"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// declareTLSCodec declares a TLS codec group with id on a loopback port.
func declareTLSCodec(srv *server.Server, id corenet.IdentityValue) {
	codec := http1.HandleLoop(corenet.HTTP1HandlerFunc(func(*corenet.HTTP1RequestValue, *corenet.HTTP1ResponseValue) {}), http1.Parser{})
	srv.Group("tls", server.Listen("tcp", "127.0.0.1:0"), server.TLS(id), server.Loop(corenet.LoopValue{Loops: 1})).HandleLoop(codec)
}

// TestLoopTLS_identityALPN pins that a TLS codec group honours its
// identity's NextProtos rather than overwriting them: refused with
// ProtocolsRefused when they name h2 or nothing the codec serves, kept to
// HTTP/1.1 otherwise.
func TestLoopTLS_identityALPN(t *testing.T) {
	t.Parallel()
	h2Only, _ := alpnIdentity(t, "h2")
	both, _ := alpnIdentity(t, "h2", "http/1.1")
	foreign, _ := alpnIdentity(t, "acme-tls/1")
	for _, c := range []struct {
		name, reason string
		id           corenet.IdentityValue
	}{
		{"NextProtos naming h2 alone", "NextProtos h2", h2Only},
		{"NextProtos naming h2 beside HTTP/1.1", "NextProtos h2", both},
		{"NextProtos naming none served", "NextProtos", foreign},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv := server.New()
			declareTLSCodec(srv, c.id)
			err := srv.Start(t.Context())
			t.Cleanup(func() { _ = srv.Close() })
			if !errs.HasCode(err, corenet.CodeProtocolsRefused) || !fieldIs(err, "reason", c.reason) {
				t.Fatalf("Start = %v, want ProtocolsRefused reason=%q", err, c.reason)
			}
			if n := len(srv.State().Listeners); n != 0 {
				t.Fatalf("a refused Start bound %d listeners", n)
			}
		})
	}
	//: an identity naming HTTP/1.1 among protocols the codec ignores.
	mixed, pool := alpnIdentity(t, "acme-tls/1", "http/1.1")
	srv := server.New()
	declareTLSCodec(srv, mixed)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })
	addr := srv.State().Listeners[0].Address
	if got, err := negotiate(t, addr, pool, "h2", "http/1.1"); err != nil || got != "http/1.1" {
		t.Fatalf("negotiated %q, %v; want http/1.1", got, err)
	}
	if _, err := negotiate(t, addr, pool, "acme-tls/1"); err == nil {
		t.Fatal("a client asking acme-tls/1 alone completed the handshake; the codec offers HTTP/1.1 alone")
	}
}
