package server_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// fakeLimiter records the keys it is asked about and refuses after n.
type fakeLimiter struct {
	allow int
	keys  []string
}

func (f *fakeLimiter) Allow(key []byte, _ int64) (bool, int) {
	f.keys = append(f.keys, string(key))
	if len(f.keys) > f.allow {
		return false, 7
	}
	return true, 0
}

func TestRateLimit_netHTTP(t *testing.T) {
	l := &fakeLimiter{allow: 1}
	mw, err := server.RateLimit(l, server.RateKeyRemoteAddr())
	if err != nil {
		t.Fatal(err)
	}
	reached := 0
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached++ }))
	do := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "203.0.113.7:51234"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := do(); reached != 1 || w.Code != 200 {
		t.Fatalf("first: %d %d", reached, w.Code)
	}
	w := do()
	if reached != 1 || w.Code != 429 || w.Header().Get("Retry-After") != "7" || w.Header().Get("Connection") != "close" {
		t.Fatalf("second: reached %d, %d, %v", reached, w.Code, w.Header())
	}
	if l.keys[0] != "203.0.113.7" {
		t.Fatalf("keyed on %q: the port would give a client a bucket per connection", l.keys[0])
	}
}

func TestRateLimit_header(t *testing.T) {
	l := &fakeLimiter{allow: 5}
	mw, err := server.RateLimit(l, server.RateKeyHeader("x-api-key"))
	if err != nil {
		t.Fatal(err)
	}
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Api-Key", "k1")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if l.keys[0] != "k1" {
		t.Fatalf("%q", l.keys[0])
	}
}

func TestRateLimit_refusals(t *testing.T) {
	if _, err := server.RateLimit(nil, server.RateKeyRemoteAddr()); !errs.HasCode(err, corenet.CodeHTTP1Misconfigured) {
		t.Errorf("nil limiter: %v", err)
	}
	if _, err := server.RateLimit(&fakeLimiter{}, server.HTTPRateKeyValue{}); !errs.HasCode(err, corenet.CodeHTTP1Misconfigured) {
		t.Errorf("zero key: %v", err)
	}
}
