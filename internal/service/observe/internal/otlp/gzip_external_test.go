package otlp_test

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/observe/internal/otlp"
)

// gzipCollector records the Content-Encoding and the decoded body of every
// request, and answers status.
func gzipCollector(t *testing.T, status int) (url string, seen chan [2]string) {
	t.Helper()
	seen = make(chan [2]string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		encoding := r.Header.Get("Content-Encoding")
		if encoding == "gzip" {
			zr, err := gzip.NewReader(bytes.NewReader(body))
			if err != nil {
				t.Errorf("not a gzip body: %v", err)
			} else {
				body, _ = io.ReadAll(zr)
			}
		}
		seen <- [2]string{encoding, string(body)}
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	return server.URL + testPath, seen
}

// TestGzipCompressesTheBodyAndSaysSo (#1051): with Gzip the collector sees
// Content-Encoding: gzip and decodes the body to the document posted; without
// it there is no such header and the body is the document.
func TestGzipCompressesTheBodyAndSaysSo(t *testing.T) {
	t.Parallel()
	doc := `{"resourceMetrics":[` + strings.Repeat(`{"k":"v"},`, 200) + `{}]}`
	for _, gz := range []bool{true, false} {
		url, seen := gzipCollector(t, http.StatusOK)
		sender, err := otlp.NewSender(&otlp.HTTPConfig{Endpoint: url, Gzip: gz}, specFor(0xF2))
		if err != nil {
			t.Fatalf("NewSender: %v", err)
		}
		for range 2 {
			if err := sender.Post([]byte(doc)); err != nil {
				t.Fatalf("Post: %v", err)
			}
			got := <-seen
			if wantEnc := map[bool]string{true: "gzip", false: ""}[gz]; got[0] != wantEnc || got[1] != doc {
				t.Fatalf("gzip=%v: encoding %q, body of %d bytes (want %q, the %d-byte document)", gz, got[0], len(got[1]), wantEnc, len(doc))
			}
		}
	}
}

// TestA415IsPermanentLikeA400 (#1051): a collector refusing the encoding is
// classified as the permanent rejection, not the retryable one.
func TestA415IsPermanentLikeA400(t *testing.T) {
	t.Parallel()
	url, _ := gzipCollector(t, http.StatusUnsupportedMediaType)
	spec := specFor(0xF3)
	sender, err := otlp.NewSender(&otlp.HTTPConfig{Endpoint: url, Gzip: true}, spec)
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	if verdict := sender.Post([]byte(`{}`)); !errors.Is(verdict, spec.Rejected) {
		t.Fatalf("a 415 = %v, want the permanent rejection", verdict)
	}
}

// TestPostEncodedPostsAGzipBodyAsItRests (#1051): a body already gzip is
// posted as it is, labelled, whatever Gzip says; "" is PostContext; any
// other encoding is refused before a request, as the permanent verdict.
func TestPostEncodedPostsAGzipBodyAsItRests(t *testing.T) {
	t.Parallel()
	doc := `{"resourceSpans":[` + strings.Repeat(`{"k":"v"},`, 50) + `{}]}`
	var zipped bytes.Buffer
	zw := gzip.NewWriter(&zipped)
	_, _ = zw.Write([]byte(doc))
	_ = zw.Close()
	for _, gz := range []bool{true, false} {
		url, seen := gzipCollector(t, http.StatusOK)
		spec := specFor(0xF4)
		sender, err := otlp.NewSender(&otlp.HTTPConfig{Endpoint: url, Gzip: gz}, spec)
		if err != nil {
			t.Fatalf("NewSender: %v", err)
		}
		if err := sender.PostEncoded(t.Context(), zipped.Bytes(), "gzip"); err != nil {
			t.Fatalf("PostEncoded(gzip): %v", err)
		}
		if got := <-seen; got[0] != "gzip" || got[1] != doc {
			t.Fatalf("Gzip=%v: a gzip body arrived labelled %q as %d bytes, want gzip decoding to the document", gz, got[0], len(got[1]))
		}
		if err := sender.PostEncoded(t.Context(), []byte(doc), ""); err != nil {
			t.Fatalf("PostEncoded(identity): %v", err)
		}
		if got, want := <-seen, map[bool]string{true: "gzip", false: ""}[gz]; got[0] != want || got[1] != doc {
			t.Fatalf("Gzip=%v: the identity encoding arrived labelled %q, want %q", gz, got[0], want)
		}
		if err := sender.PostEncoded(t.Context(), []byte(doc), "br"); !errs.HasCode(err, spec.RequestUnbuildable.Code) {
			t.Fatalf("PostEncoded(br) = %v, want the permanent refusal", err)
		}
		select {
		case got := <-seen:
			t.Fatalf("a refused encoding reached the collector: %q", got[0])
		default:
		}
	}
}
