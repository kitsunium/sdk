package token

import (
	"encoding/json"
	"time"
	"unicode/utf8"

	coretoken "github.com/kitsunium/sdk/internal/core/security/token"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxClaimMembers is the largest member count a claims object may carry: the
// seven registered names plus the private-claim cap the core value enforces.
const maxClaimMembers int = 7 + coretoken.MaxPrivateClaims

// The registered claims, as slots of registeredValue, in RFC 7519 §4.1 order —
// the order a refusal names the first offender in.
const (
	// slotIssuer is "iss".
	slotIssuer int = iota
	// slotSubject is "sub".
	slotSubject
	// slotAudience is "aud".
	slotAudience
	// slotExpiry is "exp".
	slotExpiry
	// slotNotBefore is "nbf".
	slotNotBefore
	// slotIssuedAt is "iat".
	slotIssuedAt
	// slotID is "jti".
	slotID
	// registeredSlots is how many registered claims there are.
	registeredSlots
)

// registeredNames spells each slot.
var registeredNames = [registeredSlots]string{
	coretoken.ClaimIssuer, coretoken.ClaimSubject, coretoken.ClaimAudience,
	coretoken.ClaimExpiry, coretoken.ClaimNotBefore, coretoken.ClaimIssuedAt,
	coretoken.ClaimID,
}

// claimShape is the per-format encoding of the claim values whose spelling the
// two formats disagree about. Every decode method reads a value the scanner
// already validated as JSON.
type claimShape interface {
	// decodeTime reads one registered time claim.
	decodeTime(raw []byte) (time.Time, error)
	// encodeTime renders one registered time claim.
	encodeTime(instant time.Time) json.RawMessage
	// checkAudience refuses an "aud" value the format cannot carry, without
	// building anything.
	checkAudience(raw []byte) error
	// decodeAudience reads an "aud" value checkAudience accepted into its
	// general slice form; nil raw is no audience.
	decodeAudience(raw []byte) ([]string, error)
	// encodeAudience renders "aud", or nil to omit the member entirely.
	encodeAudience(audience []string) (json.RawMessage, error)
}

// registeredValue is what one claims object says through its registered
// claims: where each one sits in the object index, and the three instants,
// decoded once.
type registeredValue struct {
	// at is each slot's member index, -1 when the claim is absent.
	at [registeredSlots]int
	// expiry / notBefore / issuedAt are the decoded instants, zero when absent.
	expiry, notBefore, issuedAt time.Time
}

// readClaims holds raw to every rule a claims object answers to, and indexes
// it into idx. It is the ONE refusal set: Verify, every format and
// VerifyInto go through it, so no path accepts what another refuses.
//
// The refusals happen in cost order, cheapest first, and in the legacy
// reader's order, so a token refused for two reasons is refused for the same
// one: text that is not UTF-8, nesting depth (two linear scans), then one
// strict RFC 8259 pass that refuses malformed text, a duplicate or escaped
// member name, and a member count past the cap; then the registered claims,
// then the private names.
//
// UTF-8 is checked because a reader that repairs invalid bytes — encoding/json
// replaces each with U+FFFD — would verify "a\xff" and "a\xfe" from a careless
// or hostile issuer as one subject. RFC 8259 §8.1 makes JSON exchanged between
// systems UTF-8, and RFC 8725 §3.7 asks the reader to hold the writer to it.
func readClaims(raw []byte, maxDepth int, shape claimShape, idx *objectIndex) (reg registeredValue, err error) {
	//: two byte strings must never decode to one claim value.
	if !utf8.Valid(raw) {
		//: refused before anything is read, never repaired.
		return registeredValue{}, malformed("claims are not UTF-8")
	}
	//: bound the nesting before the scanner descends a level.
	if derr := checkJSONDepth(raw, maxDepth); derr != nil {
		//: the depth verdict already names the limit.
		return registeredValue{}, derr
	}
	//: one strict pass: grammar, duplicates (RFC 8725 §2.6), the member cap.
	if serr := scanObject(raw, maxDepth, idx); serr != nil {
		//: Malformed, DuplicateMember or TooLarge, unchanged.
		return registeredValue{}, serr
	}
	reg, rerr := readRegistered(raw, idx, shape)
	//: a malformed registered claim stops the read.
	if rerr != nil {
		//: propagate the typed verdict.
		return registeredValue{}, rerr
	}
	//: the private names answer to the core value's rules, checked here so
	//: a path that never builds a ClaimsValue refuses what one would.
	if perr := checkPrivateMembers(raw, idx); perr != nil {
		//: ClaimNameInvalid or TooLarge.
		return registeredValue{}, perr
	}
	//: every rule held.
	return reg, nil
}

// readRegistered locates and judges the seven registered claims.
//
// A registered claim present as JSON null is no value of its type, and a
// lenient reader would decode it as one anyway — "" for iss, sub and jti, an
// audience of [""] for aud, the Unix epoch for exp, nbf and iat — so a signed
// token could say "null" and be read as absent, empty or 1970.
func readRegistered(raw []byte, idx *objectIndex, shape claimShape) (reg registeredValue, err error) {
	reg.at = locateRegistered(raw, idx)
	//: null, then a string claim of another type.
	if kerr := checkRegisteredKinds(idx, &reg.at); kerr != nil {
		//: propagate Malformed.
		return registeredValue{}, kerr
	}
	//: the format's own audience rule.
	if at := reg.at[slotAudience]; at >= 0 {
		//: a shape-illegal "aud" is malformed.
		if aerr := checkAudienceIn(shape, idx.value(raw, at)); aerr != nil {
			//: propagate.
			return registeredValue{}, aerr
		}
	}
	//: the three instants, each the one claim we must never guess at.
	return readInstants(raw, idx, reg, shape)
}

// checkRegisteredKinds refuses a registered claim that is null, then a string
// claim — iss, sub, jti — of any other JSON type.
func checkRegisteredKinds(idx *objectIndex, at *[registeredSlots]int) error {
	//: null first, in RFC 7519 §4.1 order so the refusal is deterministic.
	for slot, member := range at {
		//: a present claim whose value is the null literal.
		if member >= 0 && idx.member[member].kind == kindNull {
			//: name the claim, which is the domain's vocabulary, never its value.
			return malformed("registered claim " + registeredNames[slot] + " is null")
		}
	}
	//: the three string claims: present but not a string is malformed.
	for _, slot := range [...]int{slotIssuer, slotSubject, slotID} {
		//: never coerced.
		if member := at[slot]; member >= 0 && idx.member[member].kind != kindString {
			//: name the claim, not its contents.
			return malformed("registered claim " + registeredNames[slot] + " is not a string")
		}
	}
	//: every kind held.
	return nil
}

// readInstants decodes exp, nbf and iat onto reg.
func readInstants(raw []byte, idx *objectIndex, reg registeredValue, shape claimShape) (registeredValue, error) {
	var err error
	reg.expiry, err = readInstant(raw, idx, reg.at[slotExpiry], coretoken.ClaimExpiry, shape)
	//: a malformed exp is the one claim we must never guess at.
	if err != nil {
		//: propagate.
		return registeredValue{}, err
	}
	reg.notBefore, err = readInstant(raw, idx, reg.at[slotNotBefore], coretoken.ClaimNotBefore, shape)
	//: propagate.
	if err != nil {
		//: a malformed nbf is malformed.
		return registeredValue{}, err
	}
	reg.issuedAt, err = readInstant(raw, idx, reg.at[slotIssuedAt], coretoken.ClaimIssuedAt, shape)
	//: propagate.
	if err != nil {
		//: a malformed iat is malformed.
		return registeredValue{}, err
	}
	//: all three read; the zero Time still means "absent".
	return reg, nil
}

// readInstant decodes the registered time claim at member index at under
// shape, treating absence (at < 0) as the zero Time.
func readInstant(raw []byte, idx *objectIndex, at int, name string, shape claimShape) (instant time.Time, err error) {
	//: absence is normal — exp/nbf/iat are all OPTIONAL in RFC 7519 §4.1.
	if at < 0 {
		//: the zero Time is this domain's "absent".
		return time.Time{}, nil
	}
	//: the format decides how a timestamp is spelled.
	instant, err = decodeTimeIn(shape, idx.value(raw, at))
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

// decodeTimeIn is shape.decodeTime, dispatched over the closed set of shapes.
//
// The switch is the point: a call through the interface makes its argument
// escape, which would move the claims a VerifyInto decodes on its stack onto
// the heap — one allocation per token. A concrete call keeps them where they
// are.
func decodeTimeIn(shape claimShape, raw []byte) (time.Time, error) {
	//: the two formats this package implements.
	switch shape.(type) {
	//: NumericDate.
	case joseShape:
		//: a concrete call.
		return joseShape{}.decodeTime(raw)
	//: RFC 3339.
	case pasetoShape:
		//: a concrete call.
		return pasetoShape{}.decodeTime(raw)
	//: no third format exists.
	default:
		//: refused rather than read under a rule nobody wrote.
		return time.Time{}, malformed("claims of an unknown format")
	}
}

// checkAudienceIn is shape.checkAudience, dispatched as decodeTimeIn is and
// for the same reason.
func checkAudienceIn(shape claimShape, raw []byte) error {
	//: the two formats this package implements.
	switch shape.(type) {
	//: a string or an array of them.
	case joseShape:
		//: a concrete call.
		return joseShape{}.checkAudience(raw)
	//: a string.
	case pasetoShape:
		//: a concrete call.
		return pasetoShape{}.checkAudience(raw)
	//: no third format exists.
	default:
		//: refused rather than read under a rule nobody wrote.
		return malformed("claims of an unknown format")
	}
}

// locateRegistered maps each registered slot to its member index, -1 when
// the claim is absent. Names are unique, so each slot is set at most once.
func locateRegistered(raw []byte, idx *objectIndex) [registeredSlots]int {
	at := [registeredSlots]int{-1, -1, -1, -1, -1, -1, -1}
	//: one pass over the members.
	for i := range idx.count {
		//: a registered member takes its slot.
		if slot := registeredSlot(idx.name(raw, i)); slot >= 0 {
			at[slot] = i
		}
	}
	//: the located claims.
	return at
}

// registeredSlot returns the slot of a registered name, or -1.
func registeredSlot(name []byte) int {
	//: a switch over the seven spellings; the conversion allocates nothing.
	switch string(name) {
	//: iss.
	case coretoken.ClaimIssuer:
		//: its slot.
		return slotIssuer
	//: sub.
	case coretoken.ClaimSubject:
		//: its slot.
		return slotSubject
	//: aud.
	case coretoken.ClaimAudience:
		//: its slot.
		return slotAudience
	//: exp.
	case coretoken.ClaimExpiry:
		//: its slot.
		return slotExpiry
	//: nbf.
	case coretoken.ClaimNotBefore:
		//: its slot.
		return slotNotBefore
	//: iat.
	case coretoken.ClaimIssuedAt:
		//: its slot.
		return slotIssuedAt
	//: jti.
	case coretoken.ClaimID:
		//: its slot.
		return slotID
	//: anything else is private.
	default:
		//: not registered.
		return -1
	}
}

// checkPrivateMembers holds the private members to the rules
// ClaimsValue.WithPrivateRaw enforces — no empty name, at most
// MaxPrivateClaims of them — in member order.
func checkPrivateMembers(raw []byte, idx *objectIndex) error {
	count := 0
	//: every member the domain does not model.
	for i := range idx.count {
		name := idx.name(raw, i)
		//: the registered claims have typed homes.
		if registeredSlot(name) >= 0 {
			continue
		}
		//: an unnamed claim cannot be addressed.
		if len(name) == 0 {
			//: the core value's own verdict.
			return coretoken.ClaimNameInvalid
		}
		count++
		//: the cap the core value enforces.
		if count > coretoken.MaxPrivateClaims {
			//: the core value's own verdict.
			return coretoken.TooLarge
		}
	}
	//: every private name is one the core value accepts.
	return nil
}

// decodeClaims turns a raw claims object into a ClaimsValue under shape: the
// one refusal set of readClaims, then the value, built from the index.
func decodeClaims(raw []byte, maxDepth int, shape claimShape) (claims coretoken.ClaimsValue, err error) {
	var idx objectIndex
	reg, rerr := readClaims(raw, maxDepth, shape, &idx)
	//: refused by a rule.
	if rerr != nil {
		//: the zero value with the verdict.
		return coretoken.ClaimsValue{}, rerr
	}
	audience, aerr := shape.decodeAudience(memberValue(raw, &idx, reg.at[slotAudience]))
	//: checkAudience accepted it already; kept typed anyway.
	if aerr != nil {
		//: propagate.
		return coretoken.ClaimsValue{}, aerr
	}
	claims = coretoken.NewClaimsValue().
		WithIssuer(memberText(raw, &idx, reg.at[slotIssuer])).
		WithSubject(memberText(raw, &idx, reg.at[slotSubject])).
		WithID(memberText(raw, &idx, reg.at[slotID])).
		WithAudience(audience...).
		WithExpiry(reg.expiry).WithNotBefore(reg.notBefore).WithIssuedAt(reg.issuedAt)
	//: everything the domain does not model becomes a private claim.
	return attachPrivate(claims, raw, &idx)
}

// attachPrivate copies every non-registered member onto base as a raw private
// claim, in member order — the core value stores them keyed, and PrivateNames
// sorts on the way out. The bytes are the value exactly as the issuer signed it.
func attachPrivate(base coretoken.ClaimsValue, raw []byte, idx *objectIndex) (claims coretoken.ClaimsValue, err error) {
	claims = base
	//: everything the domain does not model travels through verbatim.
	for i := range idx.count {
		next, aerr := withPrivateMember(claims, raw, idx, i)
		//: checkPrivateMembers already held these rules; kept typed anyway.
		if aerr != nil {
			//: propagate ClaimNameInvalid / TooLarge unchanged.
			return coretoken.ClaimsValue{}, aerr
		}
		claims = next
	}
	//: the complete claim set.
	return claims, nil
}

// withPrivateMember returns claims with member i attached as a raw private
// claim, or claims unchanged when member i is a registered claim.
func withPrivateMember(claims coretoken.ClaimsValue, raw []byte, idx *objectIndex, i int) (coretoken.ClaimsValue, error) {
	name := idx.name(raw, i)
	//: the registered claims already have typed homes.
	if registeredSlot(name) >= 0 {
		//: nothing to attach.
		return claims, nil
	}
	//: the key is the one copy of the name the claim set keeps.
	return claims.WithPrivateRaw(string(name), idx.value(raw, i))
}

// memberValue returns the value of member at, nil when absent.
func memberValue(raw []byte, idx *objectIndex, at int) []byte {
	//: absence is nil, which every shape reads as "no claim".
	if at < 0 {
		//: absent.
		return nil
	}
	//: a view.
	return idx.value(raw, at)
}

// memberText decodes the string member at. An absent member leaves the zero
// string, which is what ClaimsValue's With* methods read as "no claim".
func memberText(raw []byte, idx *objectIndex, at int) string {
	var text string
	//: only a present member has text to decode.
	if at >= 0 {
		text = decodeText(idx.value(raw, at))
	}
	//: the decoded string, or none.
	return text
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
