// Package errs — holds the wrap-trail mechanism introduced by
// ADR 0005. Trail carries the sites a *Error was wrapped through; origin
// wins on Code() per ADR 0002, but Trail() / HasCode() traverse the full
// list of wrap sites to help observability and matching.
package errs

import "slices"

// maxTrailLen caps the trail length so pathological recursive wraps cannot
// grow the trail unbounded. Observed middleware depth in Go SDKs (go-kit,
// otel, sqlx, grpc-go) is <= 5; 16 gives 3x headroom. Memory cost per
// *Error with a full trail: 16 * 4 bytes = 64 bytes, negligible.
const maxTrailLen int = 16

// trailReserveTail is subtracted from maxTrailLen to compute how many tail
// entries to preserve on overflow truncation: preservedTail = maxTrailLen -
// trailReserveTail. The two reserved slots account for the origin (kept at
// index 0) and the newest entry being appended, so the final slice
// origin + preservedTail + newest fits within maxTrailLen.
const trailReserveTail int = 2

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
func appendTrail(cause *Error, next Code) (trail []Code, truncated bool) {
	//: snapshot the cause's trail (if any) and its truncation flag.
	var existing []Code
	var inheritedTrunc bool
	//: only an existing *Error carries a prior trail; nil cause starts fresh.
	if cause != nil {
		existing = cause.trail
		inheritedTrunc = cause.trailTruncated
	}

	//: a zero Code is meaningless and would poison later HasCode / PrefixMatcher lookups, so we silently preserve the existing trail and let
	//: the AST audit flag the offending caller separately.
	if next == 0 {
		//: empty cause + zero next → nil trail (nothing to clone).
		if len(existing) == 0 {
			//: callers expect a nil slice when there is nothing to record.
			return nil, inheritedTrunc
		}
		//: non-empty cause + zero next → defensive clone, no append.
		return slices.Clone(existing), inheritedTrunc
	}

	//: happy path — fits under cap, no truncation triggered here.
	if len(existing)+1 <= maxTrailLen {
		//: clone then append so the returned slice never aliases the cause.
		return append(slices.Clone(existing), next), inheritedTrunc
	}

	//: overflow — keep [origin] + last (maxTrailLen-trailReserveTail) links + [next].
	trail = make([]Code, 0, maxTrailLen)
	trail = append(trail, existing[0])
	trail = append(trail, existing[len(existing)-(maxTrailLen-trailReserveTail):]...)
	trail = append(trail, next)
	//: overflow always sets truncated=true regardless of the inherited flag.
	return trail, true
}
