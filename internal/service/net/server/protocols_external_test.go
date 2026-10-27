// Package server_test — an HTTP group's protocols (ADR 0192): what its TLS
// listeners negotiate by ALPN, what net/http then serves, what Start refuses,
// and the HTTP/2 bounds a peer meets.
package server_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"io"
	stdnet "net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// alpnDeadline bounds every client operation of these tests, so a server that
// never answers fails the test instead of hanging the suite.
const alpnDeadline time.Duration = 10 * time.Second

// startTLSHTTP serves h on a TLS loopback group built with id and opts, and
// returns the bound address.
func startTLSHTTP(t *testing.T, id corenet.IdentityValue, h http.Handler, opts ...server.GroupOption) string {
	t.Helper()
	srv := server.New()
	opts = append([]server.GroupOption{server.Listen("tcp", "127.0.0.1:0"), server.TLS(id)}, opts...)
	srv.Group("api", opts...).HandleHTTP(h)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })
	return srv.State().Listeners[0].Address
}

// alpnIdentity is a self-signed identity whose NextProtos is next, and the
// pool that trusts it.
func alpnIdentity(t *testing.T, next ...string) (corenet.IdentityValue, *x509.CertPool) {
	t.Helper()
	id, cert, err := newSelfSignedIdentityALPN(next)
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return id, pool
}

// clientTLS trusts pool and offers protos.
func clientTLS(pool *x509.CertPool, protos ...string) *tls.Config {
	return &tls.Config{RootCAs: pool, ServerName: testServerName, NextProtos: protos, MinVersion: tls.VersionTLS12}
}

// negotiate handshakes with addr offering protos and returns the protocol the
// server chose, or the handshake's error.
func negotiate(t *testing.T, addr string, pool *x509.CertPool, protos ...string) (string, error) {
	t.Helper()
	d := &tls.Dialer{NetDialer: &stdnet.Dialer{Timeout: alpnDeadline}, Config: clientTLS(pool, protos...)}
	c, err := d.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		return "", err
	}
	defer closeOrFail(t, c)
	tc, ok := c.(*tls.Conn)
	if !ok {
		t.Fatalf("dialled a %T", c)
	}
	return tc.ConnectionState().NegotiatedProtocol, nil
}

// protoMajor sends one GET through a client speaking only the given versions
// and returns the response's major version as the handler saw it.
func protoMajor(t *testing.T, addr string, pool *x509.CertPool, h1, h2 bool) (int, error) {
	t.Helper()
	var p http.Protocols
	p.SetHTTP1(h1)
	p.SetHTTP2(h2)
	tr := &http.Transport{TLSClientConfig: clientTLS(pool), Protocols: &p}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: alpnDeadline}
	resp, err := client.Get("https://" + addr + "/")
	if err != nil {
		return 0, err
	}
	defer closeOrFail(t, resp.Body)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != resp.Proto {
		t.Fatalf("handler saw %q, client %q", body, resp.Proto)
	}
	return resp.ProtoMajor, nil
}

// protoEcho answers with the protocol the request arrived on.
var protoEcho = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	_, _ = io.WriteString(w, r.Proto)
})

// TestProtocols_ALPN pins what a TLS group negotiates and serves: unset, both,
// h2 first; HTTP/1.1 alone; h2 alone; both in the group's order; and, when the
// identity names its own NextProtos, the identity's order over the versions
// both name. A version the group does not offer fails the handshake.
func TestProtocols_ALPN(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		next   []string
		opts   []server.GroupOption
		offer  []string
		want   string
		h1, h2 bool
	}
	tests := []tc{
		{name: "unset: h2 first", offer: []string{"http/1.1", "h2"}, want: "h2", h1: true, h2: true},
		{name: "unset: HTTP/1.1 to a client without h2", offer: []string{"http/1.1"}, want: "http/1.1", h1: true, h2: true},
		{name: "HTTP/1.1 alone", opts: []server.GroupOption{server.Protocols("http/1.1")}, offer: []string{"h2", "http/1.1"}, want: "http/1.1", h1: true},
		{name: "h2 alone", opts: []server.GroupOption{server.Protocols("h2")}, offer: []string{"h2", "http/1.1"}, want: "h2", h2: true},
		{name: "both, HTTP/1.1 preferred", opts: []server.GroupOption{server.Protocols("http/1.1", "h2")}, offer: []string{"h2", "http/1.1"}, want: "http/1.1", h1: true, h2: true},
		{name: "the identity's order", next: []string{"http/1.1", "h2"}, offer: []string{"h2", "http/1.1"}, want: "http/1.1", h1: true, h2: true},
		{name: "the identity's list, narrowed", next: []string{"h2", "acme"}, opts: []server.GroupOption{server.Protocols("h2")}, offer: []string{"h2", "http/1.1"}, want: "h2", h2: true},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			id, pool := alpnIdentity(t, c.next...)
			addr := startTLSHTTP(t, id, protoEcho, c.opts...)
			got, err := negotiate(t, addr, pool, c.offer...)
			if err != nil || got != c.want {
				t.Fatalf("offered %v: negotiated %q, %v, want %q", c.offer, got, err, c.want)
			}
			//: each version the group serves answers on it; the other fails.
			for _, v := range []struct {
				h1, h2, served bool
				major          int
			}{{true, false, c.h1, 1}, {false, true, c.h2, 2}} {
				major, err := protoMajor(t, addr, pool, v.h1, v.h2)
				if v.served && (err != nil || major != v.major) {
					t.Errorf("HTTP/%d: %d, %v", v.major, major, err)
				}
				if !v.served && err == nil {
					t.Errorf("HTTP/%d was served by a group that excludes it", v.major)
				}
			}
		})
	}
}

// TestProtocols_refused pins every combination Start refuses with
// ProtocolsRefused, the reason named, before anything is bound; and a loop
// group, which refuses the option itself.
func TestProtocols_refused(t *testing.T) {
	t.Parallel()
	plain, _ := alpnIdentity(t)
	h2Only, _ := alpnIdentity(t, "h2")
	foreign, _ := alpnIdentity(t, "acme-tls/1")
	type tc struct {
		name, reason string
		declare      func(srv *server.Server)
	}
	httpGroup := func(opts ...server.GroupOption) func(*server.Server) {
		return func(srv *server.Server) {
			srv.Group("g", append([]server.GroupOption{server.Listen("tcp", "127.0.0.1:0")}, opts...)...).HandleHTTP(protoEcho)
		}
	}
	tests := []tc{
		{"an empty list", "empty", httpGroup(server.TLS(plain), server.Protocols())},
		{"an unknown identifier", "h3", httpGroup(server.TLS(plain), server.Protocols("h2", "h3"))},
		{"a repeated identifier", "h2", httpGroup(server.TLS(plain), server.Protocols("h2", "h2"))},
		{"h2 without TLS", "h2 without TLS", httpGroup(server.Protocols("h2"))},
		{"NextProtos naming an excluded version", "NextProtos h2", httpGroup(server.TLS(h2Only), server.Protocols("http/1.1"))},
		{"NextProtos naming none served", "NextProtos", httpGroup(server.TLS(foreign))},
		{"a group serving no http.Handler", "handler", func(srv *server.Server) {
			srv.Group("g", server.Listen("tcp", "127.0.0.1:0"), server.Protocols("http/1.1")).Handle(echoConnHandler{})
		}},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv := server.New()
			c.declare(srv)
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
	//: plaintext and unset: HTTP/1.1, served.
	srv := server.New()
	httpGroup()(srv)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("a plaintext HTTP group without Protocols: %v", err)
	}
	closeOrFail(t, srv)
}

// fieldIs reports whether err carries the string field key=want.
func fieldIs(err error, key, want string) bool {
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == key {
			return f.StringValue() == want
		}
	}
	return false
}

// h2Conn is a raw HTTP/2 connection over TLS, for the frames a client library
// will not send: a header block that never ends, a body nobody reads.
type h2Conn struct {
	c *tls.Conn
}

// dialH2 handshakes h2 with addr and sends the connection preface and an
// empty SETTINGS frame.
func dialH2(t *testing.T, addr string, pool *x509.CertPool) *h2Conn {
	t.Helper()
	d := &tls.Dialer{NetDialer: &stdnet.Dialer{Timeout: alpnDeadline}, Config: clientTLS(pool, "h2")}
	c, err := d.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	tc, _ := c.(*tls.Conn)
	t.Cleanup(func() { _ = tc.Close() })
	h := &h2Conn{c: tc}
	if _, err := io.WriteString(tc, "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"); err != nil {
		t.Fatalf("preface: %v", err)
	}
	h.frame(t, 0x4, 0, 0, nil)
	return h
}

// frame writes one frame (RFC 9113 §4.1).
func (h *h2Conn) frame(t *testing.T, typ, flags byte, stream uint32, payload []byte) {
	t.Helper()
	var hdr [9]byte
	hdr[0], hdr[1], hdr[2] = byte(len(payload)>>16), byte(len(payload)>>8), byte(len(payload))
	hdr[3], hdr[4] = typ, flags
	binary.BigEndian.PutUint32(hdr[5:], stream)
	if _, err := h.c.Write(append(hdr[:], payload...)); err != nil {
		t.Fatalf("frame %d: %v", typ, err)
	}
}

// closedWithin reports whether the server ends the connection — EOF, a reset,
// or a GOAWAY frame — within d, reading and discarding everything else.
func (h *h2Conn) closedWithin(d time.Duration) bool {
	_ = h.c.SetReadDeadline(time.Now().Add(d))
	var hdr [9]byte
	for {
		if _, err := io.ReadFull(h.c, hdr[:]); err != nil {
			var ne stdnet.Error
			return !(errors.As(err, &ne) && ne.Timeout())
		}
		n := int(hdr[0])<<16 | int(hdr[1])<<8 | int(hdr[2])
		if _, err := io.CopyN(io.Discard, h.c, int64(n)); err != nil {
			return true
		}
		//: GOAWAY: the server is closing the connection.
		if hdr[3] == 0x7 {
			return true
		}
	}
}

// TestProtocols_slowHeaders pins that a header block that never ends — a
// HEADERS frame without END_HEADERS, and no CONTINUATION — does not hold an
// HTTP/2 connection open past the group's idle bound, or its read bound when
// it sets no idle one: no stream is open until the block ends, so the
// connection is idle. ReadHeaderTimeout is HTTP/1.1's; net/http's HTTP/2
// server has no header phase of its own.
func TestProtocols_slowHeaders(t *testing.T) {
	t.Parallel()
	for name, opt := range map[string]server.GroupOption{
		"IdleTimeout": server.IdleTimeout(200 * time.Millisecond),
		"ReadTimeout": server.ReadTimeout(200 * time.Millisecond),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			id, pool := alpnIdentity(t)
			addr := startTLSHTTP(t, id, protoEcho, opt)
			h := dialH2(t, addr, pool)
			//: :method GET, :scheme https, :path / from HPACK's static table;
			//: END_STREAM only, so the block waits for a CONTINUATION that never
			//: comes.
			h.frame(t, 0x1, 0x1, 1, []byte{0x82, 0x87, 0x84})
			start := time.Now()
			if !h.closedWithin(5 * time.Second) {
				t.Fatal("an unfinished header block held the connection open")
			}
			if took := time.Since(start); took > 3*time.Second {
				t.Fatalf("closed after %v", took)
			}
		})
	}
}

// TestProtocols_unreadStream pins the flow-control bounds: a stream whose
// handler never reads its body takes at most one stream's window of it, and
// another stream on the same connection still sends and is read a body larger
// than that window meanwhile — the connection's window outlasts one stream's.
func TestProtocols_unreadStream(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			<-release
		case http.MethodPut:
			_, _ = io.Copy(io.Discard, r.Body)
		}
		_, _ = io.WriteString(w, r.Proto)
	})
	id, pool := alpnIdentity(t)
	addr := startTLSHTTP(t, id, h)
	var p http.Protocols
	p.SetHTTP2(true)
	tr := &http.Transport{TLSClientConfig: clientTLS(pool), Protocols: &p}
	t.Cleanup(tr.CloseIdleConnections)
	client := &http.Client{Transport: tr, Timeout: alpnDeadline}
	//: one GET first, so both requests share its connection.
	if _, err := protoMajor2(client, addr); err != nil {
		t.Fatalf("first GET: %v", err)
	}
	sent := &countingReader{r: bytes.NewReader(make([]byte, 16<<20))}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	posted := make(chan error, 1)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+addr+"/", sent)
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		posted <- err
	}()
	//: the body stalls once the stream's window is spent.
	var last int64
	if !waitFor(t, func() bool {
		n := sent.n.Load()
		stalled := n > 0 && n == last
		last = n
		time.Sleep(50 * time.Millisecond)
		return stalled
	}) {
		t.Fatal("the unread body never stalled")
	}
	//: the window, plus what the transport and TLS buffer on the way.
	if n := sent.n.Load(); n > 512<<10 {
		t.Fatalf("an unread stream took %d bytes", n)
	}
	t.Logf("the unread stream took %d bytes", sent.n.Load())
	put, _ := http.NewRequestWithContext(t.Context(), http.MethodPut, "https://"+addr+"/", bytes.NewReader(make([]byte, 768<<10)))
	resp, err := client.Do(put)
	if err != nil {
		t.Fatalf("a second stream beside the unread one: %v", err)
	}
	_ = resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Fatalf("the second stream was %s", resp.Proto)
	}
	close(release)
	cancel()
	<-posted
}

// protoMajor2 sends one GET with client and returns the protocol the handler
// saw.
func protoMajor2(client *http.Client, addr string) (string, error) {
	resp, err := client.Get("https://" + addr + "/")
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	return strings.TrimSpace(string(b)), err
}

// countingReader counts the bytes read from r.
type countingReader struct {
	r io.Reader
	n atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}
