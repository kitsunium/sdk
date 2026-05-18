// Package errs: prefix_matcher.go hosts the PrefixMatcher target used by
// errors.Is for CIDR-style Code matching. PrefixMatcher deliberately does
// NOT implement the `error` interface so it cannot escape into error
// chains as a return value — callers pass it only to errors.Is.
package errs

// PrefixMatcher is a NON-ERROR sentinel used ONLY as a target for errors.Is.
// Callers build it via NewPrefixMatcher and pass it to errors.Is; the
// matching logic lives on (*Error).Is (see error.go, added in W4).
type PrefixMatcher struct {
	prefix Code
	mask   Code
}

// NewPrefixMatcher constructs a PrefixMatcher usable with errors.Is to
// match all Codes sharing a given bit-prefix (defined by the mask).
// Example: all codes originating from pkg/v1/* →
//
//	errors.Is(err, errs.NewPrefixMatcher(0x01_00_00_00, errs.MaskByMajor))
//
// Params:
//   - prefix: the pattern the Code must match after masking.
//   - mask: which bits participate in the match (use MaskBy* constants).
//
// Returns:
//   - *PrefixMatcher: opaque target for errors.Is.
func NewPrefixMatcher(prefix, mask Code) *PrefixMatcher {
	//: single struct literal — no allocation optimisation needed at this scale.
	return &PrefixMatcher{prefix: prefix, mask: mask}
}

// String returns a diagnostic representation of the matcher.
//
// Returns:
//   - string: human-readable "PrefixMatcher{prefix=…, mask=…}" form.
func (p *PrefixMatcher) String() string {
	//: rely on Code.String() for each component — canonical dotted form.
	return "PrefixMatcher{prefix=" + p.prefix.String() + ", mask=" + p.mask.String() + "}"
}

// Error implements the `error` interface so PrefixMatcher is usable as a
// target for `errors.Is`. Returning an error-looking string is the ONLY
// way Go's errors.Is can accept a non-sentinel target; callers MUST never
// return a *PrefixMatcher from a function as an error. A follow-up linter
// rule (KTN-ERRS-PREFIXMATCHER-RETURN) will catch accidental escapes.
//
// Returns:
//   - string: identical to String() — same diagnostic form.
func (p *PrefixMatcher) Error() string {
	//: identical to String() — the `error` conformance is a stdlib-protocol
	//: concession, not a claim that a PrefixMatcher represents a failure.
	return p.String()
}

// Prefix exposes the matcher's prefix for internal callers (e.g., the
// (*Error).Is method) without leaking the field directly.
//
// Returns:
//   - Code: the prefix value supplied at construction.
func (p *PrefixMatcher) Prefix() Code {
	//: direct read — PrefixMatcher is immutable after construction.
	return p.prefix
}

// Mask exposes the matcher's mask for internal callers.
//
// Returns:
//   - Code: the mask value supplied at construction.
func (p *PrefixMatcher) Mask() Code {
	//: direct read — see Prefix.
	return p.mask
}
