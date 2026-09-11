// Package token — the three numeric limits a verification policy resolves.
package token

// boundsValue groups the size and depth limits a policy applies, so resolving
// them stays one call rather than three near-identical error checks inside
// newPolicy — which is what kept that function readable when the third bound
// arrived.
type boundsValue struct {
	// tokenLen is the resolved maximum token-string length.
	tokenLen int
	// claimDepth is the resolved maximum claims JSON nesting.
	claimDepth int
	// keyCandidates is the resolved maximum number of keys a set verifier
	// will try for one kid.
	keyCandidates int
}

// resolveBounds maps every zero bound to its default and refuses one outside
// its documented range.
func resolveBounds(cfg VerifierConfig) (bounds boundsValue, err error) {
	tokenLen, lerr := resolveBound(cfg.MaxTokenLen, DefaultMaxTokenLen,
		MinTokenLen, MaxTokenLenCeiling, "MaxTokenLen")
	//: propagate.
	if lerr != nil {
		//: refuse at construction.
		return boundsValue{}, lerr
	}
	depth, derr := resolveBound(cfg.MaxClaimDepth, DefaultMaxClaimDepth,
		1, MaxClaimDepthCeiling, "MaxClaimDepth")
	//: propagate.
	if derr != nil {
		//: refuse at construction.
		return boundsValue{}, derr
	}
	candidates, cerr := resolveBound(cfg.MaxKeyCandidates, DefaultMaxKeyCandidates,
		1, MaxKeyCandidatesCeiling, "MaxKeyCandidates")
	//: propagate.
	if cerr != nil {
		//: refuse at construction.
		return boundsValue{}, cerr
	}
	//: all three inside their ranges.
	return boundsValue{tokenLen: tokenLen, claimDepth: depth, keyCandidates: candidates}, nil
}

// resolveBound maps zero to fallback and refuses anything outside [low, high],
// naming knob in the refusal.
func resolveBound(configured, fallback, low, high int, knob string) (value int, err error) {
	//: zero means "unset" for every bound in this package.
	if configured == 0 {
		//: the documented default.
		return fallback, nil
	}
	//: an out-of-range bound is a construction-site mistake, not a policy.
	if configured < low || configured > high {
		//: name the knob so the fix is greppable.
		return 0, misconfigured(knob)
	}
	//: the caller's value, inside the range.
	return configured, nil
}
