// Package client — the policy constructors.
package client

import (
	"regexp"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_compileAnchored pins the anchoring, which is the whole reason this helper
// exists rather than each constructor calling regexp.Compile.
//
// Leaving anchoring to the caller is the omission that turns an allowlist into a
// sieve: `/v1/profiles` would otherwise be satisfied by
// `/anything/v1/profiles/and/more`, and the mistake is invisible in review
// because the pattern reads exactly like what was intended.
func Test_compileAnchored(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// patterns are the unanchored patterns handed in.
		patterns []string
		// matches must be accepted by the compiled pattern.
		matches []string
		// rejects must not be.
		rejects []string
		// wantErr is whether compilation must fail.
		wantErr bool
	}
	tests := []tc{
		{
			name:     "a literal path",
			patterns: []string{"/v1/profiles"},
			matches:  []string{"/v1/profiles"},
			//: the anchors are the point: neither a prefix nor a suffix may satisfy it.
			rejects: []string{"/anything/v1/profiles", "/v1/profiles/and/more", "/v1/profile"},
		},
		{
			name:     "an alternation",
			patterns: []string{"/v1/(subscribers|profiles)"},
			matches:  []string{"/v1/subscribers", "/v1/profiles"},
			//: the wrapping group is what keeps the anchors around BOTH branches;
			//: "^/v1/(a|b)$" without it would anchor only the first.
			rejects: []string{"/v1/sessions", "/evil/v1/profiles", "/v1/profilesX"},
		},
		{
			name:     "a parameterised segment",
			patterns: []string{"/v1/supi/[^/]+"},
			matches:  []string{"/v1/supi/208930000100001"},
			rejects:  []string{"/v1/supi/", "/v1/supi/a/b"},
		},
		{
			//: a pattern the caller already anchored still works.
			name:     "an already anchored pattern",
			patterns: []string{"^/v1/x$"},
			matches:  []string{"/v1/x"},
			rejects:  []string{"/v1/xy"},
		},
		{name: "several patterns", patterns: []string{"/a", "/b"}, matches: []string{"/a"}},
		{name: "no pattern at all"},
		{name: "an unterminated class", patterns: []string{"/v1/[unterminated"}, wantErr: true},
		{name: "an invalid repetition", patterns: []string{"/v1/*+"}, wantErr: true},
		//: one bad pattern among good ones still fails the whole construction.
		{name: "a bad pattern after a good one", patterns: []string{"/a", "("}, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := compileAnchored(c.patterns)

		if c.wantErr {
			if !errs.HasCode(err, corenet.CodeRequestDenied) {
				t.Fatalf("compileAnchored(%v) = %v, want REQUEST_DENIED", c.patterns, err)
			}
			//: nothing is handed back, so no caller can build a policy around a
			//: half-compiled list.
			if got != nil {
				t.Errorf("compileAnchored returned %d patterns beside the error", len(got))
			}
			return
		}
		if err != nil {
			t.Fatalf("compileAnchored(%v) = %v, want nil", c.patterns, err)
		}
		if len(got) != len(c.patterns) {
			t.Fatalf("compiled %d patterns, want %d", len(got), len(c.patterns))
		}
		for _, path := range c.matches {
			if !anyMatch(got, path) {
				t.Errorf("%q was not matched by %v", path, c.patterns)
			}
		}
		for _, path := range c.rejects {
			if anyMatch(got, path) {
				t.Errorf("%q was matched by %v — the pattern is not anchored", path, c.patterns)
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

// anyMatch reports whether any compiled pattern covers the path.
func anyMatch(patterns []*regexp.Regexp, path string) bool {
	for _, re := range patterns {
		if re.MatchString(path) {
			return true
		}
	}
	return false
}
