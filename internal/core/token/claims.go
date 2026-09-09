// Package token — the immutable claim set every concrete token format decodes
// into and every issuer renders from.
package token

import (
	"maps"
	"slices"
	"strconv"
	"time"
)

// Registered claim names (RFC 7519 §4.1). PASETO v4 reuses the same seven
// names, with a different value encoding — see internal/service/token.
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

// IsRegisteredClaim reports whether name is one of the seven registered claim
// names this domain models itself.
func IsRegisteredClaim(name string) bool {
	//: seven entries — a linear scan beats a map allocation.
	return slices.Contains(registeredClaims, name)
}

// ClaimsValue is one token's claim set: the seven registered claims as typed
// fields plus any number of application ("private") claims held as their raw
// JSON encoding.
//
// It is an immutable value. Accessors copy every slice and map out, the With*
// methods return a modified copy rather than mutating the receiver, and every
// field is unexported so encoding/json cannot reach the contents by reflection
// — a claim set routinely carries personal data, and the only way it should
// ever be serialised is through a format package that knows what it is doing.
//
// Private claims are RAW JSON, not decoded values: this is the core layer, and
// "what shape is a scope claim" is the caller's question, not the domain's.
// Decode one with pkg/v1/token.PrivateClaim.
type ClaimsValue struct {
	// issuer is the "iss" claim, empty when absent.
	issuer string
	// subject is the "sub" claim, empty when absent.
	subject string
	// id is the "jti" claim, empty when absent.
	id string
	// audience is the "aud" claim. JWT allows one string or an array of them
	// (RFC 7519 §4.1.3), so the general shape is a slice; PASETO allows only
	// one, which its encoder enforces.
	audience []string
	// expiry is the "exp" claim; the zero Time means the claim was absent.
	expiry time.Time
	// notBefore is the "nbf" claim; the zero Time means it was absent.
	notBefore time.Time
	// issuedAt is the "iat" claim; the zero Time means it was absent.
	issuedAt time.Time
	// private maps an application claim name to its raw JSON encoding. Raw,
	// because re-encoding a decoded value would change bytes the issuer signed.
	private map[string][]byte
}

// NewClaimsValue returns an empty claim set to build on. The zero ClaimsValue
// is equally valid and equally empty; this constructor exists so a call site
// reads as a statement of intent rather than a struct literal.
//
// pkg/v1/token re-exports it as the shorter NewClaims, since the Value suffix
// is a core-layer naming rule (KTN-STRUCT-ROLE) and not a consumer's concern.
func NewClaimsValue() ClaimsValue {
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
// (see VerifierConfig.AllowMissingExpiry in internal/service/token).
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

// PrivateRaw returns a copy of the raw JSON encoding of the named application
// claim. The second result reports presence, so a claim explicitly set to JSON
// null is distinguishable from a claim that was never sent.
func (c ClaimsValue) PrivateRaw(name string) (raw []byte, found bool) {
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

// IsZero reports whether c carries no claim at all.
func (c ClaimsValue) IsZero() bool {
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

// GoString implements fmt.GoStringer so %#v stays redacted too — fmt bypasses
// String for Go-syntax formatting and would otherwise dump every field.
func (c ClaimsValue) GoString() string {
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
