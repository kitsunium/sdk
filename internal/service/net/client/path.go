// Package client — path safety checks applied before any allowlist pattern.
package client

import (
	"strings"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// escapeSlash and escapeBackslash are the two percent-escapes a peer's router
// re-reads as a path separator, written lowercase for the fold. They are the
// same length — one "%" and two hex digits — which is what lets one window
// serve both comparisons.
const (
	// escapeSlash is "/" percent-encoded.
	escapeSlash string = "%2f"
	// escapeBackslash is "\" percent-encoded.
	escapeBackslash string = "%5c"
)

// dotSegments is every spelling of "." and ".." that a peer would re-normalise,
// written lowercase so a candidate segment is folded rather than the table.
//
// Percent-encoding is case-insensitive — %2E and %2e denote the same octet — so
// the comparison has to ignore case somewhere. Doing it on this side costs
// nothing: the table is fixed and already lowercase, while folding the input
// meant allocating a copy of every segment a caller ever sends.
var dotSegments = [...]string{".", "..", "%2e", "%2e%2e", ".%2e", "%2e."}

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
	//: an encoded separator makes the wire form and the upstream's view of the
	//: path disagree, so no pattern can authorise it honestly.
	if hasEncodedSeparator(escapedPath) {
		//: refuse without echoing the path.
		return errs.Wrap(corenet.UnsafePath, errs.WrapParams{},
			errs.String("why", "path carries a percent-encoded separator"))
	}
	//: the path is absolute, normalised, and means the same thing on both ends.
	return nil
}

// hasEncodedSeparator reports a percent-encoded path separator.
//
// This closes the gap that judging the escaped path opens. Patterns are matched
// against the wire form, so `/v1/supi/a%2fb` satisfies `^/v1/supi/[^/]+$` as a
// single segment — while an upstream that decodes before routing sees
// `/v1/supi/a/b`, two segments and a different resource than the policy believed
// it was authorising. The two views disagree, so the only honest answer is to
// refuse: a caller with a legitimate slash in an identifier is asking the
// allowlist a question it cannot answer.
//
// It scans in place. The obvious spelling — Contains over a lowercased copy —
// allocated the WHOLE path for every request carrying a single uppercase byte,
// which url.URL.EscapedPath guarantees on any percent-encoded path (it emits
// %2F, not %2f) and which a canonical UUID carries on a path that is not
// adversarial at all. Equivalence with that spelling is not argued, it is
// tested: Test_pathScansAreASCIIFoldEquivalent sweeps every Unicode code point.
func hasEncodedSeparator(escapedPath string) bool {
	//: an escape needs a whole triplet, so the last start position is the one
	//: with room for it.
	for i := 0; i+len(escapeSlash) <= len(escapedPath); i++ {
		//: only a "%" can begin an escape; everything else is skipped whole.
		if escapedPath[i] != '%' {
			continue
		}
		triplet := escapedPath[i : i+len(escapeSlash)]
		//: both denote a separator to an upstream that decodes before routing.
		if foldsASCII(triplet, escapeSlash) || foldsASCII(triplet, escapeBackslash) {
			//: one encoded separator is enough to refuse the whole path.
			return true
		}
	}
	//: no encoded separator anywhere in the path.
	return false
}

// hasDotSegment reports a "." or ".." segment in literal or percent-encoded
// form. Both forms are reinterpreted upstream, so neither may reach a pattern.
func hasDotSegment(escapedPath string) bool {
	//: inspect each segment of the escaped path in turn. SplitSeq iterates
	//: without materialising the slice of segments.
	for seg := range strings.SplitSeq(strings.TrimPrefix(escapedPath, "/"), "/") {
		//: every spelling of "." and ".." that a peer would re-normalise.
		for _, want := range dotSegments {
			//: the length check inside foldsASCII rejects an ordinary segment —
			//: an identifier, a resource name — on its first comparison.
			if foldsASCII(seg, want) {
				//: one dot segment is enough to refuse the whole path.
				return true
			}
		}
	}
	//: no segment resolves to a dot segment.
	return false
}

// foldsASCII reports whether s equals want when ASCII letters are compared
// without regard to case. want MUST already be lowercase ASCII.
//
// It is exactly strings.ToLower(s) == want for every want this package uses,
// and it allocates nothing. The equality holds because no code point outside
// ASCII lowercases into any byte those constants are spelled with, which the
// equivalence test asserts by sweeping the whole code space rather than by
// trusting this sentence.
func foldsASCII(s, want string) bool {
	//: a length mismatch is the common answer and costs one comparison.
	if len(s) != len(want) {
		//: cannot be equal at any case.
		return false
	}
	//: compare byte by byte, folding only the input.
	for i := range len(s) {
		//: the first difference decides.
		if lowerASCII(s[i]) != want[i] {
			//: not this spelling.
			return false
		}
	}
	//: every byte matched.
	return true
}

// lowerASCII lowercases one ASCII letter and leaves every other byte untouched,
// including every byte of a multi-byte UTF-8 sequence.
func lowerASCII(b byte) byte {
	//: only A-Z moves; the offset is the gap between the two ASCII runs.
	if b >= 'A' && b <= 'Z' {
		//: shift into the lowercase run.
		return b + ('a' - 'A')
	}
	//: not an ASCII capital, so it is already in its folded form.
	return b
}
