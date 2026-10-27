// Package server — the http1 codec with TLS (ADR 0187 §D5, ADR 0196):
// crypto/tls on its own goroutine, bounded, HTTP/1.1 alone by ALPN, then the
// codec on the portable backend's lanes; the N connections × N tokens
// conformance over TLS; and every combination Start refuses.
package server

import (
	"crypto/tls"
	"io"
	stdnet "net"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/http1"
)

// tlsDial dials addr over TLS, trusting any certificate, offering protos.
func tlsDial(t *testing.T, addr string, protos ...string) (*tls.Conn, error) {
	t.Helper()
	d := &tls.Dialer{
		NetDialer: &stdnet.Dialer{Timeout: 3 * time.Second},
		Config:    &tls.Config{InsecureSkipVerify: true, NextProtos: protos},
	} //nolint:gosec // a self-signed test server
	c, err := d.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		return nil, err
	}
	tc, _ := c.(*tls.Conn)
	_ = tc.SetDeadline(time.Now().Add(20 * time.Second))
	t.Cleanup(func() { swallowErr(tc.Close()) })
	return tc, nil
}

// tlsLoopDial is loopDial over TLS, for tokenRounds.
func tlsLoopDial(t *testing.T, addr string) stdnet.Conn {
	t.Helper()
	c, err := tlsDial(t, addr, "http/1.1")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// startTLSCodec serves h through the http1 codec with TLS, two lanes.
func startTLSCodec(t *testing.T, h corenet.HTTP1Handler, opts ...GroupOption) (*Server, string) {
	t.Helper()
	srv := New()
	t.Cleanup(func() { swallowErr(srv.Close()) })
	opts = append([]GroupOption{Listen("tcp", "127.0.0.1:0"), TLS(testIdentity(t)), Loop(corenet.LoopValue{Loops: 2})}, opts...)
	srv.Group("tls", opts...).HandleLoop(http1.HandleLoop(h, http1.Parser{}))
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return srv, srv.State().Listeners[0].Address
}

// TestLoopTLS_serves pins the path: a TLS client speaking HTTP/1.1 is
// answered by the codec on the portable backend, its lane in [0, 2); ALPN
// offers HTTP/1.1 alone, so a client asking h2 only fails the handshake, and
// one asking both gets HTTP/1.1.
func TestLoopTLS_serves(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	lanes := map[int]bool{}
	h := corenet.HTTP1HandlerFunc(func(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
		mu.Lock()
		lanes[req.Lane] = true
		mu.Unlock()
		res.Body = []byte("lane-" + strconv.Itoa(req.Lane))
	})
	srv, addr := startTLSCodec(t, h)
	if st := srv.State(); len(st.Loops) != 1 || st.Loops[0].Backend != corenet.LoopBackendPortable || st.Loops[0].Loops != 2 {
		t.Fatalf("loops %+v", st.Loops)
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}}} //nolint:gosec // a self-signed test server
	t.Cleanup(tr.CloseIdleConnections)
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	for range 6 {
		tr.CloseIdleConnections()
		resp, err := client.Get("https://" + addr + "/")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.ProtoMajor != 1 || resp.TLS == nil || resp.TLS.NegotiatedProtocol != "http/1.1" || len(body) == 0 {
			t.Fatalf("%s %+v %q", resp.Proto, resp.TLS, body)
		}
	}
	mu.Lock()
	n := len(lanes)
	mu.Unlock()
	if n != 2 {
		t.Fatalf("served on %d lanes, want 2", n)
	}
	if _, err := tlsDial(t, addr, "h2"); err == nil {
		t.Fatal("an h2-only client completed the handshake")
	}
	if c, err := tlsDial(t, addr, "h2", "http/1.1"); err != nil || c.ConnectionState().NegotiatedProtocol != "http/1.1" {
		t.Fatalf("both offered: %v", err)
	}
}

// TestLoopTLS_handshakeBound pins the handshake's bound: a peer that opens a
// socket and says nothing is closed at HandshakeTimeout, and the lane never
// sees it.
func TestLoopTLS_handshakeBound(t *testing.T) {
	t.Parallel()
	srv, addr := startTLSCodec(t, corenet.HTTP1HandlerFunc(func(*corenet.HTTP1RequestValue, *corenet.HTTP1ResponseValue) {}),
		HandshakeTimeout(150*time.Millisecond))
	c, err := stdnet.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { swallowErr(c.Close()) }()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	start := time.Now()
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("a silent peer was answered")
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("closed after %v", took)
	}
	if total := srv.State().TotalConns; total != 0 {
		t.Fatalf("a lane counted the silent peer: %d", total)
	}
}

// TestLoopTLS_tokensStayTheirs is P2's conformance over portable+TLS: N
// connections × N tokens through Routes and Bearer, buffers marked as the
// batch releases them, two lanes, under -race.
func TestLoopTLS_tokensStayTheirs(t *testing.T) {
	const conns, rounds = 16, 20
	markRecycled.Store(true)
	t.Cleanup(func() { markRecycled.Store(false) })
	log := &authLog{}
	routes, err := http1.Routes(http1.RouteValue{ID: 1, Path: "/whoami"}, http1.RouteValue{ID: 2, Path: "/health"})
	if err != nil {
		t.Fatal(err)
	}
	bearer, err := http1.Bearer(conformanceVerifier{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	app := corenet.HTTP1HandlerFunc(func(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
		if req.Route != 1 {
			res.Status = http.StatusNoContent
			return
		}
		tok := string(req.Authorization[len("Bearer "):])
		log.mu.Lock()
		log.recs = append(log.recs, authRec{token: tok, auth: req.Auth, lane: req.Lane})
		log.mu.Unlock()
		res.Body = append([]byte(nil), req.Auth.Subject()...)
	})
	_, addr := startTLSCodec(t, corenet.Chain[corenet.HTTP1Handler](app, routes, bearer))
	var wg sync.WaitGroup
	errc := make(chan error, conns)
	for i := range conns {
		wg.Go(func() { errc <- tokenRounds(t, tlsLoopDial, addr, i, rounds) })
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			t.Fatal(err)
		}
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.recs) != conns*rounds {
		t.Fatalf("%d requests served, want %d", len(log.recs), conns*rounds)
	}
	lanes := map[int]bool{}
	for _, r := range log.recs {
		i, _ := tokenIndex([]byte(r.token))
		if want := "user-" + strconv.Itoa(i); string(r.auth.Subject()) != want || r.auth.Claims() != uint64(i) {
			t.Fatalf("token %s kept subject %q claims %d, want %q", r.token, r.auth.Subject(), r.auth.Claims(), want)
		}
		lanes[r.lane] = true
	}
	if len(lanes) != 2 {
		t.Fatalf("served on lanes %v, want both", lanes)
	}
}

// TestLoopTLS_refused pins what Start refuses with TLS (R6): a raw loop
// handler, a zero identity, an explicit native backend and a loop
// middleware, each with LoopTLSUnsupported.
func TestLoopTLS_refused(t *testing.T) {
	t.Parallel()
	codec := http1.HandleLoop(corenet.HTTP1HandlerFunc(func(*corenet.HTTP1RequestValue, *corenet.HTTP1ResponseValue) {}), http1.Parser{})
	id := testIdentity(t)
	for _, c := range []struct {
		name, option string
		h            corenet.LoopHandler
		opts         []GroupOption
		mw           bool
	}{
		{"a raw loop handler", "", &loopLineH{}, []GroupOption{TLS(id)}, false},
		{"a zero identity", "TLS", codec, []GroupOption{TLS(corenet.IdentityValue{})}, false},
		{"an explicit native backend", "Loop", codec, []GroupOption{TLS(id), Loop(corenet.LoopValue{Backend: corenet.LoopBackendEpoll})}, false},
		{"a loop middleware", "UseLoop", codec, []GroupOption{TLS(id)}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv := New()
			t.Cleanup(func() { swallowErr(srv.Close()) })
			g := srv.Group("g", append([]GroupOption{Listen("tcp", "127.0.0.1:0")}, c.opts...)...)
			if c.mw {
				g.UseLoop(func(next corenet.LoopHandler) corenet.LoopHandler { return next })
			}
			g.HandleLoop(c.h)
			err := srv.Start(t.Context())
			if !errs.HasCode(err, corenet.CodeLoopTLSUnsupported) {
				t.Fatalf("Start = %v, want LoopTLSUnsupported", err)
			}
			if c.option != "" && !hasField(err, "option", c.option) {
				t.Fatalf("Start = %v, want option=%s", err, c.option)
			}
		})
	}
}

// hasField reports whether err carries the string field key=want.
func hasField(err error, key, want string) bool {
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == key {
			return f.StringValue() == want
		}
	}
	return false
}
