// Package vcs is the version-control contract: what "the set a branch changed"
// is, and what a resolution of it reports.
//
// The domain is deliberately thin. It names a CHANGED SET — the files, line
// ranges and directories a branch touched relative to its merge-base — and the
// outcome of computing one. It does not model repositories, commits, refs or
// history: the SDK has exactly one implementation, which shells out to the git
// binary (internal/service/vcs/git), and a contract broader than that would
// describe nothing real.
//
// There is **no registry**. A registry's key would be a VCS name, and resolving
// one from a config string would let a typo silently swap the implementation
// with every call still succeeding — the same argument ADR 0052 makes for lock.
package vcs

// ChangedSet reports what a branch changed, at the three granularities a caller
// can ask about. It is FROZEN at four methods.
//
// Every path argument is absolute. Implementations normalise through
// filepath.Clean before comparing, so a caller need not.
//
// The three Contains methods are not interchangeable and none implies another in
// the direction a caller might assume: a pure rename or a deletion touches a
// FILE and its DIRECTORY while contributing no line range at all, so
// ContainsFile can be true where ContainsLine is false for every line of it.
// That is the point — a check that only looks at lines silently ignores a
// deleted file.
type ChangedSet interface {
	// ContainsLine reports whether line on absFile falls inside a changed hunk.
	// Lines are 1-based and the bounds are inclusive.
	ContainsLine(absFile string, line int) bool
	// ContainsFile reports whether absFile appears anywhere in the diff,
	// including as the old side of a rename or a deletion.
	ContainsFile(absFile string) bool
	// ContainsDir reports whether absDir directly encloses any touched file.
	// It is not recursive: a parent of a touched directory is not itself
	// touched.
	ContainsDir(absDir string) bool
	// IsEmpty reports whether nothing at all was touched — a clean branch, or
	// one whose every change the caller's own filter excluded.
	IsEmpty() bool
}

// ResolutionValue is the outcome of resolving what a branch changed.
//
// It has exactly two readable shapes and the zero value is neither, which is
// deliberate: a ResolutionValue nobody produced carries a nil Set AND
// FullFallback false, a combination no constructor mints, so a caller that
// forgot to check reads "nothing changed" from no code path that ever ran.
// Check Degraded first.
//
//   - Resolved: Set is non-nil and BaseRef / BaseSHA / HeadSHA name the
//     comparison that produced it.
//   - Degraded: FullFallback is true and Reason says why. Set is nil. The
//     caller must treat EVERYTHING as in scope.
//
// The degraded shape is the whole reason this is a value rather than
// (ChangedSet, error). A resolver that cannot be trusted must never answer with
// an empty set: "nothing changed" and "I could not tell what changed" are
// opposite instructions, and conflating them is how a review silently passes on
// a branch it never looked at.
type ResolutionValue struct {
	// Set is what the branch changed, or nil when FullFallback is true.
	Set ChangedSet
	// BaseRef is the default-branch ref the comparison used, e.g. "origin/main".
	// Empty when degraded.
	BaseRef string
	// BaseSHA is the merge-base commit between BaseRef and HEAD. Empty when
	// degraded.
	BaseSHA string
	// HeadSHA is the commit HEAD resolved to. Empty when degraded.
	HeadSHA string
	// FullFallback reports that no trustworthy changed set could be computed and
	// the caller must fall back to considering everything in scope.
	FullFallback bool
	// Reason is the human-readable cause of the fallback, meant to be surfaced
	// rather than swallowed. Empty when not degraded.
	Reason string
}

// Degraded reports whether the resolution failed to produce a trustworthy set
// and the caller must treat everything as in scope.
//
// It is the predicate callers want, and it reads from FullFallback rather than
// from a nil Set so the two can never disagree.
//
// The receiver is a pointer because the value is 88 bytes — four strings, an
// interface and a bool — and copying all of it to read one bool is what
// KTN-VAR-BIGSTRUCT exists to catch. Assign the resolution to a variable and
// call it on that; the chained form Resolve(...).Degraded() does not compile,
// which is a fair trade for not copying 88 bytes per query.
func (r *ResolutionValue) Degraded() bool {
	//: FullFallback is the authoritative signal; a nil Set merely accompanies it.
	return r.FullFallback
}
