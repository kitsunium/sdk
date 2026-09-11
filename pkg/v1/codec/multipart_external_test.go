// Package codec_test — black-box proof that a consumer can build, send and
// read back a multipart/form-data upload through the facade alone. This file
// imports nothing under internal/, because a consumer's code cannot.
package codec_test

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"testing"

	codec "github.com/kitsunium/sdk/pkg/v1/codec"
	errs "github.com/kitsunium/sdk/pkg/v1/errs"
)

// facadeUpload is a two-part upload as a consumer would build it: a plain
// field and a file.
func facadeUpload() codec.MultipartForm {
	return codec.MultipartForm{Parts: []codec.MultipartPart{
		{Name: "title", Data: []byte("Q3 report")},
		{Name: "file", FileName: "report.pdf", ContentType: "application/pdf", Data: []byte("%PDF-1.7 body")},
	}}
}

// TestMultipartUploadThroughTheFacade is the regression for a facade that
// documented multipart.FormValue and multipart.ContentType while both lived
// only under internal/: the advertised uploads were unreachable, except as the
// JSON-mediated _json part. It builds a file part with the facade's own types,
// takes the Content-Type from the facade, and hands both to the STDLIB reader —
// what any receiving server would do — then decodes the body back through the
// facade as well. The streaming case reads the delimiter off the Encoder
// before a byte is written, which is when an HTTP client must set its header.
//
// SEEN FAILING before the facade carried these names — the build stops where a
// consumer's would: "undefined: codec.MultipartForm", "undefined:
// codec.MultipartPart", "undefined: codec.MultipartContentType". And with
// MultipartContentType answering for a different body than the one passed:
//
//	Marshal, then MultipartContentType: part 0: the stdlib reader refused the body: multipart: NextPart: EOF
func TestMultipartUploadThroughTheFacade(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		encode func(t *testing.T) (body []byte, contentType string)
	}
	tests := []tc{
		{"Marshal, then MultipartContentType", func(t *testing.T) ([]byte, string) {
			t.Helper()
			body, err := codec.Marshal(codec.Multipart, facadeUpload())
			if err != nil {
				t.Fatalf("Marshal err=%v", err)
			}
			contentType, cerr := codec.MultipartContentType(body)
			if cerr != nil {
				t.Fatalf("MultipartContentType err=%v", cerr)
			}
			return body, contentType
		}},
		{"streamed, header set before the first byte", func(t *testing.T) ([]byte, string) {
			t.Helper()
			var wire bytes.Buffer
			enc, err := codec.NewEncoder(codec.Multipart, &wire)
			if err != nil {
				t.Fatalf("NewEncoder err=%v", err)
			}
			provider, ok := enc.(interface{ Boundary() string })
			if !ok || wire.Len() != 0 {
				t.Fatalf("the encoder does not expose its boundary before writing (ok=%v, %d bytes out)", ok, wire.Len())
			}
			contentType := mime.FormatMediaType("multipart/form-data", map[string]string{"boundary": provider.Boundary()})
			for _, part := range facadeUpload().Parts {
				if eerr := enc.Encode(part); eerr != nil {
					t.Fatalf("Encode err=%v", eerr)
				}
			}
			if cerr := enc.Close(); cerr != nil {
				t.Fatalf("Close err=%v", cerr)
			}
			return wire.Bytes(), contentType
		}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		body, contentType := tc.encode(t)
		mediaType, params, perr := mime.ParseMediaType(contentType)
		if perr != nil || mediaType != "multipart/form-data" {
			t.Fatalf("%s: Content-Type %q is not form-data (%v)", tc.name, contentType, perr)
		}
		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for i, want := range facadeUpload().Parts {
			part, nerr := reader.NextPart()
			if nerr != nil {
				t.Fatalf("%s: part %d: the stdlib reader refused the body: %v\n%s", tc.name, i, nerr, body)
			}
			data, rerr := io.ReadAll(part)
			if rerr != nil {
				t.Fatalf("%s: part %d: reading the body err=%v", tc.name, i, rerr)
			}
			if part.FormName() != want.Name || part.FileName() != want.FileName ||
				part.Header.Get("Content-Type") != want.ContentType || !bytes.Equal(data, want.Data) {
				t.Errorf("%s: part %d read back as (%q, %q, %q, %q), want (%q, %q, %q, %q)", tc.name, i,
					part.FormName(), part.FileName(), part.Header.Get("Content-Type"), data,
					want.Name, want.FileName, want.ContentType, want.Data)
			}
		}
		if _, nerr := reader.NextPart(); !errors.Is(nerr, io.EOF) {
			t.Errorf("%s: the body does not close after the upload: %v", tc.name, nerr)
		}
		var back codec.MultipartForm
		if uerr := codec.Unmarshal(codec.Multipart, body, &back); uerr != nil {
			t.Fatalf("%s: Unmarshal err=%v", tc.name, uerr)
		}
		if back.Boundary != params["boundary"] || len(back.Parts) != len(facadeUpload().Parts) {
			t.Errorf("%s: decoded boundary %q and %d parts, want %q and %d",
				tc.name, back.Boundary, len(back.Parts), params["boundary"], len(facadeUpload().Parts))
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestMultipartRefusalsThroughTheFacade pins the two refusals the facade's
// documentation promises a consumer, matched the way a consumer matches them —
// through pkg/v1/errs: a header value carrying a line break is refused rather
// than escaped, and a body with no delimiter has no Content-Type to recover.
//
// SEEN FAILING, one case per mutation. With the service encoder's CR/LF/NUL
// refusal made inert:
//
//	a CRLF in a filename is refused, not escaped: expected VALUE_INVALID, got <nil>
//
// and with MultipartContentType answering for a different body:
//
//	a body with no delimiter has no Content-Type: expected BOUNDARY_INVALID, got <nil>
func TestMultipartRefusalsThroughTheFacade(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		run        func() (out string, err error)
		wantReason string
	}
	tests := []tc{
		{"a CRLF in a filename is refused, not escaped", func() (string, error) {
			part := codec.MultipartPart{Name: "file", FileName: "a\r\nX-Injected: 1", Data: []byte("x")}
			body, err := codec.Marshal(codec.Multipart, part)
			return string(body), err
		}, "VALUE_INVALID"},
		{"a body with no delimiter has no Content-Type", func() (string, error) {
			return codec.MultipartContentType([]byte("not a multipart body"))
		}, "BOUNDARY_INVALID"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		out, err := tc.run()
		if !errs.HasReason(err, tc.wantReason) {
			t.Errorf("%s: expected %s, got %v", tc.name, tc.wantReason, err)
		}
		if out != "" {
			t.Errorf("%s: %q returned alongside the refusal", tc.name, out)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
