// Package token — the single-key JWS compact verifier.
package token

import coretoken "github.com/kitsunium/sdk/internal/core/token"

// jwsVerifier authenticates JWS compact tokens under one algorithm-bound key.
type jwsVerifier struct {
	// bound is the verification key; its algorithm is fixed at construction
	// and is the only algorithm this verifier will ever accept.
	bound verifyingKey
	// policy is the validated verification policy.
	policy policyValue
}

// newJWSVerifier validates cfg and returns the bound verifier.
func newJWSVerifier(bound verifyingKey, cfg VerifierConfig) (verifier coretoken.Verifier, err error) {
	policy, perr := newPolicy(cfg)
	//: every bound and knob is validated once, here.
	if perr != nil {
		//: propagate PolicyMisconfigured.
		return nil, perr
	}
	//: bound for life.
	return &jwsVerifier{bound: bound, policy: policy}, nil
}

// Verify authenticates token and returns its claims.
//
// The order is the contract: parse, compare the algorithm against the binding,
// verify the signature, and only then decode and judge the claims. A caller
// never receives — or logs — a claim out of a token that did not authenticate.
func (v *jwsVerifier) Verify(token string) (claims coretoken.ClaimsValue, err error) {
	parts, perr := v.policy.parseJWS(token)
	//: bounded parse, no key involved yet.
	if perr != nil {
		//: propagate TooLarge / Malformed / TooDeep / DuplicateMember.
		return coretoken.ClaimsValue{}, perr
	}
	//: the algorithm gate, before any key reaches any primitive.
	if herr := v.policy.checkHeader(parts.header, v.bound.algorithm()); herr != nil {
		//: propagate AlgorithmNone / AlgorithmMismatch / HeaderUnsupported.
		return coretoken.ClaimsValue{}, herr
	}
	//: authenticate before reading anything the token claims about itself.
	if !v.bound.verify(parts.input, parts.signature) {
		//: one verdict for "wrong key" and "tampered bytes" — same event.
		return coretoken.ClaimsValue{}, coretoken.SignatureInvalid
	}
	//: authenticated: now the claims may be decoded and judged.
	return v.policy.decodeAndValidate(parts.payload, joseShape{})
}
