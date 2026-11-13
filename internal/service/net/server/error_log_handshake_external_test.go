// Package server_test — a TLS handshake that fails reaches the server's
// logger, never stderr, under the budget net/http's own lines share (#846):
// the engine negotiates TLS before net/http or a codec sees the connection,
// so the line net/http would have written is the engine's to write.
package server_test

import (
	"context"
	"io"
	stdnet "net"
	"net/http"
	"strings"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	"github.com/kitsunium/sdk/internal/service/net/http1"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// plaintextOnTLS sends an HTTP request in clear to a TLS listener at addr:
// the first record does not look like a TLS handshake, so the negotiation
// fails at once.
func plaintextOnTLS(t *testing.T, addr string) {
	t.Helper()
	c, err := stdnet.DialTimeout("tcp", addr, boundsDeadline)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer closeOrFail(t, c)
	_ = c.SetDeadline(time.Now().Add(boundsDeadline))
	if _, err := io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, _ = io.Copy(io.Discard, c)
}

// TestWithLogger_handshakeFailureReachesTheLogger holds #846's first
// criterion on every kind of TLS group the engine negotiates for: a plaintext
// request on a TLS listener writes nothing to the standard logger (stderr's
// stand-in) and the logger receives one warn record — source=net/http on an
// HTTP group, in net/http's own words, source=net/server on a connection
// handler's group and on a loop codec's — naming the group and the peer.
func TestWithLogger_handshakeFailureReachesTheLogger(t *testing.T) {
	id := selfSignedIdentity(t)
	for _, c := range []struct {
		name    string
		declare func(*server.Server)
		source  string
		prefix  string
	}{
		{"http", func(srv *server.Server) {
			srv.Group("api", server.Listen("tcp", "127.0.0.1:0"), server.TLS(id)).
				HandleHTTP(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		}, "net/http", "http: TLS handshake error from 127.0.0.1:"},
		{"stream", func(srv *server.Server) {
			srv.Group("api", server.Listen("tcp", "127.0.0.1:0"), server.TLS(id)).
				HandleFunc(func(context.Context, corenet.Conn) error { return nil })
		}, "net/server", "TLS handshake error from 127.0.0.1:"},
		{"loop-codec", func(srv *server.Server) {
			codec := http1.HandleLoop(corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {}), http1.Parser{})
			srv.Group("api", server.Listen("tcp", "127.0.0.1:0"), server.TLS(id), server.Loop(corenet.LoopValue{Loops: 1})).HandleLoop(codec)
		}, "net/server", "TLS handshake error from 127.0.0.1:"},
	} {
		t.Run(c.name, func(t *testing.T) {
			std := captureStandardLog(t)
			rec := &recordingLogger{}
			srv := server.New(server.WithLogger(rec))
			c.declare(srv)
			if err := srv.Start(t.Context()); err != nil {
				t.Fatalf("start: %v", err)
			}
			t.Cleanup(func() { closeOrFail(t, srv) })
			plaintextOnTLS(t, srv.State().Listeners[0].Address)
			if !waitFor(t, func() bool { return len(rec.got()) > 0 }) {
				t.Fatal("the logger received no record")
			}
			records := rec.got()
			if len(records) != 1 {
				t.Fatalf("the logger received %d records, want 1: %+v", len(records), records)
			}
			r := records[0]
			if r.level != level.Warn || !strings.HasPrefix(r.message, c.prefix) || !strings.Contains(r.message, "tls:") {
				t.Errorf("record = %q at %v, want a warn starting %q with crypto/tls's reason", r.message, r.level, c.prefix)
			}
			if r.attrs["source"] != c.source || r.attrs["group"] != "api" {
				t.Errorf("attrs = %v, want source=%s and group=api", r.attrs, c.source)
			}
			if got := std.String(); got != "" {
				t.Errorf("the standard logger was written to: %q", got)
			}
			if got := srv.State().SuppressedLogs; got != 0 {
				t.Errorf("SuppressedLogs = %d, want 0 for one failure", got)
			}
		})
	}
}

// TestWithLogger_noLoggerCountsHandshakeFailures pins the default: with no
// logger a failed handshake is counted in SuppressedLogs, never written.
func TestWithLogger_noLoggerCountsHandshakeFailures(t *testing.T) {
	std := captureStandardLog(t)
	srv := server.New()
	srv.Group("api", server.Listen("tcp", "127.0.0.1:0"), server.TLS(selfSignedIdentity(t))).
		HandleHTTP(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })
	plaintextOnTLS(t, srv.State().Listeners[0].Address)
	if !waitFor(t, func() bool { return srv.State().SuppressedLogs == 1 }) {
		t.Fatalf("SuppressedLogs = %d, want 1", srv.State().SuppressedLogs)
	}
	if got := std.String(); got != "" {
		t.Errorf("the standard logger was written to: %q", got)
	}
}
