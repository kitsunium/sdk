// Package client — path safety checks applied before any allowlist pattern.
package client

import (
	"strings"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// checkPath refuses a path that is relative or carries a dot segment.
//
// This runs BEFORE any allowlist pattern because an anchored pattern is not
// sufficient on its own: `^/v1/supi/[^/]+$` matches `/v1/supi/..` perfectly
// well, and the upstream then normalises that to a different resource. Go
// reduces dot segments neither in url.URL nor in the transport, so nothing else
// in the stack will catch it. No caller thinks to check, which is exactly why
// it belongs here rather than in each consumer's policy.
func checkPath(escapedPath string) error {
	//: a relative path would be resolved against whatever base the transport
	//: happens to hold, escaping the reviewed surface entirely.
	if !strings.HasPrefix(escapedPath, "/") {
		//: refuse before the request can be built.
		return errs.Wrap(corenet.UnsafePath, errs.WrapParams{},
			errs.String("why", "path is not absolute"))
	}
	//: dot segments are refused ahead of the patterns, not by them.
	if hasDotSegment(escapedPath) {
		//: refuse without echoing the path.
		return errs.Wrap(corenet.UnsafePath, errs.WrapParams{},
			errs.String("why", "path carries a dot segment"))
	}
	//: the path is absolute and normalised.
	return nil
}

// hasDotSegment reports a "." or ".." segment in literal or percent-encoded
// form. Both forms are reinterpreted upstream, so neither may reach a pattern.
func hasDotSegment(escapedPath string) bool {
	//: inspect each segment of the escaped path in turn.
	for seg := range strings.SplitSeq(strings.TrimPrefix(escapedPath, "/"), "/") {
		//: compare lowercased — %2E and %2e denote the same octet.
		switch strings.ToLower(seg) {
		//: every spelling of "." and ".." that a peer would re-normalise.
		case ".", "..", "%2e", "%2e%2e", ".%2e", "%2e.":
			//: one dot segment is enough to refuse the whole path.
			return true
		}
	}
	//: no segment resolves to a dot segment.
	return false
}
