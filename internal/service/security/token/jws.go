package token

import (
	"bytes"
	"strings"
	"unicode/utf8"

	coretoken "github.com/kitsunium/sdk/internal/core/security/token"
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

// headerView is a JOSE header as the scanner read it: each member this
// package acts on as its string literal inside the decoded header, nil when
// absent or null. It decodes nothing, so reading it allocates nothing.
type headerView struct {
	// alg / typ / kid are the members' literals, quotes included.
	alg, typ, kid []byte
	// critical reports whether a "crit" member was present.
	critical bool
}

// readJOSEHeader reads a decoded JOSE header object, under the same rules as a
// claims object: UTF-8, a depth bound, and the one strict scan that refuses
// malformed text and a duplicate or escaped member name (RFC 8725 §2.6).
//
// The view is returned by value: a view written through a pointer would make
// raw escape, and a VerifyInto decodes raw on its stack.
func readJOSEHeader(raw []byte) (view headerView, err error) {
	//: a reader that repairs invalid UTF-8 would make two headers one, so the
	//: header is held to UTF-8 before it is read, as the claims are.
	if !utf8.Valid(raw) {
		//: refused, never repaired.
		return headerView{}, malformed("JOSE header is not UTF-8")
	}
	//: bound the nesting first — the header is small and flat.
	if derr := checkJSONDepth(raw, maxHeaderDepth); derr != nil {
		//: propagate TooDeep.
		return headerView{}, derr
	}
	var idx objectIndex
	//: a header that says two things is refused, and so is one that is not
	//: a JSON object.
	if serr := scanObject(raw, maxHeaderDepth, &idx); serr != nil {
		//: propagate Malformed, DuplicateMember or TooLarge.
		return headerView{}, serr
	}
	var merr error
	//: the three string members, in the order the legacy reader judged them.
	if view.alg, merr = headerMember(raw, &idx, "alg"); merr != nil {
		//: a non-string alg is malformed.
		return headerView{}, merr
	}
	//: then typ.
	if view.typ, merr = headerMember(raw, &idx, "typ"); merr != nil {
		//: a non-string typ is malformed.
		return headerView{}, merr
	}
	//: then kid.
	if view.kid, merr = headerMember(raw, &idx, "kid"); merr != nil {
		//: a non-string kid is malformed.
		return headerView{}, merr
	}
	//: "crit" names header parameters that MUST be understood (RFC 7515
	//: §4.1.11). This package understands none of them, so its mere presence
	//: is a rejection — "ignore what you do not understand" is exactly how a
	//: security-relevant extension gets silently dropped.
	view.critical = idx.lookup(raw, "crit") >= 0
	//: the four members this package acts on; the rest are ignored.
	return view, nil
}

// headerMember returns the literal of the string member name, nil when it is
// absent or null — which a header has always read as absent — and Malformed
// when it is present as anything else.
func headerMember(raw []byte, idx *objectIndex, name string) (lit []byte, err error) {
	at := idx.lookup(raw, name)
	//: absent, or null.
	if at < 0 || idx.member[at].kind == kindNull {
		//: absent.
		return nil, nil
	}
	//: present but not a string is malformed.
	if idx.member[at].kind != kindString {
		//: the legacy reader's wording, kept.
		return nil, malformed("registered claim " + name + " is not a string")
	}
	//: a view of the literal.
	return idx.value(raw, at), nil
}

// parseJOSEHeader reads a decoded JOSE header object into the string form the
// set verifier selects a key with.
func parseJOSEHeader(raw []byte) (header headerValue, err error) {
	view, rerr := readJOSEHeader(raw)
	//: the one reader.
	if rerr != nil {
		//: propagate.
		return headerValue{}, rerr
	}
	//: decoded once, the common spellings without an allocation.
	return headerValue{
		alg: headerText(view.alg), typ: headerText(view.typ),
		kid: headerText(view.kid), critical: view.critical,
	}, nil
}

// headerText decodes a header member's literal, "" when it was absent or
// null. The spellings nearly every header carries come back as constants.
func headerText(lit []byte) string {
	//: absent or null.
	if lit == nil {
		//: this domain's "absent".
		return ""
	}
	//: the names this package itself writes, interned.
	for _, known := range [...]string{"HS256", "ES256", "EdDSA", defaultType} {
		//: compared without converting.
		if string(lit[1:len(lit)-1]) == known {
			//: no allocation.
			return known
		}
	}
	//: anything else, decoded.
	return decodeText(lit)
}

// checkHeader enforces the algorithm binding and the typing policy.
//
// This is the algorithm-confusion gate. It COMPARES the header against the
// algorithm the verifier was constructed with; it never uses the header to
// choose a key, an algorithm or a branch. It runs before any key material
// reaches any primitive.
func (p policyValue) checkHeader(header headerValue, bound coretoken.Algorithm) error {
	//: the same judgement the scanner's view gets, from decoded strings.
	return p.judgeHeader(headerFacts{
		unsecured: strings.EqualFold(header.alg, algNone), algEmpty: header.alg == "",
		algBound: header.alg == bound.String(), critical: header.critical,
		typeOK: p.requireType == "" || header.typ == p.requireType,
	}, bound)
}

// checkHeaderView is checkHeader over a header the scanner read, compared
// literal against expectation without decoding anything.
func (p policyValue) checkHeaderView(view *headerView, bound coretoken.Algorithm) error {
	//: the same judgement checkHeader makes.
	return p.judgeHeader(headerFacts{
		unsecured: literalIsNone(view.alg), algEmpty: literalEqual(view.alg, ""),
		algBound: literalEqual(view.alg, bound.String()), critical: view.critical,
		typeOK: p.requireType == "" || literalEqual(view.typ, p.requireType),
	}, bound)
}

// headerFacts is what the header judgement needs, whichever reader produced it.
type headerFacts struct {
	// unsecured: alg is "none" in some capitalisation.
	unsecured bool
	// algEmpty: alg is absent, null or "".
	algEmpty bool
	// algBound: alg names the bound algorithm exactly.
	algBound bool
	// critical: a "crit" member was present.
	critical bool
	// typeOK: no type is required, or typ is exactly the required one.
	typeOK bool
}

// judgeHeader returns the first verdict facts earn, in the one order every
// path applies.
func (p policyValue) judgeHeader(facts headerFacts, bound coretoken.Algorithm) error {
	//: an unsecured token is refused by name, so the log says what happened.
	if facts.unsecured {
		//: unconditional — no configuration accepts it.
		return coretoken.AlgorithmNone
	}
	//: a header with no alg at all is not a JWS.
	if facts.algEmpty {
		//: structural failure.
		return coretoken.Malformed
	}
	//: the comparison. A mismatch is a refusal, never a re-selection.
	if !facts.algBound {
		//: the verdict an algorithm-confusion attempt produces.
		return errs.Wrap(coretoken.AlgorithmMismatch, errs.WrapParams{},
			errs.String("expected", bound.String()))
	}
	//: an unrecognised critical parameter is a mandatory rejection.
	if facts.critical {
		//: refuse rather than ignore.
		return coretoken.HeaderUnsupported
	}
	//: explicit typing, when the recipient asked for it (RFC 8725 §3.11/§3.12).
	if !facts.typeOK {
		//: refuse a token of the wrong kind from the right issuer.
		return errs.Wrap(coretoken.HeaderUnsupported, errs.WrapParams{},
			errs.String("expected_typ", p.requireType))
	}
	//: header accepted.
	return nil
}

// literalEqual reports whether a header member's literal decodes to want; an
// absent or null member decodes to "".
func literalEqual(lit []byte, want string) bool {
	//: absent or null.
	if lit == nil {
		//: equal only to the empty string.
		return want == ""
	}
	//: compared as it is read.
	return jsonStringEqual(lit, want)
}

// literalIsNone reports whether a header member's literal decodes to "none"
// in any capitalisation, folded as strings.EqualFold folds. No letter of
// "none" has a case partner outside ASCII, so every spelling of it decodes to
// four octets, and a longer literal is not one.
func literalIsNone(lit []byte) bool {
	//: absent, null, or longer than any spelling of four characters.
	if lit == nil || len(lit) > 2+4*unicodeEscapeLen {
		//: not "none".
		return false
	}
	var buf [4 * unicodeEscapeLen]byte
	n, ok := decodeTextInto(buf[:], lit)
	//: the same folding the string path applies.
	return ok && n == len(algNone) && bytes.EqualFold(buf[:n], []byte(algNone))
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

// refuseUnsecured refuses an unsecured token — "alg": "none", in any case — by
// name. It needs no key, so a verifier that selects among several runs it
// before selecting, and the verdict never depends on which keys it holds.
func refuseUnsecured(header headerValue) error {
	//: unconditional — no configuration accepts it.
	if strings.EqualFold(header.alg, algNone) {
		//: AlgorithmNone.
		return coretoken.AlgorithmNone
	}
	//: secured, as far as the header says.
	return nil
}
