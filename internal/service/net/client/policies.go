// Package client — the policy constructors.
package client

import (
	"regexp"
	"strings"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// AllowMethods admits only the named HTTP methods. Passing none admits none:
// an allowlist that lost its contents must close, not open.
func AllowMethods(methods ...string) corenet.Policy {
	set := make(map[string]struct{}, len(methods))
	//: normalise to uppercase so the caller's spelling does not matter.
	for _, m := range methods {
		set[strings.ToUpper(m)] = struct{}{}
	}
	//: the concrete type stays unexported behind the port.
	return &methodPolicy{allowed: set}
}

// AllowPaths admits only paths matching one of the patterns.
//
// The patterns are supplied UNANCHORED and this constructor anchors them.
// Leaving anchoring to the caller is precisely the omission that turns an
// allowlist into a sieve — `/v1/profiles` would otherwise be satisfied by
// `/anything/v1/profiles/and/more` — and it is invisible in review.
func AllowPaths(patterns ...string) (policy corenet.Policy, err error) {
	compiled, cerr := compileAnchored(patterns)
	//: a malformed pattern must fail at construction, never at request time.
	if cerr != nil {
		//: surface the compilation failure unchanged.
		return nil, cerr
	}
	//: with no pattern this refuses everything, which is the safe default.
	return &pathPolicy{patterns: compiled}, nil
}

// DenyPaths refuses paths matching one of the patterns. A deny is final and is
// not overridable by an allow pattern, so a narrow exclusion can be carved out
// of a broad allow rule without rewriting it into something unreadable.
func DenyPaths(patterns ...string) (policy corenet.Policy, err error) {
	compiled, cerr := compileAnchored(patterns)
	//: a malformed pattern must fail at construction, never at request time.
	if cerr != nil {
		//: surface the compilation failure unchanged.
		return nil, cerr
	}
	//: with no pattern this denies nothing, which is the safe default for a
	//: denylist — the allowlist is what grants.
	return &denyPolicy{patterns: compiled}, nil
}

// Policies requires every policy to allow the request. Composition is by
// conjunction so that adding a policy can only ever narrow what is permitted.
func Policies(members ...corenet.Policy) corenet.Policy {
	//: an empty conjunction refuses; see conjunction.Allow.
	return &conjunction{members: members}
}

// compileAnchored wraps each pattern in its anchors and compiles it.
func compileAnchored(patterns []string) (compiled []*regexp.Regexp, err error) {
	out := make([]*regexp.Regexp, 0, len(patterns))
	//: anchor every pattern here so no caller can forget to.
	for _, raw := range patterns {
		re, cerr := regexp.Compile("^(?:" + raw + ")$")
		//: an uncompilable pattern is a configuration error.
		if cerr != nil {
			//: refuse at construction so no request ever meets a broken pattern.
			return nil, errs.Wrap(corenet.RequestDenied, errs.WrapParams{},
				errs.String("why", "the path pattern could not be compiled"),
				errs.String("pattern", raw))
		}
		out = append(out, re)
	}
	//: every pattern compiled and is anchored.
	return out, nil
}
