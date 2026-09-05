// Package client — the path denylist.
package client

import (
	"net/http"
	"regexp"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_denyPolicy_Allow pins the denylist's direction and its path guard.
//
// The guard runs FIRST, before any pattern, because a dot segment would let a
// denied path be spelled around: "/v1/public/../secret" does not match a pattern
// written for "/v1/secret", and the upstream normalises it to exactly that.
func Test_denyPolicy_Allow(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		patterns []string
		path     string
		//: the code the refusal must carry, or zero to allow.
		wantCode errs.Code
	}
	tests := []tc{
		{name: "no patterns deny nothing", path: "/v1/x"},
		{name: "an unmatched path passes", patterns: []string{"^/v1/secret$"}, path: "/v1/x"},
		{
			name:     "a matched path is denied",
			patterns: []string{"^/v1/secret$"},
			path:     "/v1/secret",
			wantCode: corenet.CodeRequestDenied,
		},
		{
			name:     "one of several patterns",
			patterns: []string{"^/a$", "^/b$"},
			path:     "/b",
			wantCode: corenet.CodeRequestDenied,
		},
		{
			//: the guard fires before any pattern is tried.
			name:     "a dot segment is refused before the patterns",
			patterns: []string{"^/v1/secret$"},
			path:     "/v1/public/../secret",
			wantCode: corenet.CodeUnsafePath,
		},
		{
			name:     "an encoded separator is refused too",
			patterns: []string{"^/v1/secret$"},
			path:     "/v1/a%2fb",
			wantCode: corenet.CodeUnsafePath,
		},
		{
			name:     "a relative path is refused",
			patterns: nil,
			path:     "v1/x",
			wantCode: corenet.CodeUnsafePath,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		compiled := make([]*regexp.Regexp, 0, len(c.patterns))
		for _, raw := range c.patterns {
			compiled = append(compiled, regexp.MustCompile(raw))
		}
		p := &denyPolicy{patterns: compiled}

		err := p.Allow(corenet.RequestValue{Method: http.MethodGet, EscapedPath: c.path})

		if c.wantCode == 0 {
			if err != nil {
				t.Fatalf("Allow(%q) = %v, want nil", c.path, err)
			}
			return
		}
		if !errs.HasCode(err, c.wantCode) {
			t.Fatalf("Allow(%q) = %v, want code %v", c.path, err, c.wantCode)
		}
		//: neither refusal echoes the path.
		for _, f := range errs.FieldsOf(err) {
			if f.StringValue() == c.path {
				t.Errorf("the refusal echoed the path: %v", errs.FieldsOf(err))
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
