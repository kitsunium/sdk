package static_test

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kitsunium/sdk/internal/service/net/static"
)

// precompressedTree is a tree with sidecars, and one with a sidecar that is a
// directory.
func precompressedTree() fstest.MapFS {
	return fstest.MapFS{
		"app.js":    {Data: []byte("the script")},
		"app.js.gz": {Data: []byte("gzip bytes")},
		"app.js.br": {Data: []byte("brotli bytes")},
		"plain.txt": {Data: []byte("identity only")},
		"dir.js":    {Data: []byte("a script")},
		"dir.js.gz": {Mode: fs.ModeDir | 0o755},
	}
}

// fetch runs one GET against h with the given Accept-Encoding.
func fetch(t *testing.T, h http.Handler, target, accept string) *http.Response {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	if accept != "" {
		req.Header.Set("Accept-Encoding", accept)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

// read drains a response's body.
func read(t *testing.T, resp *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// TestPrecompressed pins #853's second criterion: app.js.gz for /app.js when the
// client allows it, typed text/javascript, and app.js itself otherwise.
func TestPrecompressed(t *testing.T) {
	h, err := static.NewHandler(precompressedTree(), static.Config{Precompressed: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, accept, body, coding string
	}{
		{"gzip", "gzip", "gzip bytes", "gzip"},
		{"brotli first", "gzip, br", "brotli bytes", "br"},
		{"only br", "br", "brotli bytes", "br"},
		{"neither", "deflate", "the script", ""},
		{"none", "", "the script", ""},
		{"q=0", "gzip;q=0", "the script", ""},
		{"star", "*", "brotli bytes", "br"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := fetch(t, h, "/app.js", tc.accept)
			if got := read(t, resp); got != tc.body {
				t.Fatalf("body = %q, want %q", got, tc.body)
			}
			if got := resp.Header.Get("Content-Encoding"); got != tc.coding {
				t.Fatalf("Content-Encoding = %q, want %q", got, tc.coding)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
				t.Fatalf("Content-Type = %q", ct)
			}
			if !strings.Contains(strings.Join(resp.Header.Values("Vary"), ","), "Accept-Encoding") {
				t.Fatalf("Vary = %q", resp.Header.Values("Vary"))
			}
		})
	}
}

// TestPrecompressed_identityWithoutSidecar pins a file no sidecar exists for.
func TestPrecompressed_identityWithoutSidecar(t *testing.T) {
	h, err := static.NewHandler(precompressedTree(), static.Config{Precompressed: true})
	if err != nil {
		t.Fatal(err)
	}
	resp := fetch(t, h, "/plain.txt", "gzip")
	if read(t, resp) != "identity only" || resp.Header.Get("Content-Encoding") != "" {
		t.Fatalf("resp: %v", resp.Header)
	}
}

// TestPrecompressed_neverADirectorySidecar pins that a directory in the
// sidecar's place is not served, and the file is.
func TestPrecompressed_neverADirectorySidecar(t *testing.T) {
	h, err := static.NewHandler(precompressedTree(), static.Config{Precompressed: true})
	if err != nil {
		t.Fatal(err)
	}
	resp := fetch(t, h, "/dir.js", "gzip")
	if got := read(t, resp); got != "a script" || resp.Header.Get("Content-Encoding") != "" {
		t.Fatalf("got %q headers %v", got, resp.Header)
	}
}

// TestPrecompressed_off pins that the option is off by default.
func TestPrecompressed_off(t *testing.T) {
	h, err := static.NewHandler(precompressedTree(), static.Config{})
	if err != nil {
		t.Fatal(err)
	}
	resp := fetch(t, h, "/app.js", "gzip")
	if read(t, resp) != "the script" || resp.Header.Get("Vary") != "" {
		t.Fatalf("headers %v", resp.Header)
	}
}
