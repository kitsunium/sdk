// Package client — the method allowlist.
package client

import (
	"net/http"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_methodPolicy_Allow pins the allowlist's direction: anything not named is
// refused, INCLUDING when nothing was named at all.
//
// An empty set admitting everything would be the classic allowlist inversion —
// a caller who built the set from a config that failed to load would get an
// unrestricted client while believing they had a read-only one.
func Test_methodPolicy_Allow(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		allowed []string
		method  string
		wantErr bool
	}
	tests := []tc{
		{name: "an allowed method", allowed: []string{http.MethodGet}, method: http.MethodGet},
		{name: "one of several", allowed: []string{http.MethodGet, http.MethodHead}, method: http.MethodHead},
		//: the comparison is case-insensitive, because a caller writing "get"
		//: means the method, not a different one.
		{name: "a lowercase request method", allowed: []string{http.MethodGet}, method: "get"},
		{name: "a mixed-case request method", allowed: []string{http.MethodGet}, method: "GeT"},
		{name: "an unlisted method", allowed: []string{http.MethodGet}, method: http.MethodDelete, wantErr: true},
		//: the inversion this guards against.
		{name: "an empty set admits nothing", allowed: nil, method: http.MethodGet, wantErr: true},
		{name: "an empty method", allowed: []string{http.MethodGet}, method: "", wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		set := make(map[string]struct{}, len(c.allowed))
		for _, m := range c.allowed {
			set[m] = struct{}{}
		}
		p := &methodPolicy{allowed: set}

		err := p.Allow(corenet.RequestValue{Method: c.method, EscapedPath: "/v1/x"})

		if c.wantErr {
			if !errs.HasCode(err, corenet.CodeRequestDenied) {
				t.Fatalf("Allow(%q) = %v, want REQUEST_DENIED", c.method, err)
			}
			//: the METHOD is named because it is not sensitive; the path is
			//: deliberately not, since it would leak the private API surface.
			var named bool
			for _, f := range errs.FieldsOf(err) {
				if f.Key() == "method" && f.StringValue() == c.method {
					named = true
				}
				if f.StringValue() == "/v1/x" {
					t.Errorf("the refusal echoed the path: %v", errs.FieldsOf(err))
				}
			}
			if !named {
				t.Errorf("the refusal does not name the method: %v", errs.FieldsOf(err))
			}
			return
		}
		if err != nil {
			t.Fatalf("Allow(%q) = %v, want nil", c.method, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
