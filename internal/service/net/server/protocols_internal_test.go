package server

import (
	"crypto/tls"
	"io"
	"net/http"
	"slices"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// Test_offered pins the ALPN list a TLS group offers: its own list when the
// identity names none, else the identity's entries the group serves, in the
// identity's order, and a refusal for an HTTP version the group excludes or
// for nothing in common.
func Test_offered(t *testing.T) {
	t.Parallel()
	both := []string{protoH2, protoHTTP1}
	for _, c := range []struct {
		name         string
		next, protos []string
		want         []string
		reason       string
	}{
		{"no NextProtos", nil, both, both, ""},
		{"the identity's order", []string{protoHTTP1, protoH2}, both, []string{protoHTTP1, protoH2}, ""},
		{"a foreign entry dropped", []string{"acme-tls/1", protoH2}, both, []string{protoH2}, ""},
		{"h2 excluded", []string{protoH2, protoHTTP1}, []string{protoHTTP1}, nil, "NextProtos h2"},
		{"HTTP/1.1 excluded", []string{protoHTTP1}, []string{protoH2}, nil, "NextProtos http/1.1"},
		{"nothing in common", []string{"acme-tls/1"}, both, nil, "NextProtos"},
	} {
		got, reason := offered(c.next, c.protos)
		if !slices.Equal(got, c.want) || reason != c.reason {
			t.Errorf("%s: %v %q, want %v %q", c.name, got, reason, c.want, c.reason)
		}
	}
}

// TestHTTPAdapter_protocols pins what a TLS HTTP group hands net/http, once a
// request has launched it: the Protocols computed at Start, the HTTP/2
// bounds, never the identity in a TLSConfig — the listener's negotiates — and the
// listener's ALPN list from the same computation.
func TestHTTPAdapter_protocols(t *testing.T) {
	t.Parallel()
	srv := New()
	group := srv.Group("api", Listen("tcp", "127.0.0.1:0"), TLS(testIdentity(t)), Protocols(protoHTTP1, protoH2),
		WriteTimeout(3*time.Second))
	group.HandleHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	if alpn := group.serverTLS().NextProtos; !slices.Equal(alpn, []string{protoHTTP1, protoH2}) {
		t.Fatalf("listener ALPN %v", alpn)
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // a self-signed test server
	t.Cleanup(tr.CloseIdleConnections)
	resp, err := (&http.Client{Transport: tr}).Get("https://" + srv.State().Listeners[0].Address + "/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = resp.Body.Close()
	group.httpAdapter.mu.RLock()
	hs := group.httpAdapter.server
	group.httpAdapter.mu.RUnlock()
	//: net/http's HTTP/2 setup fills an empty TLSConfig of its own on Serve;
	//: none of the group's identity is in it, so it never negotiates.
	if hs.TLSConfig != nil && (len(hs.TLSConfig.Certificates) != 0 || hs.TLSConfig.GetCertificate != nil) {
		t.Fatal("http.Server.TLSConfig carries a certificate")
	}
	if hs.Protocols == nil || !hs.Protocols.HTTP1() || !hs.Protocols.HTTP2() || hs.Protocols.UnencryptedHTTP2() {
		t.Fatalf("Protocols %v", hs.Protocols)
	}
	want := http2Config(corenet.DurationValue(3 * time.Second))
	if hs.HTTP2 == nil || hs.HTTP2.MaxConcurrentStreams != want.MaxConcurrentStreams ||
		hs.HTTP2.MaxReadFrameSize != want.MaxReadFrameSize ||
		hs.HTTP2.MaxDecoderHeaderTableSize != want.MaxDecoderHeaderTableSize ||
		hs.HTTP2.MaxEncoderHeaderTableSize != want.MaxEncoderHeaderTableSize ||
		hs.HTTP2.MaxReceiveBufferPerStream != h2ReceivePerStream ||
		hs.HTTP2.MaxReceiveBufferPerConnection != h2ReceivePerConn ||
		hs.HTTP2.WriteByteTimeout != 3*time.Second {
		t.Fatalf("HTTP2 %+v, want %+v", hs.HTTP2, want)
	}
}
