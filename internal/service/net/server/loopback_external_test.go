package server_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kitsunium/sdk/internal/service/net/server"
)

func TestLoopbackHost(t *testing.T) {
	reached := 0
	h := server.LoopbackHost()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached++ }))
	for host, code := range map[string]int{
		"127.0.0.1:7311":  200,
		"127.0.0.1":       200,
		"127.8.9.10:80":   200,
		"localhost:7311":  200,
		"LocalHost":       200,
		"[::1]:7311":      200,
		"[::1]":           200,
		"evil.example":    403,
		"evil.example:80": 403,
		"192.0.2.1:7311":  403,
		"localhost.evil":  403,
		"":                403,
	} {
		before := reached
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != code {
			t.Errorf("Host %q answered %d, want %d", host, w.Code, code)
		}
		if served := reached > before; served != (code == 200) {
			t.Errorf("Host %q: handler reached = %v", host, served)
		}
	}
}
