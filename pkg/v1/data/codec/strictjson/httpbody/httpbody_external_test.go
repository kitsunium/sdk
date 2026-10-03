// Package httpbody_test walks the facade as a handler uses it: a good body
// decodes, a bad one is refused with strictjson's codes and a status, and
// nothing needs an internal import.
package httpbody_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/data/codec/strictjson"
	"github.com/kitsunium/sdk/pkg/v1/data/codec/strictjson/httpbody"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// createItem is the body the facade test decodes.
type createItem struct {
	Name  string `json:"name"`
	Price int    `json:"price"`
}

// TestABodyDecodesOrIsRefused pins the facade end to end: the refusals are
// strictjson's codes, with their statuses, and PointerOf locates them.
func TestABodyDecodesOrIsRefused(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		body        string
		contentType string
		wantCode    errs.Code
		wantStatus  int
	}
	tests := []tc{
		{name: "a JSON body", body: `{"name":"lamp","price":12}`, contentType: "application/json"},
		{name: "an empty body", contentType: "application/json", wantCode: strictjson.CodeDocumentEmpty, wantStatus: http.StatusBadRequest},
		{name: "not declared JSON", body: `{"name":"lamp"}`, contentType: "text/plain", wantCode: strictjson.CodeMediaTypeUnsupported, wantStatus: http.StatusUnsupportedMediaType},
		{name: "an unknown member", body: `{"name":"lamp","role":"admin"}`, contentType: "application/json", wantCode: strictjson.CodeMemberUnknown, wantStatus: http.StatusBadRequest},
		{name: "past the bound", body: `{"name":"` + strings.Repeat("x", 2048) + `"}`, contentType: "application/json", wantCode: strictjson.CodeDocumentTooLarge, wantStatus: http.StatusRequestEntityTooLarge},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/items", strings.NewReader(c.body))
		req.Header.Set("Content-Type", c.contentType)
		var in createItem
		err := httpbody.DecodeRequest(httptest.NewRecorder(), req, &in, 1<<10)
		//: a good body decodes.
		if c.wantCode == 0 {
			if err != nil || in.Name != "lamp" {
				t.Fatalf("DecodeRequest() = %v, %+v; want the body", err, in)
			}
			return
		}
		//: a refused one carries strictjson's code and its status.
		if !errs.HasCode(err, c.wantCode) || errs.HTTPStatusOf(err) != c.wantStatus {
			t.Fatalf("DecodeRequest() = %v (status %d), want code %v and %d", err, errs.HTTPStatusOf(err), c.wantCode, c.wantStatus)
		}
		//: and never repeats a value of the body.
		if strings.Contains(errs.PublicOf(err), "admin") {
			t.Errorf("the refusal repeated a value: %q", errs.PublicOf(err))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestPointerOfLocatesARefusedBody pins that strictjson's PointerOf reads a
// refusal this package returned.
func TestPointerOfLocatesARefusedBody(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/items", strings.NewReader(`{"name":"lamp","role":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	var in createItem
	err := httpbody.DecodeRequest(nil, req, &in, 1<<10)
	//: the member the target does not declare.
	if pointer, located := strictjson.PointerOf(err); !located || pointer != "/role" {
		t.Errorf("PointerOf() = %q, %v; want /role", pointer, located)
	}
}
