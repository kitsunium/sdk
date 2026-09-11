// Package token — JWS Compact Serialization (RFC 7515): the header, and the
// algorithm gate every verifier runs before a key reaches a primitive.
package token

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// jwsSegments is the segment count of a JWS Compact Serialization:
	// header, payload, signature.
	jwsSegments int = 3
	// headerSegment indexes the JOSE header segment.
	headerSegment int = 0
	// payloadSegment indexes the claims segment.
	payloadSegment int = 1
	// signatureSegment indexes the signature segment.
	signatureSegment int = 2
	// maxSignatureLen bounds a decoded signature. The widest algorithm here
	// produces 64 octets; the slack costs nothing and the bound is what stops
	// a megabyte "signature" from being decoded before it is rejected.
	maxSignatureLen int = 256
	// maxHeaderDepth bounds the JOSE header's JSON nesting. A header is a flat
	// object of scalars plus at most the "crit" array.
	maxHeaderDepth int = 4
	// defaultType is the "typ" an issuer stamps when the config names none
	// (RFC 7519 §5.1).
	defaultType string = "JWT"
	// algNone is the unsecured algorithm of RFC 7519 §6, matched
	// case-insensitively so "None" and "NONE" get the verdict they deserve
	// rather than the generic mismatch.
	algNone string = "none"
)

// headerValue is the subset of the JOSE header this package acts on. Every
// other member is IGNORED on the way in (RFC 7515 allows unknown parameters)
// and not carried on the way out — re-emitting a parameter we never understood
// would be vouching for it.
type headerValue struct {
	// alg is the "alg" member, verbatim. It is COMPARED against the binding
	// and never used to look anything up.
	alg string
	// typ is the "typ" member, empty when absent.
	typ string
	// kid is the "kid" member, empty when absent. A hint chosen by whoever
	// published the key — never proof of anything.
	kid string
	// critical reports whether a "crit" member was present.
	critical bool
}

// parseJOSEHeader reads a decoded JOSE header object.
func parseJOSEHeader(raw []byte) (header headerValue, err error) {
	//: encoding/json would turn invalid UTF-8 into U+FFFD rather than refuse
	//: it, so the header is held to UTF-8 before it is read, as the claims are.
	if !utf8.Valid(raw) {
		//: refused, never repaired.
		return headerValue{}, malformed("JOSE header is not UTF-8")
	}
	//: bound the nesting first — the header is small and flat.
	if derr := checkJSONDepth(raw, maxHeaderDepth); derr != nil {
		//: propagate TooDeep.
		return headerValue{}, derr
	}
	//: a header that says two things is refused (RFC 8725 §2.6).
	if derr := checkNoDuplicateMembers(raw); derr != nil {
		//: propagate DuplicateMember or Malformed.
		return headerValue{}, derr
	}
	var members map[string]json.RawMessage
	//: only now build the member map.
	if json.Unmarshal(raw, &members) != nil {
		//: not a JSON object.
		return headerValue{}, malformed("JOSE header is not a JSON object")
	}
	alg, aerr := decodeStringClaim(members, "alg")
	//: a non-string alg is malformed.
	if aerr != nil {
		//: propagate.
		return headerValue{}, aerr
	}
	//: the remaining members this package acts on.
	return finishHeader(members, alg)
}

// finishHeader reads the remaining members this package acts on.
func finishHeader(members map[string]json.RawMessage, alg string) (header headerValue, err error) {
	typ, terr := decodeStringClaim(members, "typ")
	//: a non-string typ is malformed.
	if terr != nil {
		//: propagate.
		return headerValue{}, terr
	}
	kid, kerr := decodeStringClaim(members, "kid")
	//: a non-string kid is malformed.
	if kerr != nil {
		//: propagate.
		return headerValue{}, kerr
	}
	//: "crit" names header parameters that MUST be understood (RFC 7515
	//: §4.1.11). This package understands none of them, so its mere presence
	//: is a rejection — "ignore what you do not understand" is exactly how a
	//: security-relevant extension gets silently dropped.
	_, critical := members["crit"]
	//: the four members this package acts on; the rest are ignored.
	return headerValue{alg: alg, typ: typ, kid: kid, critical: critical}, nil
}

// checkHeader enforces the algorithm binding and the typing policy.
//
// This is the algorithm-confusion gate. It COMPARES the header against the
// algorithm the verifier was constructed with; it never uses the header to
// choose a key, an algorithm or a branch. It runs before any key material
// reaches any primitive.
func (p policyValue) checkHeader(header headerValue, bound coretoken.Algorithm) error {
	//: an unsecured token is refused by name, so the log says what happened.
	if strings.EqualFold(header.alg, algNone) {
		//: unconditional — no configuration accepts it.
		return coretoken.AlgorithmNone
	}
	//: a header with no alg at all is not a JWS.
	if header.alg == "" {
		//: structural failure.
		return coretoken.Malformed
	}
	//: the comparison. A mismatch is a refusal, never a re-selection.
	if header.alg != bound.String() {
		//: the verdict an algorithm-confusion attempt produces.
		return errs.Wrap(coretoken.AlgorithmMismatch, errs.WrapParams{},
			errs.String("expected", bound.String()))
	}
	//: an unrecognised critical parameter is a mandatory rejection.
	if header.critical {
		//: refuse rather than ignore.
		return HeaderUnsupported
	}
	//: explicit typing, when the recipient asked for it (RFC 8725 §3.11/§3.12).
	if p.requireType != "" && header.typ != p.requireType {
		//: refuse a token of the wrong kind from the right issuer.
		return errs.Wrap(HeaderUnsupported, errs.WrapParams{},
			errs.String("expected_typ", p.requireType))
	}
	//: header accepted.
	return nil
}

// decodeAndValidate turns an AUTHENTICATED payload segment into claims and
// applies the policy. It is only ever called after a signature has verified.
func (p policyValue) decodeAndValidate(payload string, shape claimShape) (claims coretoken.ClaimsValue, err error) {
	raw, derr := decodeSegment(payload, p.maxTokenLen)
	//: propagate.
	if derr != nil {
		//: a payload segment that is not strict base64url.
		return coretoken.ClaimsValue{}, derr
	}
	decoded, cerr := decodeClaims(raw, p.maxClaimDepth, shape)
	//: propagate.
	if cerr != nil {
		//: malformed, too deep or too many claims.
		return coretoken.ClaimsValue{}, cerr
	}
	//: temporal and identity checks, on authenticated claims only.
	if verr := p.validateClaims(decoded); verr != nil {
		//: return the zero value with the verdict — never claims plus an error.
		return coretoken.ClaimsValue{}, verr
	}
	//: authenticated and accepted.
	return decoded, nil
}
