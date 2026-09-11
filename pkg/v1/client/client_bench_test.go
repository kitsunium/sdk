package client_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/client"
	"github.com/kitsunium/sdk/pkg/v1/tlsid"
)

// sinks so no response or error can be proven unused and elided.
var (
	respSink   client.Response
	errSink    error
	clientSink *client.Client
)

// benchBody is what the fake origin returns: a small JSON document, the shape
// almost every internal call actually carries.
var benchBody = []byte(`{"id":"01HQ8Z3M4N5P6Q7R8S9T0V1W2X","status":"active"}`)

// benchPolicy is the read-only policy from this package's own tests, built
// through the public surface only.
func benchPolicy(b *testing.B) client.Policy {
	b.Helper()
	allow, err := client.AllowPaths(`/v1/(subscribers|supi/[^/]+)`)
	if err != nil {
		b.Fatalf("AllowPaths: %v", err)
	}
	deny, err := client.DenyPaths(`/v1/authentication.*`)
	if err != nil {
		b.Fatalf("DenyPaths: %v", err)
	}
	return client.Policies(client.AllowMethods(http.MethodGet), deny, allow)
}

// benchOrigin is a loopback server that answers immediately, so every number
// below is the CLIENT's cost with the network held at its floor — which is what
// isolates the SDK's contribution from the round trip it wraps.
func benchOrigin(b *testing.B) *httptest.Server {
	b.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(benchBody); err != nil {
			b.Errorf("origin write: %v", err)
		}
	}))
	b.Cleanup(srv.Close)
	return srv
}

// BenchmarkGet_Admitted is the whole outbound call: resolve the path, run the
// policy, dial (pooled), read the bounded body. It is what one internal HTTP
// call costs before the origin does any work.
func BenchmarkGet_Admitted(b *testing.B) {
	srv := benchOrigin(b)
	cl, err := client.New(client.Config{BaseURL: srv.URL}, tlsid.Identity{}, benchPolicy(b), nil)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		respSink, errSink = cl.Get(ctx, "/v1/subscribers", nil)
	}
	if errSink != nil {
		b.Fatalf("Get: %v", errSink)
	}
}

// BenchmarkGet_WithQuery adds four query parameters, so the delta is what
// building and encoding a query costs on top of the call.
func BenchmarkGet_WithQuery(b *testing.B) {
	srv := benchOrigin(b)
	cl, err := client.New(client.Config{BaseURL: srv.URL}, tlsid.Identity{}, benchPolicy(b), nil)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	query := url.Values{
		"page":   {"3"},
		"limit":  {"50"},
		"sort":   {"created_at"},
		"filter": {"status:active"},
	}
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		respSink, errSink = cl.Get(ctx, "/v1/subscribers", query)
	}
	if errSink != nil {
		b.Fatalf("Get: %v", errSink)
	}
}

// BenchmarkGet_PolicyRefused is the number that decides whether a policy can be
// broad: a refused path must cost far less than an admitted one, or the policy
// becomes the expensive part of every call it blocks — and it must never reach
// the socket.
func BenchmarkGet_PolicyRefused(b *testing.B) {
	srv := benchOrigin(b)
	cl, err := client.New(client.Config{BaseURL: srv.URL}, tlsid.Identity{}, benchPolicy(b), nil)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		respSink, errSink = cl.Get(ctx, "/v1/authentication/tokens", nil)
	}
	if errSink == nil {
		b.Fatal("a denied path was admitted")
	}
}

// BenchmarkGet_Parallel is what a service under load actually does: N goroutines
// sharing one client and its connection pool.
func BenchmarkGet_Parallel(b *testing.B) {
	srv := benchOrigin(b)
	cl, err := client.New(client.Config{BaseURL: srv.URL}, tlsid.Identity{}, benchPolicy(b), nil)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		ctx := b.Context()
		for pb.Next() {
			if _, err := cl.Get(ctx, "/v1/subscribers", nil); err != nil {
				b.Errorf("Get: %v", err)
				return
			}
		}
	})
}

// BenchmarkNew prices construction, which happens once per dependency at wiring
// time and compiles the policy's patterns.
func BenchmarkNew(b *testing.B) {
	srv := benchOrigin(b)
	policy := benchPolicy(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		built, err := client.New(client.Config{BaseURL: srv.URL}, tlsid.Identity{}, policy, nil)
		if err != nil {
			b.Fatalf("New: %v", err)
		}
		clientSink = built
	}
}
