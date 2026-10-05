package token

import (
	"maps"
	"slices"
	"strconv"
	"time"
)

// Registered claim names (RFC 7519 §4.1). PASETO v4 reuses the same seven
// names, with a different value encoding — see internal/service/security/token.
const (
	// ClaimIssuer is "iss" — who minted the token.
	ClaimIssuer string = "iss"
	// ClaimSubject is "sub" — who the token is about.
	ClaimSubject string = "sub"
	// ClaimAudience is "aud" — who the token is FOR. Checked, never assumed.
	ClaimAudience string = "aud"
	// ClaimExpiry is "exp" — the instant after which the token is dead.
	ClaimExpiry string = "exp"
	// ClaimNotBefore is "nbf" — the instant before which it is not yet alive.
	ClaimNotBefore string = "nbf"
	// ClaimIssuedAt is "iat" — when it was minted.
	ClaimIssuedAt string = "iat"
	// ClaimID is "jti" — a unique id for the token.
	ClaimID string = "jti"
)

// MaxPrivateClaims caps how many application claims one token may carry. The
// number is a bound, not a budget: a claim set is an authorisation statement,
// and a token that needs more than this many members is describing a database
// row rather than a subject. The cap keeps a hostile payload's decode cost
// linear and bounded, which is the same reason the wire size is capped.
const MaxPrivateClaims int = 64

// claimSlots is how many registered claims RFC 7519 §4.1 defines, and hence
// the width of the presence vector registeredCount folds.
const claimSlots int = 7

// registeredClaims is the closed set of names this domain owns. A private
// claim may not use one of them, because a second spelling of "exp" that the
// validator never reads is a claim an application would trust and nothing
// would enforce.
var registeredClaims = []string{
	ClaimIssuer, ClaimSubject, ClaimAudience,
	ClaimExpiry, ClaimNotBefore, ClaimIssuedAt, ClaimID,
}

// isRegisteredClaim is IsRegisteredClaim's body: decl_gen.go writes IsRegisteredClaim, from the
// design, as one call of it.
func isRegisteredClaim(name string) bool {
	//: seven entries — a linear scan beats a map allocation.
	return slices.Contains(registeredClaims, name)
}

// newClaimsValue is NewClaimsValue's body: decl_gen.go writes NewClaimsValue, from the
// design, as one call of it.
func newClaimsValue() ClaimsValue {
	//: every field's zero value already means "claim absent".
	return ClaimsValue{}
}

// Issuer reports the "iss" claim, empty when the token carried none.
func (c ClaimsValue) Issuer() string {
	//: direct read of the immutable member.
	return c.issuer
}

// Subject reports the "sub" claim, empty when the token carried none.
func (c ClaimsValue) Subject() string {
	//: direct read of the immutable member.
	return c.subject
}

// ID reports the "jti" claim, empty when the token carried none.
func (c ClaimsValue) ID() string {
	//: direct read of the immutable member.
	return c.id
}

// Audience returns a copy of the "aud" claim, nil when the token carried none.
func (c ClaimsValue) Audience() []string {
	//: clone so a caller cannot reach back into the claim set.
	return slices.Clone(c.audience)
}

// Expiry reports the "exp" claim. The zero Time means the claim was ABSENT,
// never "expired at the epoch" — a verifier decides what to do about absence
// (see VerifierConfig.AllowMissingExpiry in internal/service/security/token).
func (c ClaimsValue) Expiry() time.Time {
	//: time.Time is a value; no aliasing to guard against.
	return c.expiry
}

// NotBefore reports the "nbf" claim; the zero Time means it was absent.
func (c ClaimsValue) NotBefore() time.Time {
	//: time.Time is a value; no aliasing to guard against.
	return c.notBefore
}

// IssuedAt reports the "iat" claim; the zero Time means it was absent.
func (c ClaimsValue) IssuedAt() time.Time {
	//: time.Time is a value; no aliasing to guard against.
	return c.issuedAt
}

// PrivateNames returns the application claim names, sorted, so a caller
// enumerating them gets a stable order rather than Go's randomised map order.
func (c ClaimsValue) PrivateNames() []string {
	//: sorted keys — deterministic output for tests and for logs of SHAPE.
	return slices.Sorted(maps.Keys(c.private))
}

// privateRaw is ClaimsValue.PrivateRaw's body: decl_gen.go writes ClaimsValue.PrivateRaw, from the
// design, as one call of it.
func (c ClaimsValue) privateRaw(name string) (raw []byte, found bool) {
	//: map lookup on the raw encodings.
	stored, ok := c.private[name]
	//: absence is a normal answer, not an error.
	if !ok {
		//: nothing to copy.
		return nil, false
	}
	//: clone so a caller cannot mutate the claim set through the slice.
	return slices.Clone(stored), true
}

// isZero is ClaimsValue.IsZero's body: decl_gen.go writes ClaimsValue.IsZero, from the
// design, as one call of it.
func (c ClaimsValue) isZero() bool {
	//: an empty claim set has no registered claim and no private one.
	return c.issuer == "" && c.subject == "" && c.id == "" &&
		len(c.audience) == 0 && len(c.private) == 0 &&
		c.expiry.IsZero() && c.notBefore.IsZero() && c.issuedAt.IsZero()
}

// String implements fmt.Stringer and renders the SHAPE of the claim set, never
// a value: "token.Claims{registered:3 aud:1 private:2}".
//
// A claim set is the output of an authentication decision and routinely holds a
// subject id, an email, a tenant, a scope list. Rendering any of it would put
// that in whichever log line happened to use %v — which is exactly how claim
// contents end up in a log aggregator that was never scoped to hold them. The
// counts are enough to tell "the token had no audience" from "the audience did
// not match", which is what a %v is usually reaching for.
func (c ClaimsValue) String() string {
	//: count the registered claims that are present, without naming any.
	return "token.Claims{registered:" + strconv.Itoa(c.registeredCount()) +
		" aud:" + strconv.Itoa(len(c.audience)) +
		" private:" + strconv.Itoa(len(c.private)) + "}"
}

// goString is ClaimsValue.GoString's body: decl_gen.go writes ClaimsValue.GoString, from the
// design, as one call of it.
func (c ClaimsValue) goString() string {
	//: same shape-only rendering as String.
	return c.String()
}

// registeredCount reports how many of the seven registered claims are present.
func (c ClaimsValue) registeredCount() int {
	present := [claimSlots]bool{
		c.issuer != "", c.subject != "", c.id != "", len(c.audience) != 0,
		!c.expiry.IsZero(), !c.notBefore.IsZero(), !c.issuedAt.IsZero(),
	}
	count := 0
	//: count set members without naming any of them.
	for _, set := range present {
		//: one more registered claim on the wire.
		if set {
			count++
		}
	}
	//: total present, names withheld.
	return count
}
