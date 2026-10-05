package errs

// newPrefixMatcher is NewPrefixMatcher's body: decl_gen.go writes NewPrefixMatcher, from the
// design, as one call of it.
func newPrefixMatcher(prefix, mask Code) *PrefixMatcher {
	//: single struct literal — no allocation optimisation needed at this scale.
	return &PrefixMatcher{prefix: prefix, mask: mask}
}

// String returns a diagnostic representation of the matcher.
func (p *PrefixMatcher) String() string {
	//: rely on Code.String() for each component — canonical dotted form.
	return "PrefixMatcher{prefix=" + p.prefix.String() + ", mask=" + p.mask.String() + "}"
}

// Error implements the `error` interface so PrefixMatcher is usable as a
// target for `errors.Is`. Returning an error-looking string is the ONLY
// way Go's errors.Is can accept a non-sentinel target; callers MUST never
// return a *PrefixMatcher from a function as an error. A follow-up linter
// rule will catch accidental escapes.
func (p *PrefixMatcher) Error() string {
	//: identical to String() — the `error` conformance is a stdlib-protocol
	//: concession, not a claim that a PrefixMatcher represents a failure.
	return p.String()
}

// Prefix exposes the matcher's prefix for internal callers (e.g., the
// (*Error).Is method) without leaking the field directly.
func (p *PrefixMatcher) Prefix() Code {
	//: direct read — PrefixMatcher is immutable after construction.
	return p.prefix
}

// Mask exposes the matcher's mask for internal callers.
func (p *PrefixMatcher) Mask() Code {
	//: direct read — see Prefix.
	return p.mask
}
