package git

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
