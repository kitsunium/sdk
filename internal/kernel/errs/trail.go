// Package errs: trail.go holds the wrap-trail mechanism introduced by
// ADR 0005. Trail carries the sites a *Error was wrapped through; origin
// wins on Code() per ADR 0002, but Trail() / HasCode() traverse the full
// list of wrap sites to help observability and matching.
package errs

import "slices"

// maxTrailLen caps the trail length so pathological recursive wraps cannot
// grow the trail unbounded. Observed middleware depth in Go SDKs (go-kit,
// otel, sqlx, grpc-go) is <= 5; 16 gives 3x headroom. Memory cost per
// *Error with a full trail: 16 * 4 bytes = 64 bytes, negligible.
const maxTrailLen = 16

// appendTrail returns the new trail slice and whether truncation occurred.
// Contract:
//   - cause == nil (no prior trail) → trail = [next] unless next == 0.
//   - next == 0 → refuse to write a meaningless zero Code; existing trail
//     is preserved untouched. (v5 fix: protects HasCode / PrefixMatcher
//     from caller bugs on the origin-wins path where params.Code is
//     appended unchecked.)
//   - under cap → append, inherit cause.trailTruncated (monotonic OR).
//   - overflow → keep origin + (maxTrailLen-2) most recent entries + next;
//     trailTruncated transitions to true and stays true.
//
// Params:
//   - cause: the *Error being wrapped (may be nil for stdlib-cause path).
//   - next: the Code to append to the trail.
//
// Returns:
//   - out: fresh slice (never aliases the cause's internal storage).
//   - truncated: monotonic truncation flag for the returned trail.
func appendTrail(cause *Error, next Code) (out []Code, truncated bool) {
	//: snapshot the cause's trail (if any) and its truncation flag.
	var existing []Code
	var inheritedTrunc bool
	if cause != nil {
		existing = cause.trail
		inheritedTrunc = cause.trailTruncated
	}

	//: v5 guard — a zero Code is meaningless and would poison later
	//: HasCode / PrefixMatcher lookups. Silently preserve the existing
	//: trail; the audit (A8) or the linter can flag the caller separately.
	if next == 0 {
		if len(existing) == 0 {
			return nil, inheritedTrunc
		}
		return slices.Clone(existing), inheritedTrunc
	}

	//: happy path — fits under cap, no truncation triggered here.
	if len(existing)+1 <= maxTrailLen {
		return append(slices.Clone(existing), next), inheritedTrunc
	}

	//: overflow — keep [origin] + last (maxTrailLen-2) links + [next].
	out = make([]Code, 0, maxTrailLen)
	out = append(out, existing[0])
	out = append(out, existing[len(existing)-(maxTrailLen-2):]...)
	out = append(out, next)
	return out, true
}
