package server_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

func TestCORS_netHTTP(t *testing.T) {
	mw, err := server.CORS(corenet.CORSValue{Origins: []string{"https://a.example"}, Methods: []string{"GET", "POST"}, MaxAge: corenet.DurationValue(60e9)})
	if err != nil {
		t.Fatal(err)
	}
	reached := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached = true }))
	do := func(method, origin, want string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/x", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if want != "" {
			r.Header.Set("Access-Control-Request-Method", want)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := do("OPTIONS", "https://a.example", "POST"); reached || w.Code != 204 ||
		w.Header().Get("Access-Control-Allow-Origin") != "https://a.example" ||
		w.Header().Get("Access-Control-Max-Age") != "60" ||
		w.Header().Get("Vary") != "Origin" {
		t.Fatalf("preflight: reached %v, %d, %v", reached, w.Code, w.Header())
	}
	if w := do("OPTIONS", "https://evil.example", "POST"); w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("unlisted preflight: %d, %v", w.Code, w.Header())
	}
	reached = false
	if w := do("GET", "https://a.example", ""); !reached || w.Code != 200 || w.Header().Get("Access-Control-Allow-Origin") != "https://a.example" {
		t.Fatalf("actual: reached %v, %d, %v", reached, w.Code, w.Header())
	}
	if _, err := server.CORS(corenet.CORSValue{Origins: []string{"*"}, Credentials: true}); !errors.Is(err, corenet.CORSMisconfigured) {
		t.Fatalf("wildcard with credentials: %v", err)
	}
}
