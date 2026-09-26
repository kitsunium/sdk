// Package static_test — the facade as a consumer meets it: an application
// served under a prefix, over a real connection.
package static_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kitsunium/sdk/pkg/v1/server/static"
)

// TestTheShortestUsefulStaticSite is the facade's acceptance criterion: a
// single-page application mounted under a prefix, reached over a real
// connection, answering a route with its shell, a hashed asset for good, a
// missing script with a 404, and every answer with the security headers.
func TestTheShortestUsefulStaticSite(t *testing.T) {
	t.Parallel()
	tree := fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>app</title>")},
		"assets/app-3f2a9c.js": {Data: []byte("console.log('app')")},
	}
	assets, err := static.New(tree, static.Config{
		SinglePageApp: true,
		Immutable:     func(name string) bool { return strings.HasPrefix(name, "assets/") },
	})
	//: a valid configuration.
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /app/", http.StripPrefix("/app", assets))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	type tc struct {
		path       string
		wantStatus int
		wantBody   string
		wantCache  string
	}
	tests := []tc{
		{"/app/settings", http.StatusOK, "<!doctype html><title>app</title>", static.RevalidateCacheControl},
		{"/app/assets/app-3f2a9c.js", http.StatusOK, "console.log('app')", static.ImmutableCacheControl},
		{"/app/assets/missing.js", http.StatusNotFound, "", static.RevalidateCacheControl},
	}
	//: each path, over the connection.
	for _, c := range tests {
		response, err := http.Get(server.URL + c.path)
		//: the loopback server answers.
		if err != nil {
			t.Fatalf("GET %s: %v", c.path, err)
		}
		body, err := io.ReadAll(response.Body)
		//: a whole body.
		if err != nil {
			t.Fatalf("GET %s body: %v", c.path, err)
		}
		//: nothing held past the read.
		if err := response.Body.Close(); err != nil {
			t.Errorf("GET %s close: %v", c.path, err)
		}
		//: the status.
		if response.StatusCode != c.wantStatus {
			t.Errorf("GET %s = %d, want %d", c.path, response.StatusCode, c.wantStatus)
		}
		//: the file.
		if c.wantBody != "" && string(body) != c.wantBody {
			t.Errorf("GET %s body = %q, want %q", c.path, body, c.wantBody)
		}
		//: the caching split.
		if got := response.Header.Get("Cache-Control"); got != c.wantCache {
			t.Errorf("GET %s Cache-Control = %q, want %q", c.path, got, c.wantCache)
		}
		//: the headers on every answer.
		if response.Header.Get("Content-Security-Policy") != static.DefaultContentSecurityPolicy ||
			response.Header.Get("X-Content-Type-Options") != "nosniff" ||
			response.Header.Get("Referrer-Policy") != static.DefaultReferrerPolicy {
			t.Errorf("GET %s is missing a security header: %v", c.path, response.Header)
		}
	}
}

// TestMisconfiguredIsTheEnginesSentinel pins that the facade re-exports the
// core value rather than a copy: New's refusal matches it.
func TestMisconfiguredIsTheEnginesSentinel(t *testing.T) {
	t.Parallel()
	handler, err := static.New(nil, static.Config{})
	//: refused, and matchable through the facade.
	if handler != nil || !errors.Is(err, static.Misconfigured) {
		t.Fatalf("New(nil) = %v, %v; want nil and Misconfigured", handler, err)
	}
}
