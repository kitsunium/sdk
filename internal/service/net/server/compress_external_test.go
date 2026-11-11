package server_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// serveCompressed mounts h under Compress on a plaintext HTTP group.
func serveCompressed(t *testing.T, v corenet.CompressValue, h http.Handler) string {
	t.Helper()
	mw, err := server.Compress(v)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	srv := server.New()
	srv.Group("web", server.Listen("tcp", "127.0.0.1:0"), server.Plaintext()).HandleHTTP(mw(h))
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { discard(srv.Close()) })
	return "http://" + srv.State().Listeners[0].Address
}

// get performs a request with Accept-Encoding as given, the raw wire body kept.
func get(t *testing.T, url, encoding, method string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Accept-Encoding", encoding)
	if encoding == "none" {
		req.Header.Del("Accept-Encoding")
	}
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	t.Cleanup(client.CloseIdleConnections)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { discard(resp.Body.Close()) }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return resp, body
}

// sized returns a handler that writes size bytes of contentType.
func sized(contentType string, size int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(bytes.Repeat([]byte("compress me please, "), size/20+1)[:size])
	}
}

// TestCompress_acceptance pins #853's first criterion: 64 KiB of text/plain is
// gzip-encoded with Vary under 10 KiB on the wire, and every other case is
// identity.
func TestCompress_acceptance(t *testing.T) {
	url := serveCompressed(t, corenet.CompressValue{}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/small":
			sized("text/plain; charset=utf-8", 512)(w, r)
		case "/png":
			sized("image/png", 64<<10)(w, r)
		default:
			sized("text/plain; charset=utf-8", 64<<10)(w, r)
		}
	}))
	resp, wire := get(t, url+"/", "gzip, deflate", http.MethodGet)
	if resp.Header.Get("Content-Encoding") != "gzip" || !strings.Contains(resp.Header.Get("Vary"), "Accept-Encoding") {
		t.Fatalf("headers: %v", resp.Header)
	}
	if len(wire) >= 10<<10 {
		t.Fatalf("wire size %d, want under 10 KiB", len(wire))
	}
	gz, err := gzip.NewReader(bytes.NewReader(wire))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	plain, err := io.ReadAll(gz)
	if err != nil || len(plain) != 64<<10 {
		t.Fatalf("plain: %d, %v", len(plain), err)
	}
	for _, tc := range []struct{ name, path, encoding string }{
		{"no header", "/", "none"},
		{"512 B", "/small", "gzip"},
		{"image", "/png", "gzip"},
		{"q=0", "/", "gzip;q=0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, wire := get(t, url+tc.path, tc.encoding, http.MethodGet)
			if ce := resp.Header.Get("Content-Encoding"); ce != "" {
				t.Fatalf("Content-Encoding = %q", ce)
			}
			if tc.path == "/" && len(wire) != 64<<10 {
				t.Fatalf("identity body %d bytes", len(wire))
			}
		})
	}
}

// TestCompress_neverCompresses pins the exclusions.
func TestCompress_neverCompresses(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    http.HandlerFunc
	}{
		{"head", nil},
		{"no content", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }},
		{"not modified", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotModified) }},
		{"already encoded", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Encoding", "br")
			sized("text/plain", 8000)(w, r)
		}},
		{"event stream", func(w http.ResponseWriter, r *http.Request) {
			sized("text/event-stream", 8000)(w, r)
		}},
		{"html by default", func(w http.ResponseWriter, r *http.Request) { sized("text/html", 8000)(w, r) }},
		{"no-transform", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-transform")
			sized("text/plain", 8000)(w, r)
		}},
		{"range answer", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Range", "bytes 0-1/2")
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("hi"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Handler(tc.h)
			method := http.MethodGet
			if tc.h == nil {
				h = sized("text/plain", 8000)
				method = http.MethodHead
			}
			url := serveCompressed(t, corenet.CompressValue{}, h)
			resp, _ := get(t, url, "gzip", method)
			if ce := resp.Header.Get("Content-Encoding"); ce != "" && ce != "br" {
				t.Fatalf("Content-Encoding = %q", ce)
			}
			if tc.name == "already encoded" && resp.Header.Get("Content-Encoding") != "br" {
				t.Fatal("the handler's encoding was replaced")
			}
		})
	}
}

// TestCompress_staysLive pins that a flushed stream reaches the client before
// the handler ends, compressed: the read returns before the handler is released.
func TestCompress_staysLive(t *testing.T) {
	release := make(chan struct{})
	url := serveCompressed(t, corenet.CompressValue{}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"chunk":1}`))
		w.(http.Flusher).Flush()
		<-release
		_, _ = w.Write([]byte(`{"chunk":2}`))
	}))
	defer close(release)
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	t.Cleanup(client.CloseIdleConnections)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { discard(resp.Body.Close()) }()
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q", resp.Header.Get("Content-Encoding"))
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	buf := make([]byte, 64)
	read := make(chan int, 1)
	go func() {
		n, _ := io.ReadAtLeast(gz, buf, len(`{"chunk":1}`))
		read <- n
	}()
	select {
	case n := <-read:
		if string(buf[:n]) != `{"chunk":1}` {
			t.Fatalf("read %q before the handler ended", buf[:n])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the flushed chunk never arrived")
	}
}

// TestCompress_refusesOutOfRange pins the constructor.
func TestCompress_refusesOutOfRange(t *testing.T) {
	for _, v := range []corenet.CompressValue{{Level: 42}, {Level: -2}, {MinBytes: -1}} {
		if _, err := server.Compress(v); !errs.HasCode(err, corenet.CodeCompressInvalid) {
			t.Fatalf("Compress(%+v) = %v", v, err)
		}
	}
}

// TestCompress_sniffsBeforeCompressing pins that a handler naming no type is
// typed on its bytes, not on gzip's.
func TestCompress_sniffsBeforeCompressing(t *testing.T) {
	url := serveCompressed(t, corenet.CompressValue{Types: []string{"text/plain"}}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, strings.Repeat("plain text ", 400))
	}))
	resp, _ := get(t, url, "gzip", http.MethodGet)
	if resp.Header.Get("Content-Encoding") != "gzip" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("headers: %v", resp.Header)
	}
}

// BenchmarkCompress measures a 64 KiB response compressed, and the same declined
// — the client sends no Accept-Encoding — which is what serving the middleware
// to a client that cannot use it costs.
func BenchmarkCompress(b *testing.B) {
	body := bytes.Repeat([]byte("compress me please, "), 64<<10/20+1)[:64<<10]
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(body)
	})
	mw, err := server.Compress(corenet.CompressValue{})
	if err != nil {
		b.Fatal(err)
	}
	handler := mw(h)
	for _, tc := range []struct {
		name, accept string
	}{{"gzip", "gzip"}, {"declined", ""}} {
		b.Run(tc.name, func(b *testing.B) {
			ctx := context.Background()
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for b.Loop() {
				req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://x/", nil)
				req.Header.Set("Accept-Encoding", tc.accept)
				handler.ServeHTTP(discardWriter{header: http.Header{}}, req)
			}
		})
	}
}

// discardWriter is a ResponseWriter that keeps nothing.
type discardWriter struct {
	header http.Header
}

// Header implements http.ResponseWriter.
func (w discardWriter) Header() http.Header {
	return w.header
}

// Write implements http.ResponseWriter.
func (w discardWriter) Write(p []byte) (int, error) {
	return len(p), nil
}

// WriteHeader implements http.ResponseWriter.
func (w discardWriter) WriteHeader(int) {}
