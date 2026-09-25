package strictjson_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/strictjson"
)

// TestDecodeRequest pins the three things a request body adds to a document:
// an empty body is DOCUMENT_EMPTY whatever it claims to be, a non-empty one
// must declare JSON, and everything else is Decode's verdict.
func TestDecodeRequest(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		body        string
		contentType string
		wantCode    errs.Code
	}
	tests := []tc{
		{name: "application/json", body: `{"name":"lamp","price":12}`, contentType: "application/json"},
		{name: "with a charset", body: `{"name":"lamp","price":12}`, contentType: "application/json; charset=utf-8"},
		{name: "a vendor +json type", body: `{"name":"lamp","price":12}`, contentType: "application/vnd.shop+json"},
		{name: "upper-case", body: `{"name":"lamp","price":12}`, contentType: "Application/JSON"},
		{name: "text/plain", body: `{"name":"lamp","price":12}`, contentType: "text/plain", wantCode: strictjson.CodeMediaTypeUnsupported},
		{name: "a form", body: `name=lamp`, contentType: "application/x-www-form-urlencoded", wantCode: strictjson.CodeMediaTypeUnsupported},
		{name: "no content type", body: `{"name":"lamp","price":12}`, wantCode: strictjson.CodeMediaTypeUnsupported},
		{name: "an unparsable content type", body: `{}`, contentType: "application/json;;", wantCode: strictjson.CodeMediaTypeUnsupported},
		{name: "an empty body with no content type", wantCode: strictjson.CodeDocumentEmpty},
		{name: "an empty body claiming text", contentType: "text/plain", wantCode: strictjson.CodeDocumentEmpty},
		{name: "an unknown member", body: `{"name":"lamp","admin":true}`, contentType: "application/json", wantCode: strictjson.CodeMemberUnknown},
		{name: "past the bound", body: `{"name":"` + strings.Repeat("x", 200) + `"}`, contentType: "application/json", wantCode: strictjson.CodeDocumentTooLarge},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/items", strings.NewReader(c.body))
		if c.contentType != "" {
			req.Header.Set("Content-Type", c.contentType)
		}
		var got item
		err := strictjson.DecodeRequest(httptest.NewRecorder(), req, &got, 64)
		if c.wantCode == 0 {
			if err != nil || got.Name != "lamp" {
				t.Fatalf("DecodeRequest() = %v, %+v; want the body decoded", err, got)
			}
			return
		}
		if !errs.HasCode(err, c.wantCode) {
			t.Fatalf("DecodeRequest() = %v, want code %v", err, c.wantCode)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestAnOversizedBodyClosesTheConnection pins why the bound goes through
// http.MaxBytesReader rather than only through Decode's own: a client past
// the bound gets a 413 AND a closed connection, so net/http does not keep
// reading what it keeps sending in order to reuse the connection.
func TestAnOversizedBodyClosesTheConnection(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got item
		err := strictjson.DecodeRequest(w, r, &got, 64)
		w.WriteHeader(errs.HTTPStatusOf(err))
	}))
	defer server.Close()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL,
		strings.NewReader(`{"name":"`+strings.Repeat("x", 100_000)+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status %d, want 413", resp.StatusCode)
	}
	if !resp.Close {
		t.Error("the server kept the connection open after a body past the bound")
	}
}

// TestDecodeRequestRefusesANilRequest pins the argument check on the one
// argument Decode does not have.
func TestDecodeRequestRefusesANilRequest(t *testing.T) {
	t.Parallel()
	var got item
	if err := strictjson.DecodeRequest(nil, nil, &got, 64); !errs.HasCode(err, strictjson.CodeDecodeMisconfigured) {
		t.Fatalf("DecodeRequest(nil request) = %v, want DECODE_MISCONFIGURED", err)
	}
}
