// Package client_test — the policy constructors as a caller composes them.
package client_test

import (
	"net/http"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcclient "github.com/kitsunium/sdk/internal/service/net/client"
)

// TestAllowMethods pins the closed set. Passing NO method admits none, because
// an allowlist that lost its contents — a config that failed to parse, a slice
// that was never populated — must close rather than open.
func TestAllowMethods(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		allowed []string
		method  string
		wantErr bool
	}
	tests := []tc{
		{name: "the named method", allowed: []string{http.MethodGet}, method: http.MethodGet},
		{name: "a lowercase declaration", allowed: []string{"get"}, method: http.MethodGet},
		{name: "a lowercase request", allowed: []string{http.MethodGet}, method: "get"},
		{name: "one of several", allowed: []string{http.MethodGet, http.MethodHead}, method: http.MethodHead},
		{name: "an unlisted method", allowed: []string{http.MethodGet}, method: http.MethodPost, wantErr: true},
		{name: "no methods at all admits none", allowed: nil, method: http.MethodGet, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		p := svcclient.AllowMethods(c.allowed...)
		err := p.Allow(corenet.RequestValue{Method: c.method, EscapedPath: "/v1/x"})

		if c.wantErr {
			if !errs.HasCode(err, corenet.CodeRequestDenied) {
				t.Fatalf("Allow(%q) = %v, want REQUEST_DENIED", c.method, err)
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

// TestAllowPaths pins the anchoring, which is the whole reason this constructor
// exists rather than the caller compiling their own regexp.
//
// Leaving anchoring to the caller is precisely the omission that turns an
// allowlist into a sieve: "/v1/profiles" unanchored is satisfied by
// "/anything/v1/profiles/and/more", and it is invisible in review because the
// pattern reads exactly like the path it was meant to permit.
func TestAllowPaths(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		patterns []string
		path     string
		wantErr  bool
	}
	tests := []tc{
		{name: "an exact match", patterns: []string{"/v1/profiles"}, path: "/v1/profiles"},
		{name: "a segment wildcard", patterns: []string{"/v1/supi/[^/]+"}, path: "/v1/supi/001"},
		{name: "one of several patterns", patterns: []string{"/a", "/b"}, path: "/b"},
		//: the sieve the anchoring prevents.
		{name: "a prefix does not match", patterns: []string{"/v1/profiles"}, path: "/anything/v1/profiles", wantErr: true},
		{name: "a suffix does not match", patterns: []string{"/v1/profiles"}, path: "/v1/profiles/and/more", wantErr: true},
		{name: "a wildcard does not cross a separator", patterns: []string{"/v1/supi/[^/]+"}, path: "/v1/supi/a/b", wantErr: true},
		{name: "an unmatched path", patterns: []string{"/a"}, path: "/b", wantErr: true},
		//: with no pattern this refuses everything, which is the safe default.
		{name: "no patterns at all refuses", patterns: nil, path: "/a", wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		p, err := svcclient.AllowPaths(c.patterns...)
		if err != nil {
			t.Fatalf("AllowPaths = %v, want nil", err)
		}

		aerr := p.Allow(corenet.RequestValue{Method: http.MethodGet, EscapedPath: c.path})
		if c.wantErr {
			if !errs.HasCode(aerr, corenet.CodeRequestDenied) {
				t.Fatalf("Allow(%q) = %v, want REQUEST_DENIED", c.path, aerr)
			}
			//: a refusal must not echo the path, or every denied request logs
			//: a piece of the private API surface.
			for _, f := range errs.FieldsOf(aerr) {
				if f.StringValue() == c.path {
					t.Errorf("the refusal echoed the path: %v", errs.FieldsOf(aerr))
				}
			}
			return
		}
		if aerr != nil {
			t.Fatalf("Allow(%q) = %v, want nil", c.path, aerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestAllowPathsRejectsBadPatterns pins that a malformed pattern fails at
// CONSTRUCTION. Deferring it to request time would mean a client that builds
// fine, starts fine, and then refuses every call for a reason the operator only
// discovers in production.
func TestAllowPathsRejectsBadPatterns(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		pattern string
	}
	tests := []tc{
		{"an unclosed group", "/v1/("},
		{"an unclosed class", "/v1/["},
		{"an invalid repeat count", "/v1/a{2,1}"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		for _, build := range []func(...string) (corenet.Policy, error){
			svcclient.AllowPaths, svcclient.DenyPaths,
		} {
			p, err := build(c.pattern)
			if !errs.HasCode(err, corenet.CodeRequestDenied) {
				t.Fatalf("building with %q = %v, want REQUEST_DENIED", c.pattern, err)
			}
			//: a refused constructor must hand back no policy, or a caller
			//: checking only the value would install a nil one.
			if p != nil {
				t.Errorf("building with %q returned a policy beside the error", c.pattern)
			}
			//: the offending pattern IS named — it is the operator's own
			//: configuration, not a request path.
			var named bool
			for _, f := range errs.FieldsOf(err) {
				if f.Key() == "pattern" && f.StringValue() == c.pattern {
					named = true
				}
			}
			if !named {
				t.Errorf("the error does not name the pattern: %v", errs.FieldsOf(err))
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

// TestDenyPaths pins that a deny is FINAL: it is not overridable by an allow
// pattern, which is what lets a narrow exclusion be carved out of a broad allow
// rule without rewriting that rule into something unreadable.
//
// The empty case is deliberately the mirror image of AllowPaths: an empty
// denylist denies NOTHING, because the allowlist is what grants.
func TestDenyPaths(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		patterns []string
		path     string
		wantErr  bool
	}
	tests := []tc{
		{name: "no patterns deny nothing", patterns: nil, path: "/v1/x"},
		{name: "an unmatched path passes", patterns: []string{"/v1/secret"}, path: "/v1/x"},
		{name: "a matched path is denied", patterns: []string{"/v1/secret"}, path: "/v1/secret", wantErr: true},
		{name: "one of several patterns", patterns: []string{"/a", "/b"}, path: "/b", wantErr: true},
		//: anchored like the allow side, so a deny cannot be spelled around by
		//: adding a prefix.
		{name: "a prefix does not match", patterns: []string{"/v1/secret"}, path: "/x/v1/secret"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		p, err := svcclient.DenyPaths(c.patterns...)
		if err != nil {
			t.Fatalf("DenyPaths = %v, want nil", err)
		}

		aerr := p.Allow(corenet.RequestValue{Method: http.MethodGet, EscapedPath: c.path})
		if c.wantErr {
			if !errs.HasCode(aerr, corenet.CodeRequestDenied) {
				t.Fatalf("Allow(%q) = %v, want REQUEST_DENIED", c.path, aerr)
			}
			return
		}
		if aerr != nil {
			t.Fatalf("Allow(%q) = %v, want nil", c.path, aerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestPolicies pins the conjunction. Composition narrows and only narrows: every
// member must allow, so adding a policy can never widen what is permitted — the
// property that makes it safe to hand a caller a client and let them add rules.
func TestPolicies(t *testing.T) {
	t.Parallel()
	allowGet := svcclient.AllowMethods(http.MethodGet)
	allowAll := svcclient.AllowMethods(http.MethodGet, http.MethodPost)

	type tc struct {
		name    string
		members func(t *testing.T) []corenet.Policy
		method  string
		path    string
		wantErr bool
	}
	tests := []tc{
		{
			//: an empty conjunction refuses, which is the closed default.
			name:    "no members refuse",
			members: func(*testing.T) []corenet.Policy { return nil },
			method:  http.MethodGet, path: "/v1/x", wantErr: true,
		},
		{
			name:    "a single permissive member allows",
			members: func(*testing.T) []corenet.Policy { return []corenet.Policy{allowAll} },
			method:  http.MethodPost, path: "/v1/x",
		},
		{
			//: the narrower member wins, which is what "narrows only" means.
			name:    "the narrower member decides",
			members: func(*testing.T) []corenet.Policy { return []corenet.Policy{allowAll, allowGet} },
			method:  http.MethodPost, path: "/v1/x", wantErr: true,
		},
		{
			name: "a path member and a method member together",
			members: func(t *testing.T) []corenet.Policy {
				t.Helper()
				paths, err := svcclient.AllowPaths("/v1/x")
				if err != nil {
					t.Fatalf("AllowPaths = %v", err)
				}
				return []corenet.Policy{allowGet, paths}
			},
			method: http.MethodGet, path: "/v1/x",
		},
		{
			name: "a deny member overrides an allow member",
			members: func(t *testing.T) []corenet.Policy {
				t.Helper()
				paths, aerr := svcclient.AllowPaths("/v1/.*")
				if aerr != nil {
					t.Fatalf("AllowPaths = %v", aerr)
				}
				denied, derr := svcclient.DenyPaths("/v1/secret")
				if derr != nil {
					t.Fatalf("DenyPaths = %v", derr)
				}
				return []corenet.Policy{allowGet, paths, denied}
			},
			method: http.MethodGet, path: "/v1/secret", wantErr: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		p := svcclient.Policies(c.members(t)...)

		err := p.Allow(corenet.RequestValue{Method: c.method, EscapedPath: c.path})
		if c.wantErr {
			if !errs.HasCode(err, corenet.CodeRequestDenied) {
				t.Fatalf("Allow = %v, want REQUEST_DENIED", err)
			}
			return
		}
		if err != nil {
			t.Fatalf("Allow = %v, want nil", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// buildReadOnly assembles the allowlist the SDM integration actually uses.
func buildReadOnly(t *testing.T) corenet.Policy {
	t.Helper()
	allow, err := svcclient.AllowPaths(`/v1/(subscribers|supi/[^/]+|profiles(/[^/]+)?)`)
	if err != nil {
		t.Fatalf("allow: %v", err)
	}
	deny, err := svcclient.DenyPaths(`/v1/(authentication|policy|eir|oam).*`)
	if err != nil {
		t.Fatalf("deny: %v", err)
	}
	return svcclient.Policies(svcclient.AllowMethods(http.MethodGet), deny, allow)
}

// TestPolicies_ReadOnlySurface pins the authorisation surface as it is actually
// assembled, including the traps a hand-written allowlist gets wrong: an
// unanchored pattern, a dot segment in any of its spellings, and an encoded
// separator that the two ends of the connection read differently.
//
// The individual constructors are pinned above; this holds the COMPOSITION,
// which is what a deployment depends on and the only place the interaction
// between deny, allow and the path guard is visible.
func TestPolicies_ReadOnlySurface(t *testing.T) {
	t.Parallel()
	policy := buildReadOnly(t)

	type tc struct {
		// name describes the case.
		name string
		// method and path are what the caller asks for.
		method string
		path   string
		// wantCode is the refusal, or zero when the request is admitted.
		wantCode errs.Code
	}
	tests := []tc{
		{name: "a listed read is admitted", method: http.MethodGet, path: "/v1/subscribers"},
		{name: "a parameterised read is admitted", method: http.MethodGet, path: "/v1/supi/208930000100001"},
		{name: "an optional segment is admitted", method: http.MethodGet, path: "/v1/profiles"},
		{name: "a write verb is refused", method: http.MethodDelete, path: "/v1/subscribers", wantCode: corenet.CodeRequestDenied},
		{name: "an unlisted path is refused", method: http.MethodGet, path: "/v1/context-data", wantCode: corenet.CodeRequestDenied},
		{name: "a denied path is refused", method: http.MethodGet, path: "/v1/authentication/all", wantCode: corenet.CodeRequestDenied},
		{
			name: "a prefixed path cannot satisfy an anchored pattern",
			//: the constructor anchors, so this must not match /v1/subscribers.
			method: http.MethodGet, path: "/evil/v1/subscribers", wantCode: corenet.CodeRequestDenied,
		},
		{
			name: "a literal dot segment is refused before the patterns",
			//: `[^/]+` happily matches "..", so the pattern alone would admit this.
			method: http.MethodGet, path: "/v1/supi/..", wantCode: corenet.CodeUnsafePath,
		},
		{
			name: "a percent-encoded dot segment is refused too",
			//: the upstream would decode %2e%2e back into "..".
			method: http.MethodGet, path: "/v1/supi/%2e%2e", wantCode: corenet.CodeUnsafePath,
		},
		{name: "a relative path is refused", method: http.MethodGet, path: "v1/subscribers", wantCode: corenet.CodeUnsafePath},
		{
			// The wire form is one segment and satisfies `[^/]+`, but an upstream
			// that decodes before routing sees "/v1/supi/a/b" — two segments and
			// a different resource than the policy believed it authorised. The
			// two views disagree, so the only honest answer is to refuse.
			name:   "an encoded separator is refused because the two ends disagree",
			method: http.MethodGet, path: "/v1/supi/a%2fb", wantCode: corenet.CodeUnsafePath,
		},
		{
			name:   "an uppercase encoded separator is refused too",
			method: http.MethodGet, path: "/v1/supi/a%2Fb", wantCode: corenet.CodeUnsafePath,
		},
		{
			name:   "an encoded backslash is refused as a separator",
			method: http.MethodGet, path: "/v1/supi/a%5cb", wantCode: corenet.CodeUnsafePath,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := policy.Allow(corenet.RequestValue{
			Method: c.method, Scheme: "https", Host: "sdm:8000", EscapedPath: c.path,
		})

		//: an admitted request must produce no error at all.
		if c.wantCode == 0 {
			if err != nil {
				t.Fatalf("%s %s was refused: %v", c.method, c.path, err)
			}
			return
		}
		if err == nil {
			t.Fatalf("%s %s was admitted, want a refusal", c.method, c.path)
		}
		if !errs.HasCode(err, c.wantCode) {
			t.Fatalf("%s %s = %v, want code %v", c.method, c.path, err, c.wantCode)
		}
		//: a refusal never echoes the path it refused.
		if strings.Contains(err.Error(), c.path) {
			t.Errorf("the refusal echoes the path: %v", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
