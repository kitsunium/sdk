// Package server_test — the http1 codec with TLS against its identity's ALPN
// and its Protocols list (ADR 0195, ADR 0198): the codec serves HTTP/1.1
// alone, so an identity naming h2, or nothing the codec serves, and a
// Protocols list naming h2 are refused at Start as an HTTP group's would be;
// an identity naming HTTP/1.1 among others is kept to it, Protocols
// ("http/1.1") is honoured, and no client below TLS 1.2 completes a
// handshake.
package server_test

import (
	"crypto/tls"
	stdnet "net"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/http1"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// declareTLSCodec declares a TLS codec group with id on a loopback port,
// given opts too; its handler answers "HTTP/1.1", the version it serves, as
// protoMajor expects.
func declareTLSCodec(srv *server.Server, id corenet.IdentityValue, opts ...server.GroupOption) {
	codec := http1.HandleLoop(corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
		res.Body = []byte("HTTP/1.1")
	}), http1.Parser{})
	opts = append([]server.GroupOption{server.Listen("tcp", "127.0.0.1:0"), server.TLS(id), server.Loop(corenet.LoopValue{Loops: 1})}, opts...)
	srv.Group("tls", opts...).HandleLoop(codec)
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

// TestLoopTLS_protocols pins that a TLS codec group honours Protocols when
// it names HTTP/1.1 alone — Start succeeds, the codec serves, ALPN offers
// http/1.1 — and refuses with ProtocolsRefused any list naming h2, alone or
// beside HTTP/1.1, rather than drop it, or a version it does not know.
func TestLoopTLS_protocols(t *testing.T) {
	t.Parallel()
	id, pool := alpnIdentity(t)
	for _, c := range []struct {
		name, reason string
		protos       []string
	}{
		{"h2 alone", "h2", []string{"h2"}},
		{"h2 beside HTTP/1.1", "h2", []string{"http/1.1", "h2"}},
		{"an unknown version", "spdy/3", []string{"spdy/3"}},
		{"an empty list", "empty", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv := server.New()
			declareTLSCodec(srv, id, server.Protocols(c.protos...))
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
	//: Protocols("http/1.1"): the codec's own version, honoured.
	srv := server.New()
	declareTLSCodec(srv, id, server.Protocols("http/1.1"))
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })
	addr := srv.State().Listeners[0].Address
	if got, err := negotiate(t, addr, pool, "h2", "http/1.1"); err != nil || got != "http/1.1" {
		t.Fatalf("negotiated %q, %v; want http/1.1", got, err)
	}
	if major, err := protoMajor(t, addr, pool, true, false); err != nil || major != 1 {
		t.Fatalf("served HTTP/%d, %v; want HTTP/1", major, err)
	}
}

// TestLoopTLS_floor pins that a TLS codec group negotiates nothing below
// TLS 1.2: a client capped at TLS 1.1 fails the handshake, one at TLS 1.2
// completes it.
func TestLoopTLS_floor(t *testing.T) {
	t.Parallel()
	id, pool := alpnIdentity(t)
	srv := server.New()
	declareTLSCodec(srv, id)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })
	addr := srv.State().Listeners[0].Address
	for _, c := range []struct {
		name string
		max  uint16
		ok   bool
	}{
		{"TLS 1.1", tls.VersionTLS11, false},
		{"TLS 1.2", tls.VersionTLS12, true},
	} {
		cfg := clientTLS(pool, "http/1.1")
		cfg.MinVersion, cfg.MaxVersion = tls.VersionTLS10, c.max
		d := &tls.Dialer{NetDialer: &stdnet.Dialer{Timeout: alpnDeadline}, Config: cfg}
		conn, err := d.DialContext(t.Context(), "tcp", addr)
		if err == nil {
			closeOrFail(t, conn)
		}
		if (err == nil) != c.ok {
			t.Fatalf("a client capped at %s: handshake err = %v, want success %v", c.name, err, c.ok)
		}
	}
}
