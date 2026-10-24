package token

// The legacy reader: the encoding/json claims and header decoder this package
// shipped before the reflection-free scanner of json_scan.go replaced it
// behind Verify. It is kept, unchanged but for its names, as the ORACLE the
// differential tests and the fuzz target judge the scanner against
// (legacy_differential_test.go): what it accepts and the scanner refuses must
// be a listed divergence class, and what it refuses the scanner must refuse.
// checkNoDuplicateMembers and skipValue keep their names, because BENCH.md §12
// and the duplicate-scan benchmarks measure them by name.

import (
	"bytes"
	"encoding/json"
	"math"
	"time"
	"unicode/utf8"

	coretoken "github.com/kitsunium/sdk/internal/core/security/token"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// legacyShape is the claim decoding half of the legacy claimShape.
type legacyShape interface {
	// decodeTime reads one registered time claim.
	decodeTime(raw json.RawMessage) (time.Time, error)
	// decodeAudience reads the "aud" claim into its general slice form.
	decodeAudience(raw json.RawMessage) ([]string, error)
}

// legacyJose is the legacy JWT/JOSE claim decoding.
type legacyJose struct{}

// legacyPaseto is the legacy PASETO claim decoding.
type legacyPaseto struct{}

// legacyDecodeClaims turns a raw claims object into a ClaimsValue under shape.
//
// The refusals happen in cost order, cheapest first: text that is not UTF-8
// and nesting depth (two linear scans), duplicate members (one streaming pass),
// then member count. Only after all of them does encoding/json build anything.
//
// UTF-8 is checked because encoding/json does not refuse invalid bytes — it
// replaces each with U+FFFD — so without it "a\xff" and "a\xfe" from a careless
// or hostile issuer would verify as one subject. RFC 8259 §8.1 makes JSON
// exchanged between systems UTF-8, and RFC 8725 §3.7 asks the reader to hold
// the writer to it.
func legacyDecodeClaims(raw []byte, maxDepth int, shape legacyShape) (claims coretoken.ClaimsValue, err error) {
	//: two byte strings must never decode to one claim value.
	if !utf8.Valid(raw) {
		//: refused before anything is decoded, never repaired.
		return coretoken.ClaimsValue{}, malformed("claims are not UTF-8")
	}
	//: bound the nesting before a decoder allocates a frame per level.
	if derr := checkJSONDepth(raw, maxDepth); derr != nil {
		//: the depth verdict already names the limit.
		return coretoken.ClaimsValue{}, derr
	}
	//: refuse a claims object that says two things (RFC 8725 §2.6).
	if derr := checkNoDuplicateMembers(raw); derr != nil {
		//: propagate DuplicateMember or Malformed unchanged.
		return coretoken.ClaimsValue{}, derr
	}
	var members map[string]json.RawMessage
	//: now it is safe to build the member map.
	if uerr := json.Unmarshal(raw, &members); uerr != nil {
		//: a payload that is not a JSON object is not a claim set.
		return coretoken.ClaimsValue{}, malformed("claims are not a JSON object")
	}
	//: cap the member count so a valid-but-vast payload is still refused.
	if len(members) > maxClaimMembers {
		//: name the limit, never the members.
		return coretoken.ClaimsValue{}, errs.Wrap(coretoken.TooLarge,
			errs.WrapParams{}, errs.Int("limit", maxClaimMembers))
	}
	registered, rerr := legacyDecodeRegistered(members, shape)
	//: a malformed registered claim stops the decode.
	if rerr != nil {
		//: propagate the typed verdict.
		return coretoken.ClaimsValue{}, rerr
	}
	//: everything the domain does not model becomes a private claim.
	return legacyAttachPrivate(registered, members)
}

// legacyDecodeRegistered reads the seven registered claims out of members.
func legacyDecodeRegistered(members map[string]json.RawMessage, shape legacyShape) (claims coretoken.ClaimsValue, err error) {
	//: a registered claim present as JSON null is no value of its type, and
	//: encoding/json would decode it as one anyway — "" for iss, sub and jti,
	//: an audience of [""] for aud, the Unix epoch for exp, nbf and iat — so a
	//: signed token could say "null" and be read as absent, empty or 1970.
	if name, found := legacyNullRegisteredClaim(members); found {
		//: name the claim, which is the domain's vocabulary, never its value.
		return coretoken.ClaimsValue{}, malformed("registered claim " + name + " is null")
	}
	iss, ierr := legacyDecodeStringClaim(members, coretoken.ClaimIssuer)
	//: a non-string "iss" is malformed, not ignorable.
	if ierr != nil {
		//: propagate.
		return coretoken.ClaimsValue{}, ierr
	}
	sub, serr := legacyDecodeStringClaim(members, coretoken.ClaimSubject)
	//: propagate.
	if serr != nil {
		//: a non-string "sub" is malformed.
		return coretoken.ClaimsValue{}, serr
	}
	jti, jerr := legacyDecodeStringClaim(members, coretoken.ClaimID)
	//: propagate.
	if jerr != nil {
		//: a non-string "jti" is malformed.
		return coretoken.ClaimsValue{}, jerr
	}
	audience, aerr := shape.decodeAudience(members[coretoken.ClaimAudience])
	//: propagate the format's own audience rule.
	if aerr != nil {
		//: a shape-illegal "aud" is malformed.
		return coretoken.ClaimsValue{}, aerr
	}
	base := coretoken.NewClaimsValue().WithIssuer(iss).WithSubject(sub).WithID(jti)
	//: the audience setter clones, so passing nil clears the claim.
	return legacyDecodeTimeClaims(base.WithAudience(audience...), members, shape)
}

// legacyNullRegisteredClaim reports the first registered claim members carries as
// JSON null, in RFC 7519 §4.1 order so the refusal is deterministic.
func legacyNullRegisteredClaim(members map[string]json.RawMessage) (name string, found bool) {
	//: the seven names this domain decodes into typed fields.
	for _, claim := range [...]string{
		coretoken.ClaimIssuer, coretoken.ClaimSubject, coretoken.ClaimAudience,
		coretoken.ClaimExpiry, coretoken.ClaimNotBefore, coretoken.ClaimIssuedAt,
		coretoken.ClaimID,
	} {
		//: RawMessage keeps the member's bytes as sent; the decoder already
		//: trimmed the whitespace around them.
		if raw, present := members[claim]; present && bytes.Equal(raw, []byte("null")) {
			//: the first null registered claim.
			return claim, true
		}
	}
	//: none is null.
	return "", false
}

// legacyDecodeTimeClaims reads exp / nbf / iat under shape onto base.
func legacyDecodeTimeClaims(base coretoken.ClaimsValue, members map[string]json.RawMessage, shape legacyShape) (claims coretoken.ClaimsValue, err error) {
	exp, eerr := legacyDecodeTimeClaim(members, coretoken.ClaimExpiry, shape)
	//: a malformed exp is the one claim we must never guess at.
	if eerr != nil {
		//: propagate.
		return coretoken.ClaimsValue{}, eerr
	}
	nbf, nerr := legacyDecodeTimeClaim(members, coretoken.ClaimNotBefore, shape)
	//: propagate.
	if nerr != nil {
		//: a malformed nbf is malformed.
		return coretoken.ClaimsValue{}, nerr
	}
	iat, ierr := legacyDecodeTimeClaim(members, coretoken.ClaimIssuedAt, shape)
	//: propagate.
	if ierr != nil {
		//: a malformed iat is malformed.
		return coretoken.ClaimsValue{}, ierr
	}
	//: all three parsed; the zero Time still means "absent".
	return base.WithExpiry(exp).WithNotBefore(nbf).WithIssuedAt(iat), nil
}

// legacyAttachPrivate copies every non-registered member onto base as a raw private
// claim, in whatever order the map yields — the core value stores them keyed,
// and PrivateNames sorts on the way out.
func legacyAttachPrivate(base coretoken.ClaimsValue, members map[string]json.RawMessage) (claims coretoken.ClaimsValue, err error) {
	claims = base
	//: everything the domain does not model travels through verbatim.
	for name, raw := range members {
		//: the registered claims already have typed homes.
		if coretoken.IsRegisteredClaim(name) {
			continue
		}
		next, aerr := claims.WithPrivateRaw(name, raw)
		//: the core value enforces the name and count rules.
		if aerr != nil {
			//: propagate ClaimNameInvalid / TooLarge unchanged.
			return coretoken.ClaimsValue{}, aerr
		}
		claims = next
	}
	//: the complete claim set.
	return claims, nil
}

// legacyDecodeStringClaim reads a registered string claim, treating absence as "".
func legacyDecodeStringClaim(members map[string]json.RawMessage, name string) (value string, err error) {
	raw, present := members[name]
	//: absence is normal for every registered claim.
	if !present {
		//: the empty string is this domain's "absent".
		return "", nil
	}
	//: present but not a string is malformed — never coerced.
	if json.Unmarshal(raw, &value) != nil {
		//: name the claim, not its contents.
		return "", malformed("registered claim " + name + " is not a string")
	}
	//: the claim as sent.
	return value, nil
}

// decodeTimeClaim reads a registered time claim under shape, treating absence
// as the zero Time.
func legacyDecodeTimeClaim(members map[string]json.RawMessage, name string, shape legacyShape) (instant time.Time, err error) {
	raw, present := members[name]
	//: absence is normal — exp/nbf/iat are all OPTIONAL in RFC 7519 §4.1.
	if !present {
		//: the zero Time is this domain's "absent".
		return time.Time{}, nil
	}
	//: the format decides how a timestamp is spelled.
	instant, err = shape.decodeTime(raw)
	//: propagate the format's own refusal.
	if err != nil {
		//: Malformed, from the shape.
		return time.Time{}, err
	}
	//: a present claim at exactly 0001-01-01T00:00:00Z decodes to the zero
	//: Time, which is this domain's "absent": an exp there would read as no
	//: expiry at all, and a signed token would never expire (#439). Refused
	//: as malformed — no real timestamp sits at that instant.
	if instant.IsZero() {
		//: name the claim, not its value.
		return time.Time{}, malformed("registered claim " + name + " is the zero instant")
	}
	//: a real instant.
	return instant, nil
}

// checkNoDuplicateMembers refuses a top-level JSON object that names the same
// member twice.
//
// encoding/json keeps the LAST occurrence and reports success, so a token with
// two "aud" members says one thing to a Go reader and potentially another to a
// reader written in a language that keeps the first. RFC 8725 §2.6 calls that
// out as a substitution vector; refusing is the only reading that cannot
// disagree with anybody.
func checkNoDuplicateMembers(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	open, terr := dec.Token()
	//: consume the opening brace; a non-object payload is caught here.
	if terr != nil || open != json.Delim('{') {
		//: not a JSON object at all.
		return coretoken.Malformed
	}
	seen := map[string]struct{}{}
	//: walk member names, skipping each value wholesale.
	for dec.More() {
		name, nerr := dec.Token()
		key, isString := name.(string)
		//: a member name is always a string; anything else is malformed.
		if nerr != nil || !isString {
			//: structural failure.
			return coretoken.Malformed
		}
		//: the check this function exists for.
		if _, dup := seen[key]; dup {
			//: refuse; never "keep the last one".
			return coretoken.DuplicateMember
		}
		seen[key] = struct{}{}
		//: skip the value without interpreting it (depth is already bounded).
		if serr := skipValue(dec); serr != nil {
			//: structural failure inside a value.
			return coretoken.Malformed
		}
	}
	//: every member name was distinct.
	return nil
}

// skipValue consumes exactly one JSON value from dec without interpreting it
// and without copying its bytes.
//
// The obvious spelling — decoding into a json.RawMessage — COPIES the whole
// value only for it to be discarded, and this is the duplicate-member pass, so
// every byte copied is waste.
//
// Both implementations are benchmarked side by side over the exact two
// payloads one verification scans (BenchmarkDuplicateScanRealistic); the
// copying one lives in the benchmark file and nothing in production calls it.
// The saving is 919 ns on the header and 3 909 ns on the claims — together
// 16.8 % of one HS256 verification — plus 832 B on a single 4 KiB claim value,
// a gap that grows with the value. See BENCH.md §12.
//
// The OBJECT count is not the reason and must not be quoted as one: on Go
// 1.27, whose encoding/json is backed by json/v2, the copying implementation
// allocates two FEWER objects on a claim set containing an array, because
// json.Decoder.Token boxes every scalar it walks past while Decode takes the
// value in one grab. pkg/v1/security/token/BENCH.md's "two allocations" predates that
// toolchain; the time saving it also reports does reproduce.
func skipValue(dec *json.Decoder) error {
	//: 0 means "no container open yet"; a scalar therefore ends immediately.
	depth := 0
	//: walk tokens until the value this call was asked to skip is fully read.
	for {
		tok, terr := dec.Token()
		//: propagate the decoder's own verdict, including a truncation.
		if terr != nil {
			//: the caller turns this into Malformed.
			return terr
		}
		delim, isDelim := tok.(json.Delim)
		//: a scalar at the top of this value IS the whole value.
		if !isDelim {
			//: inside a container, scalars and member names are just consumed.
			if depth == 0 {
				//: the value was a single token.
				return nil
			}
			//: keep walking the container.
			continue
		}
		//: only the delimiter kind matters; the value itself is discarded.
		switch delim {
		//: an opening delimiter descends one level.
		case '{', '[':
			//: track it so the matching close can be recognised.
			depth++
		//: a closing delimiter ascends one.
		default:
			//: back up one level.
			depth--
			//: reaching zero closes the value this call was asked to skip.
			if depth == 0 {
				//: the container is fully consumed.
				return nil
			}
		}
	}
}

// legacyParseJOSEHeader reads a decoded JOSE header object.
func legacyParseJOSEHeader(raw []byte) (header headerValue, err error) {
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
	alg, aerr := legacyDecodeStringClaim(members, "alg")
	//: a non-string alg is malformed.
	if aerr != nil {
		//: propagate.
		return headerValue{}, aerr
	}
	//: the remaining members this package acts on.
	return legacyFinishHeader(members, alg)
}

// legacyFinishHeader reads the remaining members this package acts on.
func legacyFinishHeader(members map[string]json.RawMessage, alg string) (header headerValue, err error) {
	typ, terr := legacyDecodeStringClaim(members, "typ")
	//: a non-string typ is malformed.
	if terr != nil {
		//: propagate.
		return headerValue{}, terr
	}
	kid, kerr := legacyDecodeStringClaim(members, "kid")
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

// decodeTime reads a NumericDate. It accepts a fractional value because the
// RFC defines one, and keeps it to nanosecond resolution.
func (legacyJose) decodeTime(raw json.RawMessage) (instant time.Time, err error) {
	var seconds float64
	//: a NumericDate is a JSON number; a string here is a PASETO habit.
	if json.Unmarshal(raw, &seconds) != nil {
		//: name the shape, never the value.
		return time.Time{}, malformed("NumericDate claim is not a JSON number")
	}
	//: NaN and infinities compare false against everything, so a token
	//: carrying one would be neither expired nor valid. Refuse it instead.
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || math.Abs(seconds) > maxNumericDate {
		//: out-of-range is malformed, not "very far in the future".
		return time.Time{}, errs.Wrap(coretoken.Malformed, errs.WrapParams{},
			errs.String("claim", "NumericDate out of range"))
	}
	whole, frac := math.Modf(seconds)
	//: seconds + nanoseconds, in UTC so comparisons never depend on a zone.
	return time.Unix(int64(whole), int64(frac*float64(time.Second))).UTC(), nil
}

// decodeAudience accepts the one-string and the array-of-strings forms, and
// nothing else. An empty array decodes to no audience at all.
func (legacyJose) decodeAudience(raw json.RawMessage) (audience []string, err error) {
	//: absence is normal — "aud" is OPTIONAL.
	if len(raw) == 0 {
		//: no audience claim.
		return nil, nil
	}
	var single string
	//: the common single-recipient spelling.
	if json.Unmarshal(raw, &single) == nil {
		//: one audience.
		return []string{single}, nil
	}
	var many []string
	//: the multi-recipient spelling.
	if json.Unmarshal(raw, &many) != nil {
		//: anything else — a number, an object, a mixed array — is malformed.
		return nil, malformed("aud is neither a string nor an array of strings")
	}
	//: the array as sent, order preserved.
	return many, nil
}

// decodeTime reads an RFC 3339 timestamp.
func (legacyPaseto) decodeTime(raw json.RawMessage) (instant time.Time, err error) {
	var text string
	//: PASETO timestamps are JSON strings, not numbers.
	if json.Unmarshal(raw, &text) != nil {
		//: name the shape, never the value.
		return time.Time{}, malformed("PASETO time claim is not a JSON string")
	}
	parsed, perr := time.Parse(time.RFC3339, text)
	//: a non-RFC-3339 timestamp is malformed.
	if perr != nil {
		//: time.ParseError quotes the offending value, which here is a claim
		//: out of an unauthenticated token — so it is not carried.
		return time.Time{}, malformed("PASETO time claim is not RFC 3339")
	}
	//: UTC so comparisons never depend on the sender's zone.
	return parsed.UTC(), nil
}

// decodeAudience accepts a single string only.
func (legacyPaseto) decodeAudience(raw json.RawMessage) (audience []string, err error) {
	//: absence is normal.
	if len(raw) == 0 {
		//: no audience claim.
		return nil, nil
	}
	var single string
	//: PASETO defines "aud" as a string; an array is a JWT habit.
	if json.Unmarshal(raw, &single) != nil {
		//: refuse rather than accept a JWT-shaped aud in a PASETO token.
		return nil, malformed("PASETO aud is not a string")
	}
	//: exactly one audience.
	return []string{single}, nil
}
