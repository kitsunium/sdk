// Package server_test — h2c by prior knowledge on an HTTP group given
// Plaintext (ADR 0195 amended, #861): a Go client with
// Protocols.SetUnencryptedHTTP2 speaks HTTP/2, an HTTP/1.1 client is served
// beside it when the group lists http/1.1 and refused when it does not, and a
// request carrying Upgrade: h2c gets HTTP/1.1's answer without switching.
package server_test

import (
	"bytes"
	"errors"
	"io"
	stdnet "net"
	"net/http"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/service/net/server"
)

// startH2c starts an HTTP group with opts on a loopback port serving a
// handler that names the protocol it was reached over.
func startH2c(t *testing.T, opts ...server.GroupOption) *server.Server {
	t.Helper()
	srv := server.New()
	t.Cleanup(func() { _ = srv.Close() })
	all := append([]server.GroupOption{server.Listen("tcp", "127.0.0.1:0"), server.Plaintext()}, opts...)
	srv.Group("web", all...).HandleHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Proto)
	}))
	if err := srv.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	return srv
}

func h2cClientExt(t *testing.T) *http.Client {
	t.Helper()
	tr := &http.Transport{}
	tr.Protocols = new(http.Protocols)
	tr.Protocols.SetUnencryptedHTTP2(true)
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 10 * time.Second}
}

// getProto issues a GET and returns the response's major version and body.
func getProto(t *testing.T, cli *http.Client, addr string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := cli.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.ProtoMajor, string(body)
}

// http1Raw sends raw bytes on a fresh connection and returns everything read
// until the server closes, failing the test when 3 s pass first: a
// connection the server left open is not one it answered and closed. A
// reset is a close too: a server that hangs up with request bytes unread
// sends one.
func http1Raw(t *testing.T, addr, raw string) []byte {
	t.Helper()
	conn, err := stdnet.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, raw); err != nil {
		t.Fatal(err)
	}
	all, err := io.ReadAll(conn)
	if ne, ok := errors.AsType[stdnet.Error](err); ok && ne.Timeout() {
		t.Fatalf("the server neither answered nor closed within 3 s: read %q", all)
	}
	return all
}

func TestProtocols_h2c(t *testing.T) {
	t.Run("h2 and http1 on one listener", func(t *testing.T) {
		srv := startH2c(t, server.Protocols("h2", "http/1.1"))
		addr := srv.State().Listeners[0].Address
		if !srv.State().Listeners[0].H2C {
			t.Fatal("State does not name h2c")
		}
		if major, body := getProto(t, h2cClientExt(t), addr); major != 2 || body != "HTTP/2.0" {
			t.Fatalf("h2c client: proto %d body %q", major, body)
		}
		all := http1Raw(t, addr, "GET / HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n")
		if !bytes.HasPrefix(all, []byte("HTTP/1.1 200")) || !bytes.HasSuffix(all, []byte("HTTP/1.1")) {
			t.Fatalf("HTTP/1.1 client: %q", all)
		}
	})
	t.Run("h2 alone refuses HTTP/1.1", func(t *testing.T) {
		srv := startH2c(t, server.Protocols("h2"))
		addr := srv.State().Listeners[0].Address
		if major, body := getProto(t, h2cClientExt(t), addr); major != 2 || body != "HTTP/2.0" {
			t.Fatalf("h2c client: proto %d body %q", major, body)
		}
		// No answer, and the connection closed: http1Raw fails on one left
		// open.
		if all := http1Raw(t, addr, "GET / HTTP/1.1\r\nHost: h\r\n\r\n"); len(all) != 0 {
			t.Fatalf("HTTP/1.1 client on an h2-only group read %q, want the connection closed with nothing", all)
		}
	})
	t.Run("Upgrade h2c is answered over HTTP/1.1", func(t *testing.T) {
		srv := startH2c(t, server.Protocols("h2", "http/1.1"))
		addr := srv.State().Listeners[0].Address
		all := http1Raw(t, addr, "GET / HTTP/1.1\r\nHost: h\r\nUpgrade: h2c\r\nConnection: Upgrade, HTTP2-Settings, close\r\nHTTP2-Settings: AAMAAABkAAQAoAAA\r\n\r\n")
		if !bytes.HasPrefix(all, []byte("HTTP/1.1 200")) || !bytes.HasSuffix(all, []byte("HTTP/1.1")) {
			t.Fatalf("Upgrade request: %q", all)
		}
	})
}
