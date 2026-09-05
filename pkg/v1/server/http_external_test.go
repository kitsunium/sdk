package server_test

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/server"
	"github.com/kitsunium/sdk/pkg/v1/tlsid"
)

// startHTTP mounts a handler on our engine and returns its base URL.
func startHTTP(t *testing.T, h http.Handler, opts ...server.GroupOption) (srv *server.Server, base string) {
	t.Helper()
	srv = server.New()
	all := append([]server.GroupOption{server.Listen("tcp", "127.0.0.1:0")}, opts...)
	srv.Group("api", all...).HandleHTTP(h)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })
	return srv, "http://" + srv.State().Listeners[0].Address
}

// TestHTTPAdapterServesRealRequests is the D3 proof: net/http does the protocol
// over our listener, and a stock http.Client is served correctly.
func TestHTTPAdapterServesRealRequests(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Method", r.Method)
		if _, err := io.WriteString(w, "hello from the adapter"); err != nil {
			t.Errorf("write: %v", err)
		}
	})
	_, base := startHTTP(t, mux)

	resp := get(t, base+"/hello")
	if resp.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.status)
	}
	if resp.body != "hello from the adapter" {
		t.Fatalf("body = %q", resp.body)
	}
	if resp.method != http.MethodGet {
		t.Fatalf("the handler saw method %q", resp.method)
	}
}

// TestHTTPAdapterKeepsAliveAcrossRequests pins that the bridge hands over a
// CONNECTION, not a request: several requests must reuse one connection, or the
// adapter would be paying a hand-off per request rather than per connection.
func TestHTTPAdapterKeepsAliveAcrossRequests(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/n", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, "ok"); err != nil {
			t.Errorf("write: %v", err)
		}
	})
	srv, base := startHTTP(t, mux)

	client := &http.Client{Timeout: 5 * time.Second}
	for range 3 {
		resp, err := client.Get(base + "/n")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if _, cerr := io.Copy(io.Discard, resp.Body); cerr != nil {
			t.Fatalf("drain: %v", cerr)
		}
		closeOrFail(t, resp.Body)
	}
	//: three requests over a reused connection must count as ONE accept.
	if total := srv.State().TotalConns; total != 1 {
		t.Fatalf("TotalConns = %d after three keep-alive requests, want 1", total)
	}
}

// TestHTTPAdapterOverTLS pins that the adapter inherits the group's identity:
// HTTPS comes from our listener, not from http.Server's own TLS handling.
func TestHTTPAdapterOverTLS(t *testing.T) {
	t.Parallel()
	certPEM, keyPEM := mintCert(t)
	identity, err := tlsid.New(tlsid.Params{CertPEM: certPEM, KeyPEM: keyPEM})
	if err != nil {
		t.Fatalf("server identity: %v", err)
	}
	clientID, err := tlsid.New(tlsid.Params{RootsPEM: certPEM, ServerName: "kitsunium-test"})
	if err != nil {
		t.Fatalf("client identity: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/secure", func(w http.ResponseWriter, r *http.Request) {
		//: the request must arrive over a completed TLS connection.
		if r.TLS == nil {
			t.Error("the handler saw a plaintext request on a TLS group")
		}
		if _, werr := io.WriteString(w, "encrypted"); werr != nil {
			t.Errorf("write: %v", werr)
		}
	})
	srv, _ := startHTTP(t, mux, server.TLS(identity))

	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: clientID.ClientConfig()},
	}
	resp, err := client.Get("https://" + srv.State().Listeners[0].Address + "/secure")
	if err != nil {
		t.Fatalf("https get: %v", err)
	}
	defer closeOrFail(t, resp.Body)
	body, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		t.Fatalf("read: %v", rerr)
	}
	if string(body) != "encrypted" {
		t.Fatalf("body = %q", body)
	}
}

// TestHTTPAdapterDrainsOnShutdown pins that an idle keep-alive connection does
// not hold the drain open for its whole budget. Without the adapter shutting
// its http.Server down, ServeConn would sit waiting on a connection that may
// never see another request.
func TestHTTPAdapterDrainsOnShutdown(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, "ok"); err != nil {
			t.Errorf("write: %v", err)
		}
	})
	srv, base := startHTTP(t, mux)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(base + "/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, cerr := io.Copy(io.Discard, resp.Body); cerr != nil {
		t.Fatalf("drain: %v", cerr)
	}
	closeOrFail(t, resp.Body)
	//: the connection is now idle but still open, held by keep-alive.
	waitFor(t, func() bool { return srv.State().ActiveConns == 1 })

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started := time.Now()
	if serr := srv.Shutdown(ctx); serr != nil {
		t.Fatalf("shutdown reported %v, want a clean drain", serr)
	}
	//: a drain that took the whole budget means the keep-alive was never
	//: released, which is the bug this test exists to catch.
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("drain took %v — the idle keep-alive was not released", elapsed)
	}
	if phase := srv.State().Phase; phase != server.PhaseStopped {
		t.Fatalf("phase = %v, want stopped", phase)
	}
}

// httpResult is what get observed.
type httpResult struct {
	status int
	body   string
	method string
}

// get performs one HTTP request and returns the observed result.
func get(t *testing.T, url string) httpResult {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer closeOrFail(t, resp.Body)
	body, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		t.Fatalf("read: %v", rerr)
	}
	return httpResult{status: resp.StatusCode, body: string(body), method: resp.Header.Get("X-Method")}
}
