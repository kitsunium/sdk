// Package token — the PASETO v4.public verifier.
package token

import (
	"crypto/subtle"

	coretoken "github.com/kitsunium/sdk/internal/core/token"
)

// pasetoVerifier authenticates v4.public tokens.
type pasetoVerifier struct {
	// bound is the Ed25519 verification key, tagged AlgorithmPasetoV4Public.
	bound verifyingKey
	// policy is the validated verification policy.
	policy policyValue
	// cfg carries the expected footer and implicit assertion.
	cfg PasetoVerifierConfig
}

// Verify authenticates token and returns its claims, in the same order the JWS
// verifier uses: parse, check the scheme, verify the signature, and only then
// decode and judge the claims.
func (v *pasetoVerifier) Verify(token string) (claims coretoken.ClaimsValue, err error) {
	payload, footer, perr := v.split(token)
	//: bounded parse, no key involved yet.
	if perr != nil {
		//: propagate TooLarge / Malformed / SchemeUnsupported.
		return coretoken.ClaimsValue{}, perr
	}
	//: the footer is compared in constant time — it may carry a key id, and a
	//: length-then-bytes comparison on one leaks its prefix.
	if subtle.ConstantTimeCompare(footer, v.cfg.Footer) != 1 {
		//: refuse rather than authenticate data the port cannot return.
		return coretoken.ClaimsValue{}, FooterMismatch
	}
	//: a body shorter than the signature is not a token.
	if len(payload) < pasetoSigLen {
		//: structural failure.
		return coretoken.ClaimsValue{}, coretoken.Malformed
	}
	message, signature := payload[:len(payload)-pasetoSigLen], payload[len(payload)-pasetoSigLen:]
	//: authenticate before reading anything the token claims about itself.
	if !v.bound.verify(preAuthEncode(
		[]byte(pasetoV4PublicHeader), message, footer, v.cfg.ImplicitAssertion), signature) {
		//: one verdict for "wrong key" and "tampered bytes".
		return coretoken.ClaimsValue{}, coretoken.SignatureInvalid
	}
	//: authenticated: now the claims may be decoded and judged.
	return v.decodeAuthenticated(message)
}

// decodeAuthenticated turns an authenticated PASETO payload into claims.
func (v *pasetoVerifier) decodeAuthenticated(message []byte) (claims coretoken.ClaimsValue, err error) {
	decoded, cerr := decodeClaims(message, v.policy.maxClaimDepth, pasetoShape{})
	//: propagate Malformed / TooDeep / TooLarge.
	if cerr != nil {
		//: the payload authenticated but is not a claim set.
		return coretoken.ClaimsValue{}, cerr
	}
	//: temporal and identity checks, on authenticated claims only.
	if verr := v.policy.validateClaims(decoded); verr != nil {
		//: the zero value with the verdict — never claims plus an error.
		return coretoken.ClaimsValue{}, verr
	}
	//: authenticated and accepted.
	return decoded, nil
}

// split decodes a v4.public token into its payload+signature body and footer,
// refusing any other version or purpose by name.
func (v *pasetoVerifier) split(token string) (payload, footer []byte, err error) {
	seg, serr := splitCompact(token, v.policy.maxTokenLen)
	//: the size and separator bounds already fired if they were going to.
	if serr != nil {
		//: propagate TooLarge or Malformed.
		return nil, nil, serr
	}
	//: three segments, or four with a footer.
	if seg.count < pasetoMinSegments || seg.count > pasetoMaxSegments {
		//: structural failure.
		return nil, nil, coretoken.Malformed
	}
	//: the scheme gate — v4.local lands here, named rather than "malformed".
	if cerr := checkPasetoScheme(seg.part[pasetoVersionSegment], seg.part[pasetoPurposeSegment]); cerr != nil {
		//: propagate SchemeUnsupported or Malformed.
		return nil, nil, cerr
	}
	body, berr := decodeSegment(seg.part[pasetoBodySegment], v.policy.maxTokenLen)
	//: propagate.
	if berr != nil {
		//: a body segment that is not strict base64url.
		return nil, nil, berr
	}
	//: no footer segment means an empty footer, which is what an empty
	//: configured footer compares equal to.
	if seg.count == pasetoMinSegments {
		//: three-segment form.
		return body, nil, nil
	}
	decodedFooter, ferr := decodeSegment(seg.part[pasetoFooterSegment], maxFooterLen)
	//: propagate.
	if ferr != nil {
		//: a footer segment that is not strict base64url.
		return nil, nil, ferr
	}
	//: four-segment form.
	return body, decodedFooter, nil
}
