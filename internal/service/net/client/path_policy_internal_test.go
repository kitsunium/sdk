// Package client — the path allowlist.
package client

import (
	"net/http"
	"regexp"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_pathPolicy_Allow pins the allowlist's direction and the guard that runs
// ahead of it.
//
// An empty pattern set admits NOTHING — an allowlist that grew empty by
// accident must close, never open — and the safety check runs first, because a
// pattern is matched against the wire form and an unnormalised path means one
// thing to the pattern and another to the upstream.
func Test_pathPolicy_Allow(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		patterns []string
		path     string
		//: the code the refusal must carry, or zero to allow.
		wantCode errs.Code
	}
	tests := []tc{
		{name: "an exact match", patterns: []string{"^/v1/x$"}, path: "/v1/x"},
		{name: "a segment wildcard", patterns: []string{"^/v1/supi/[^/]+$"}, path: "/v1/supi/001"},
		{name: "one of several patterns", patterns: []string{"^/a$", "^/b$"}, path: "/b"},
		{
			name:     "an unmatched path",
			patterns: []string{"^/v1/x$"},
			path:     "/v1/y",
			wantCode: corenet.CodeRequestDenied,
		},
		{
			//: the closed default: no patterns means nothing is permitted.
			name:     "no patterns admit nothing",
			path:     "/v1/x",
			wantCode: corenet.CodeRequestDenied,
		},
		{
			//: `[^/]+` matches ".." perfectly well, which is why the guard must
			//: run before the pattern rather than being left to it.
			name:     "a dot segment is refused before the pattern",
			patterns: []string{"^/v1/supi/[^/]+$"},
			path:     "/v1/supi/..",
			wantCode: corenet.CodeUnsafePath,
		},
		{
			//: `a%2fb` satisfies `[^/]+` as one segment while the upstream sees
			//: two — the two views disagree, so it is refused.
			name:     "an encoded separator is refused before the pattern",
			patterns: []string{"^/v1/supi/[^/]+$"},
			path:     "/v1/supi/a%2fb",
			wantCode: corenet.CodeUnsafePath,
		},
		{
			name:     "a relative path is refused",
			patterns: []string{"^v1/x$"},
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
		p := &pathPolicy{patterns: compiled}

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
		//: a refusal must not echo the path: every denied request would
		//: otherwise log a piece of the private API surface.
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
