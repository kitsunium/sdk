package app

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// <PRODUCT>_ACCESS_LOG reads four modes and nothing else; the default is
// every request outside dev and the errors in dev.
func TestParseAccessLog(t *testing.T) {
	for _, c := range []struct {
		in   string
		want accessPolicy
		ok   bool
	}{
		{"all", accessPolicy{mode: accessAll}, true},
		{" ERRORS ", accessPolicy{mode: accessErrors}, true},
		{"off", accessPolicy{mode: accessOff}, true},
		{"slow:100ms", accessPolicy{mode: accessSlow, slow: 100 * time.Millisecond}, true},
		{"slow:1.5s", accessPolicy{mode: accessSlow, slow: 1500 * time.Millisecond}, true},
		{"slow:", accessPolicy{}, false},
		{"slow:0s", accessPolicy{}, false},
		{"slow:-1s", accessPolicy{}, false},
		{"slow:100", accessPolicy{}, false},
		{"verbose", accessPolicy{}, false},
	} {
		got, ok := parseAccessLog(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("parseAccessLog(%q) = %v, %v, want %v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
	own := func(string) string { return "" }
	if p, err := resolveAccessLog("shop", own, false); err != nil || p.mode != accessAll {
		t.Errorf("outside dev the default is %v, %v, want all", p, err)
	}
	if p, err := resolveAccessLog("shop", own, true); err != nil || p.mode != accessErrors {
		t.Errorf("in dev the default is %v, %v, want errors", p, err)
	}
}

// A policy decides on the status and the time, and spells itself as it was
// written, for the settings' listing.
func TestAccessPolicyDecides(t *testing.T) {
	slow := accessPolicy{mode: accessSlow, slow: 100 * time.Millisecond}
	errorsOnly := accessPolicy{mode: accessErrors}
	for _, c := range []struct {
		p      accessPolicy
		status int
		took   time.Duration
		want   bool
	}{
		{accessPolicy{}, 200, 0, true},
		{errorsOnly, 399, 0, false},
		{errorsOnly, 404, 0, true},
		{errorsOnly, 503, 0, true},
		{slow, 500, 100 * time.Millisecond, false},
		{slow, 200, 101 * time.Millisecond, true},
		{accessPolicy{mode: accessOff}, 500, time.Hour, false},
	} {
		if got := c.p.logs(c.status, c.took); got != c.want {
			t.Errorf("%v.logs(%d, %v) = %v, want %v", c.p, c.status, c.took, got, c.want)
		}
	}
	if got := slow.String(); got != "slow:100ms" {
		t.Errorf("slow spells itself %q, want slow:100ms", got)
	}
}

// The identifier a caller sends is taken only when it is short and made of
// the characters of an identifier: the header is the caller's, and it is
// written to every line that names the request.
func TestRequestIDOf(t *testing.T) {
	for _, c := range []struct {
		in, want string
	}{
		{"abc", "abc"},
		{"7f3c-A.b_9", "7f3c-A.b_9"},
		{strings.Repeat("x", 128), strings.Repeat("x", 128)},
		{strings.Repeat("x", 129), ""},
		{"", ""},
		{"has space", ""},
		{"line\nbreak", ""},
		{"quote\"", ""},
		{"café", ""},
	} {
		h := http.Header{}
		if c.in != "" {
			h["X-Request-Id"] = []string{c.in}
		}
		got, ok := requestIDOf(h)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("requestIDOf(%q) = %q, %v, want %q", c.in, got, ok, c.want)
		}
	}
}

// The route is the router's pattern without its method.
func TestRouteOf(t *testing.T) {
	for in, want := range map[string]string{"GET /items/{id}": "/items/{id}", "/assets/": "/assets/", "": "", "POST /a/b": "/a/b"} {
		if got := routeOf(in); got != want {
			t.Errorf("routeOf(%q) = %q, want %q", in, got, want)
		}
	}
}
