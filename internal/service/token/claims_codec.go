// Package token — the claims <-> JSON conversion shared by both formats.
//
// JWT and PASETO agree on the seven registered claim NAMES and disagree on
// their value encodings: JWT writes NumericDate (RFC 7519 §2) and allows "aud"
// to be a string or an array of strings; PASETO writes RFC 3339 timestamps and
// allows exactly one audience. So the traversal is written once and the two
// encodings are a [claimShape] each — rather than two claim decoders that
// would drift apart on everything except the part that actually differs.
package token

import (
	"bytes"
	"encoding/json"
	"time"
	"unicode/utf8"

	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxClaimMembers is the largest member count a claims object may carry: the
// seven registered names plus the private-claim cap the core value enforces.
const maxClaimMembers int = 7 + coretoken.MaxPrivateClaims

// claimShape is the per-format encoding of the claim values whose spelling the
// two formats disagree about.
type claimShape interface {
	// decodeTime reads one registered time claim.
	decodeTime(raw json.RawMessage) (time.Time, error)
	// encodeTime renders one registered time claim.
	encodeTime(instant time.Time) json.RawMessage
	// decodeAudience reads the "aud" claim into its general slice form.
	decodeAudience(raw json.RawMessage) ([]string, error)
	// encodeAudience renders "aud", or nil to omit the member entirely.
	encodeAudience(audience []string) (json.RawMessage, error)
}

// decodeClaims turns a raw claims object into a ClaimsValue under shape.
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
func decodeClaims(raw []byte, maxDepth int, shape claimShape) (claims coretoken.ClaimsValue, err error) {
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
	registered, rerr := decodeRegistered(members, shape)
	//: a malformed registered claim stops the decode.
	if rerr != nil {
		//: propagate the typed verdict.
		return coretoken.ClaimsValue{}, rerr
	}
	//: everything the domain does not model becomes a private claim.
	return attachPrivate(registered, members)
}

// decodeRegistered reads the seven registered claims out of members.
func decodeRegistered(members map[string]json.RawMessage, shape claimShape) (claims coretoken.ClaimsValue, err error) {
	//: a registered claim present as JSON null is no value of its type, and
	//: encoding/json would decode it as one anyway — "" for iss, sub and jti,
	//: an audience of [""] for aud, the Unix epoch for exp, nbf and iat — so a
	//: signed token could say "null" and be read as absent, empty or 1970.
	if name, found := nullRegisteredClaim(members); found {
		//: name the claim, which is the domain's vocabulary, never its value.
		return coretoken.ClaimsValue{}, malformed("registered claim " + name + " is null")
	}
	iss, ierr := decodeStringClaim(members, coretoken.ClaimIssuer)
	//: a non-string "iss" is malformed, not ignorable.
	if ierr != nil {
		//: propagate.
		return coretoken.ClaimsValue{}, ierr
	}
	sub, serr := decodeStringClaim(members, coretoken.ClaimSubject)
	//: propagate.
	if serr != nil {
		//: a non-string "sub" is malformed.
		return coretoken.ClaimsValue{}, serr
	}
	jti, jerr := decodeStringClaim(members, coretoken.ClaimID)
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
	return decodeTimeClaims(base.WithAudience(audience...), members, shape)
}

// nullRegisteredClaim reports the first registered claim members carries as
// JSON null, in RFC 7519 §4.1 order so the refusal is deterministic.
func nullRegisteredClaim(members map[string]json.RawMessage) (name string, found bool) {
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

// decodeTimeClaims reads exp / nbf / iat under shape onto base.
func decodeTimeClaims(base coretoken.ClaimsValue, members map[string]json.RawMessage, shape claimShape) (claims coretoken.ClaimsValue, err error) {
	exp, eerr := decodeTimeClaim(members, coretoken.ClaimExpiry, shape)
	//: a malformed exp is the one claim we must never guess at.
	if eerr != nil {
		//: propagate.
		return coretoken.ClaimsValue{}, eerr
	}
	nbf, nerr := decodeTimeClaim(members, coretoken.ClaimNotBefore, shape)
	//: propagate.
	if nerr != nil {
		//: a malformed nbf is malformed.
		return coretoken.ClaimsValue{}, nerr
	}
	iat, ierr := decodeTimeClaim(members, coretoken.ClaimIssuedAt, shape)
	//: propagate.
	if ierr != nil {
		//: a malformed iat is malformed.
		return coretoken.ClaimsValue{}, ierr
	}
	//: all three parsed; the zero Time still means "absent".
	return base.WithExpiry(exp).WithNotBefore(nbf).WithIssuedAt(iat), nil
}

// attachPrivate copies every non-registered member onto base as a raw private
// claim, in whatever order the map yields — the core value stores them keyed,
// and PrivateNames sorts on the way out.
func attachPrivate(base coretoken.ClaimsValue, members map[string]json.RawMessage) (claims coretoken.ClaimsValue, err error) {
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

// decodeStringClaim reads a registered string claim, treating absence as "".
func decodeStringClaim(members map[string]json.RawMessage, name string) (value string, err error) {
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
func decodeTimeClaim(members map[string]json.RawMessage, name string, shape claimShape) (instant time.Time, err error) {
	raw, present := members[name]
	//: absence is normal — exp/nbf/iat are all OPTIONAL in RFC 7519 §4.1.
	if !present {
		//: the zero Time is this domain's "absent".
		return time.Time{}, nil
	}
	//: the format decides how a timestamp is spelled.
	return shape.decodeTime(raw)
}

// encodeClaims renders claims under shape. The member map marshals with sorted
// keys, so the same claim set always produces the same bytes — which is what
// makes an issued token reproducible in a test.
//
// Every string it writes must be valid UTF-8, and a claim set carrying one that
// is not is refused rather than minted: see checkClaimText for why nothing
// further down the line would refuse it instead.
func encodeClaims(claims coretoken.ClaimsValue, shape claimShape) (raw []byte, err error) {
	audience := claims.Audience()
	//: refuse before anything is rendered, so a refusal costs no encoding.
	if terr := checkClaimText(claims, audience); terr != nil {
		//: propagate IssueFailed.
		return nil, terr
	}
	members := map[string]json.RawMessage{}
	//: the three string claims, omitted when empty.
	putString(members, coretoken.ClaimIssuer, claims.Issuer())
	putString(members, coretoken.ClaimSubject, claims.Subject())
	putString(members, coretoken.ClaimID, claims.ID())
	//: the three time claims, omitted when zero.
	putTime(members, coretoken.ClaimExpiry, claims.Expiry(), shape)
	putTime(members, coretoken.ClaimNotBefore, claims.NotBefore(), shape)
	putTime(members, coretoken.ClaimIssuedAt, claims.IssuedAt(), shape)
	encodedAudience, aerr := shape.encodeAudience(audience)
	//: a format that cannot express this audience refuses to mint at all.
	if aerr != nil {
		//: propagate IssueFailed.
		return nil, aerr
	}
	//: nil means "omit the member".
	if encodedAudience != nil {
		members[coretoken.ClaimAudience] = encodedAudience
	}
	//: private claims travel as the exact bytes they arrived as.
	if perr := putPrivate(members, claims); perr != nil {
		//: propagate IssueFailed.
		return nil, perr
	}
	//: sorted-key marshal of a validated member set.
	payload, merr := json.Marshal(members)
	//: unreachable once putPrivate has held every raw value to JSON, and kept
	//: typed anyway: rule 2 admits no untyped error out of this package.
	if merr != nil {
		//: IssueFailed, origin wins; the stdlib's text travels as a field.
		return nil, errs.Wrap(coretoken.IssueFailed, errs.WrapParams{},
			errs.String("claim", "encoding/json refused the claims object: "+merr.Error()))
	}
	//: the claims object, ready to sign.
	return payload, nil
}

// checkClaimText refuses a claim set whose registered string claims are not
// valid UTF-8.
//
// JSON text is UTF-8 (RFC 8259 §8.1, RFC 8725 §3.7), and on the ISSUE path
// nothing else would say so. quoteJSONString passes every byte at or above 0x20
// through by design, and a reader that does not refuse the result repairs it
// instead: encoding/json — this package's own verifier included — decodes each
// invalid byte to U+FFFD, so a "sub" of "a\xff" and one of "a\xfe" are minted
// as two tokens and verified as ONE subject. A strict JOSE reader elsewhere
// rejects both. Nothing is repaired here either: which bytes the caller meant
// is not something this package can know.
func checkClaimText(claims coretoken.ClaimsValue, audience []string) error {
	names := [...]string{coretoken.ClaimIssuer, coretoken.ClaimSubject, coretoken.ClaimID}
	values := [...]string{claims.Issuer(), claims.Subject(), claims.ID()}
	//: the three single-valued string claims, in the order they render.
	for i, value := range values {
		//: an empty claim is absent and trivially valid.
		if !utf8.ValidString(value) {
			//: name the claim, never its value.
			return textNotUTF8(names[i])
		}
	}
	//: every audience: one bad member spoils the whole claim.
	for _, member := range audience {
		//: the same rule, per member.
		if !utf8.ValidString(member) {
			//: name the claim, never the member.
			return textNotUTF8(coretoken.ClaimAudience)
		}
	}
	//: every registered string is text JSON can carry as itself.
	return nil
}

// putPrivate stores every private claim as the exact bytes it arrived as,
// refusing one whose name or bytes are not valid UTF-8.
//
// Both halves need the check, and for opposite reasons: json.Marshal REWRITES a
// map key that is not UTF-8 — each bad byte becomes U+FFFD, so two names the
// caller kept apart would be minted as one — while it passes a RawMessage's
// bytes through untouched, so a bad value would reach the wire as written.
func putPrivate(members map[string]json.RawMessage, claims coretoken.ClaimsValue) error {
	//: PrivateNames is sorted, so which claim refuses first is deterministic.
	for _, name := range claims.PrivateNames() {
		value, _ := claims.PrivateRaw(name)
		//: refuse rather than let the encoder repair one half and not the other.
		if !utf8.ValidString(name) || !utf8.Valid(value) {
			//: the name is the caller's vocabulary, so it is not echoed either.
			return textNotUTF8("a private claim")
		}
		//: WithPrivateRaw copies its bytes without parsing them, so this is the
		//: first reader that can hold them to JSON; json.Marshal would refuse
		//: them anyway, with an untyped *json.MarshalerError.
		if !json.Valid(value) {
			//: IssueFailed naming the problem, never the claim.
			return errs.Wrap(coretoken.IssueFailed, errs.WrapParams{},
				errs.String("claim", "a private claim is not valid JSON"))
		}
		members[name] = value
	}
	//: every private claim stored.
	return nil
}

// textNotUTF8 returns the IssueFailed verdict for a claim whose text JSON
// cannot carry. claim is a fixed description — never a value, never a name the
// caller chose.
func textNotUTF8(claim string) error {
	//: origin-wins on the sentinel keeps code, reason, public and exit code.
	return errs.Wrap(coretoken.IssueFailed, errs.WrapParams{},
		errs.String("claim", claim+" is not valid UTF-8"))
}

// putString stores a registered string claim unless it is empty.
func putString(members map[string]json.RawMessage, name, value string) {
	//: an empty registered claim is an absent one, never `"": ""`.
	if value == "" {
		//: nothing to store.
		return
	}
	//: rendered rather than reflected, so there is no error to discard.
	members[name] = json.RawMessage(quoteJSONString(value))
}

// putTime stores a registered time claim unless it is the zero Time.
func putTime(members map[string]json.RawMessage, name string, instant time.Time, shape claimShape) {
	//: the zero Time is this domain's "absent".
	if instant.IsZero() {
		//: nothing to store.
		return
	}
	//: the format decides the spelling.
	members[name] = shape.encodeTime(instant)
}

// malformed returns the Malformed verdict carrying a structural detail.
//
// It deliberately does NOT attach the stdlib parse error as a cause, which is
// the opposite of this repository's usual wrapping habit. The habit exists so
// `errors.Is(err, originalCause)` keeps working — but here the "original
// cause" was produced by parsing ATTACKER-CONTROLLED bytes, and stdlib parse
// errors quote what they choked on: time.ParseError echoes the timestamp,
// json.SyntaxError gives an offset into the payload, base64.CorruptInputError
// a position in the token. Keeping any of them reachable would put token
// content one errors.Unwrap away from every log line that walks a chain.
//
// The second effect is that the verdict keeps the sentinel's own HTTP status
// and exit code: Wrap's origin-wins path inherits both, while the
// stdlib-cause path would silently fall back to 500 / EX_SOFTWARE.
//
// detail is a fixed string chosen at the call site — never a value out of the
// token.
func malformed(detail string) error {
	//: origin-wins on the sentinel keeps code, reason, public, status and exit.
	return errs.Wrap(coretoken.Malformed, errs.WrapParams{},
		errs.String("detail", detail))
}
