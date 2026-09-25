package strictjson_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/codec/strictjson"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// createItem is the body the facade test decodes.
type createItem struct {
	Name  string `json:"name"`
	Price int    `json:"price"`
}

// TestTheFacadeDecodesAndRefuses walks the public surface as a handler uses
// it: a good body decodes, a bad one is refused with a status and a location,
// and nothing needs an internal import.
func TestTheFacadeDecodesAndRefuses(t *testing.T) {
	t.Parallel()
	good := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/items", strings.NewReader(`{"name":"lamp","price":12}`))
	good.Header.Set("Content-Type", "application/json")
	var in createItem
	if err := strictjson.DecodeRequest(httptest.NewRecorder(), good, &in, 1<<10); err != nil || in.Name != "lamp" {
		t.Fatalf("DecodeRequest() = %v, %+v; want the body", err, in)
	}
	err := strictjson.Decode(strings.NewReader(`{"name":"lamp","role":"admin"}`), &in, 1<<10)
	if !errs.HasCode(err, strictjson.CodeMemberUnknown) || errs.HTTPStatusOf(err) != http.StatusBadRequest {
		t.Fatalf("Decode() = %v (status %d), want MEMBER_UNKNOWN and 400", err, errs.HTTPStatusOf(err))
	}
	if pointer, located := strictjson.PointerOf(err); !located || pointer != "/role" {
		t.Errorf("PointerOf() = %q, %v; want /role", pointer, located)
	}
	if strings.Contains(errs.PublicOf(err), "admin") {
		t.Errorf("the refusal repeated a value: %q", errs.PublicOf(err))
	}
}
