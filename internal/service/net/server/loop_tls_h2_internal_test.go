// Package server — HTTP/2 beside HTTP/1.1 on a TLS codec group (#964): the h2
// adapter offers h2 then http/1.1 by ALPN, the codec chosen by the preface on
// the portable backend's lanes; one Routes+Bearer chain serves a Go h2 client
// and an HTTP/1.1-only one; Protocols("h2") refuses a client offering
// http/1.1 alone; a native backend is refused as for the http1 codec.
package server

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/http1"
	"github.com/kitsunium/sdk/internal/service/net/http2"
)

// startTLSH2 serves h through the h2 adapter with TLS, two lanes.
func startTLSH2(t *testing.T, h corenet.HTTP1Handler, opts ...GroupOption) (*Server, string) {
	t.Helper()
	srv := New()
	t.Cleanup(func() { swallowErr(srv.Close()) })
	opts = append([]GroupOption{Listen("tcp", "127.0.0.1:0"), TLS(testIdentity(t)), Loop(corenet.LoopValue{Loops: 2})}, opts...)
	srv.Group("tls", opts...).HandleLoop(http2.HandleLoop(h, http1.Parser{}))
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return srv, srv.State().Listeners[0].Address
}

// tlsH2Client is a Go https client that prefers h2, trusting any certificate.
func tlsH2Client(t *testing.T) *http.Client {
	t.Helper()
	tr := &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // a self-signed test server
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 20 * time.Second}
}

// tlsH2TokenRounds is h2TokenRounds over TLS: rounds of /whoami with token i
// through a Go h2 client, each followed by a request with no token refused 401.
func tlsH2TokenRounds(t *testing.T, cli *http.Client, addr string, i, rounds int) error {
	t.Helper()
	for range rounds {
		for _, authed := range []bool{true, false} {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://"+addr+"/whoami", nil)
			if err != nil {
				return err
			}
			if authed {
				req.Header.Set("Authorization", "Bearer tok-"+strconv.Itoa(i))
			}
			resp, err := cli.Do(req)
			if err != nil {
				return err
			}
			body, err := io.ReadAll(resp.Body)
			swallowErr(resp.Body.Close())
			if err != nil {
				return err
			}
			want, status := "user-"+strconv.Itoa(i), http.StatusOK
			if !authed {
				status = http.StatusUnauthorized
			}
			if resp.ProtoMajor != 2 || resp.StatusCode != status || (authed && string(body) != want) {
				return errTest("whoami over h2: proto " + strconv.Itoa(resp.ProtoMajor) + " status " + resp.Status + " body " + string(body))
			}
		}
	}
	return nil
}

// TestLoopTLSH2_bothVersions is #964's first criterion: a TLS loop group with
// the h2 adapter and no Protocols offers ["h2", "http/1.1"], State saying so;
// a Go client gets HTTP/2 and an HTTP/1.1-only one HTTP/1.1, both through one
// Routes+Bearer chain on two lanes, under -race.
func TestLoopTLSH2_bothVersions(t *testing.T) {
	const conns, rounds = 6, 10
	log := &authLog{}
	routes, err := http1.Routes(http1.RouteValue{ID: 1, Path: "/whoami"})
	if err != nil {
		t.Fatal(err)
	}
	bearer, err := http1.Bearer(conformanceVerifier{})
	if err != nil {
		t.Fatal(err)
	}
	app := corenet.HTTP1HandlerFunc(func(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
		log.mu.Lock()
		log.recs = append(log.recs, authRec{auth: req.Auth, lane: req.Lane})
		log.mu.Unlock()
		res.Body = append([]byte(nil), req.Auth.Subject()...)
	})
	srv, addr := startTLSH2(t, corenet.Chain[corenet.HTTP1Handler](app, routes, bearer))
	if got := srv.State().Listeners[0].ALPN; !slices.Equal(got, []string{"h2", "http/1.1"}) {
		t.Fatalf("State ALPN = %q, want [h2 http/1.1]", got)
	}
	if c, err := tlsDial(t, addr, "h2", "http/1.1"); err != nil || c.ConnectionState().NegotiatedProtocol != "h2" {
		t.Fatalf("a client offering both: %v", err)
	}
	var wg sync.WaitGroup
	errc := make(chan error, 2*conns)
	for i := range conns {
		cli := tlsH2Client(t)
		wg.Go(func() { errc <- tlsH2TokenRounds(t, cli, addr, i, rounds) })
		wg.Go(func() { errc <- tokenRounds(t, tlsLoopDial, addr, conns+i, rounds) })
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
	if len(log.recs) != 2*conns*rounds {
		t.Fatalf("%d requests reached the handler, want %d", len(log.recs), 2*conns*rounds)
	}
	row := srv.State().Listeners[0]
	if row.HTTP2Requests == 0 || row.HTTP1Requests == 0 {
		t.Fatalf("State counts http1 %d http2 %d, want both served", row.HTTP1Requests, row.HTTP2Requests)
	}
}

// TestLoopTLSH2_protocols is #964's second criterion, as crypto/tls allows
// it: Protocols("h2") on the h2 adapter offers h2 alone, and a client offering
// h2 is served HTTP/2. A client offering http/1.1 alone completes the
// handshake with no protocol — crypto/tls lets an http/1.1 client reach an
// h2-only server so (Go issue 46310), as on an HTTP group — and its HTTP/1.1
// request is answered 400 and closed; a client offering nothing the group
// serves, http/1.1 alone excepted, fails the handshake with
// no_application_protocol. The http1 adapter still refuses Protocols("h2")
// with ProtocolsRefused naming h2.
func TestLoopTLSH2_protocols(t *testing.T) {
	hello := corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) { res.Body = []byte("ok") })
	srv, addr := startTLSH2(t, hello, Protocols("h2"))
	if got := srv.State().Listeners[0].ALPN; !slices.Equal(got, []string{"h2"}) {
		t.Fatalf("State ALPN = %q, want [h2]", got)
	}
	c, err := tlsDial(t, addr, "http/1.1")
	if err != nil {
		t.Fatalf("crypto/tls's http/1.1 fallback: %v", err)
	}
	if p := c.ConnectionState().NegotiatedProtocol; p != "" {
		t.Fatalf("negotiated %q with a client offering http/1.1 alone", p)
	}
	if _, err := c.Write([]byte("GET / HTTP/1.1\r\nHost: h\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	all, _ := io.ReadAll(c)
	if !strings.HasPrefix(string(all), "HTTP/1.1 400") {
		t.Fatalf("HTTP/1.1 on an h2-only group: %q, want a 400 and a close", all)
	}
	resp, err := tlsH2Client(t).Get("https://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	swallowErr(resp.Body.Close())
	if resp.ProtoMajor != 2 {
		t.Fatalf("served HTTP/%d, want 2", resp.ProtoMajor)
	}
	// The refusal itself, where crypto/tls makes no exception: a client
	// offering only protocols the group does not serve fails the handshake
	// with no_application_protocol (#964's amended criterion).
	if c, err := tlsDial(t, addr, "spdy/3", "http/1.0"); err == nil || !strings.Contains(err.Error(), "no application protocol") {
		if c != nil {
			t.Fatalf("a client offering spdy/3 and http/1.0 negotiated %q", c.ConnectionState().NegotiatedProtocol)
		}
		t.Fatalf("a client offering spdy/3 and http/1.0: %v, want no_application_protocol", err)
	}
	refused := New()
	t.Cleanup(func() { swallowErr(refused.Close()) })
	refused.Group("g", Listen("tcp", "127.0.0.1:0"), TLS(testIdentity(t)), Protocols("h2")).HandleLoop(http1.HandleLoop(hello, http1.Parser{}))
	if err := refused.Start(t.Context()); !errs.HasCode(err, corenet.CodeProtocolsRefused) || !hasField(err, "reason", "h2") {
		t.Fatalf("http1 adapter with Protocols(h2): Start = %v, want ProtocolsRefused reason=h2", err)
	}
}

// TestLoopTLSH2_nativeRefused is #964's third criterion: Loop naming epoll or
// io_uring with TLS on the h2 adapter is refused with LoopTLSUnsupported
// naming Loop, as for the http1 adapter.
func TestLoopTLSH2_nativeRefused(t *testing.T) {
	hello := corenet.HTTP1HandlerFunc(func(*corenet.HTTP1RequestValue, *corenet.HTTP1ResponseValue) {})
	for _, b := range []corenet.LoopBackend{corenet.LoopBackendEpoll, corenet.LoopBackendIOURing} {
		t.Run(b.String(), func(t *testing.T) {
			srv := New()
			t.Cleanup(func() { swallowErr(srv.Close()) })
			srv.Group("g", Listen("tcp", "127.0.0.1:0"), TLS(testIdentity(t)), Loop(corenet.LoopValue{Backend: b})).
				HandleLoop(http2.HandleLoop(hello, http1.Parser{}))
			err := srv.Start(t.Context())
			if !errs.HasCode(err, corenet.CodeLoopTLSUnsupported) || !hasField(err, "option", "Loop") {
				t.Fatalf("Start = %v, want LoopTLSUnsupported option=Loop", err)
			}
		})
	}
}

// TestLoopTLSH2_partialPreface pins the preface's bound on a TLS group: the
// sniff reads at most the preface's 24 bytes, holding them in the spill, and
// a client that negotiated h2 then stalls one byte short of it is closed by
// the group's IdleTimeout, never served nor held.
func TestLoopTLSH2_partialPreface(t *testing.T) {
	hello := corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) { res.Body = []byte("ok") })
	_, addr := startTLSH2(t, hello, IdleTimeout(300*time.Millisecond))
	c, err := tlsDial(t, addr, "h2")
	if err != nil {
		t.Fatal(err)
	}
	preface := "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"
	if _, err := c.Write([]byte(preface[:len(preface)-1])); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	n, _ := io.Copy(io.Discard, c)
	if n != 0 {
		t.Fatalf("a partial preface was answered %d bytes", n)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("closed after %v, want the 300ms idle bound", d)
	}
}
