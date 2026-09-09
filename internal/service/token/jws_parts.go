// Package token — the parsed, not-yet-authenticated shape of a compact token.
package token

import coretoken "github.com/kitsunium/sdk/internal/core/token"

// jwsPartsValue is a parsed, NOT YET AUTHENTICATED compact token. Nothing in
// it may be trusted; it exists so the parse happens once even when several
// candidate keys are tried.
type jwsPartsValue struct {
	// header is the parsed JOSE header.
	header headerValue
	// payload is the still-encoded claims segment.
	payload string
	// input is the signing input: the header and payload segments, still
	// base64url-encoded, joined by their separator (RFC 7515 §5.1).
	input []byte
	// signature is the decoded signature octets.
	signature []byte
}

// parseJWS splits, decodes and reads a compact token without consulting a key.
func (p policyValue) parseJWS(tok string) (parts jwsPartsValue, err error) {
	seg, serr := splitCompact(tok, p.maxTokenLen)
	//: the size and separator bounds already fired if they were going to.
	if serr != nil {
		//: propagate TooLarge or Malformed.
		return jwsPartsValue{}, serr
	}
	//: a JWS compact token has exactly three segments — no more, no fewer. A
	//: two-segment "unsecured JWS" lands here too, and is refused.
	if seg.count != jwsSegments {
		//: structural failure.
		return jwsPartsValue{}, coretoken.Malformed
	}
	header, herr := parseHeaderSegment(seg.part[headerSegment])
	//: propagate.
	if herr != nil {
		//: an unreadable header segment.
		return jwsPartsValue{}, herr
	}
	signature, gerr := decodeSegment(seg.part[signatureSegment], maxSignatureLen)
	//: propagate.
	if gerr != nil {
		//: a signature segment that is not strict base64url.
		return jwsPartsValue{}, gerr
	}
	//: the signing input is a PREFIX of the original token, so it is taken by
	//: slicing rather than rebuilt — a re-join could differ from what was signed.
	inputLen := len(seg.part[headerSegment]) + 1 + len(seg.part[payloadSegment])
	//: parsed, unauthenticated.
	return jwsPartsValue{
		header: header, payload: seg.part[payloadSegment],
		input: []byte(tok[:inputLen]), signature: signature,
	}, nil
}

// parseHeaderSegment decodes and reads the first segment of a compact token.
func parseHeaderSegment(segment string) (header headerValue, err error) {
	raw, derr := decodeSegment(segment, maxHeaderLen)
	//: propagate.
	if derr != nil {
		//: a header segment that is not strict base64url.
		return headerValue{}, derr
	}
	//: a header that is not a readable JOSE object is not a token.
	return parseJOSEHeader(raw)
}
