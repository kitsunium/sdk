// Package token — the copy-on-write setters that build a [ClaimsValue].
package token

import (
	"maps"
	"slices"
	"time"
)

// WithIssuer returns a copy of c carrying iss. The empty string clears it.
func (c ClaimsValue) WithIssuer(iss string) ClaimsValue {
	//: the value receiver already gave us the copy.
	c.issuer = iss
	//: hand back the modified copy, receiver untouched.
	return c
}

// WithSubject returns a copy of c carrying sub. The empty string clears it.
func (c ClaimsValue) WithSubject(sub string) ClaimsValue {
	//: same copy-on-write shape as WithIssuer.
	c.subject = sub
	//: hand back the modified copy.
	return c
}

// WithID returns a copy of c carrying jti. The empty string clears it.
func (c ClaimsValue) WithID(jti string) ClaimsValue {
	//: same copy-on-write shape as WithIssuer.
	c.id = jti
	//: hand back the modified copy.
	return c
}

// WithAudience returns a copy of c whose "aud" claim is exactly aud. Passing no
// argument clears the claim; the slice is copied, so a later mutation of the
// caller's backing array cannot reach the claim set.
func (c ClaimsValue) WithAudience(aud ...string) ClaimsValue {
	//: clone so the claim set does not alias the caller's slice.
	c.audience = slices.Clone(aud)
	//: hand back the modified copy.
	return c
}

// WithExpiry returns a copy of c carrying exp. The zero Time clears the claim,
// which a verifier reads as "no expiry was sent" — not as "expired".
func (c ClaimsValue) WithExpiry(exp time.Time) ClaimsValue {
	//: time.Time is a value; assignment is the copy.
	c.expiry = exp
	//: hand back the modified copy.
	return c
}

// WithNotBefore returns a copy of c carrying nbf. The zero Time clears it.
func (c ClaimsValue) WithNotBefore(nbf time.Time) ClaimsValue {
	//: time.Time is a value; assignment is the copy.
	c.notBefore = nbf
	//: hand back the modified copy.
	return c
}

// WithIssuedAt returns a copy of c carrying iat. The zero Time clears it.
func (c ClaimsValue) WithIssuedAt(iat time.Time) ClaimsValue {
	//: time.Time is a value; assignment is the copy.
	c.issuedAt = iat
	//: hand back the modified copy.
	return c
}

// WithPrivateRaw returns a copy of c carrying the application claim name with
// the raw JSON encoding raw.
//
// It refuses three things rather than accommodating them:
//
//   - an empty name, which no JSON object member can carry meaningfully;
//   - one of the seven registered names ([IsRegisteredClaim]) — a second "exp"
//     that the temporal validator never reads is a claim an application would
//     trust and nothing would enforce;
//   - more than [MaxPrivateClaims] members, so the decode cost of a hostile
//     payload stays bounded.
//
// raw is copied and is NOT validated as JSON here: the format package that
// produced it already parsed it, and re-parsing in the value type would put a
// second, differently-strict JSON reader on the trusted path.
func (c ClaimsValue) WithPrivateRaw(name string, raw []byte) (claims ClaimsValue, err error) {
	//: an unnamed claim cannot be addressed, so it cannot be checked either;
	//: a shadowed registered name would create an unenforced duplicate. Both
	//: are the same refusal, so they are the same guard.
	if name == "" || IsRegisteredClaim(name) {
		//: refuse; a registered claim has a typed setter, and an unnamed one
		//: has no way in at all.
		return ClaimsValue{}, ClaimNameInvalid
	}
	//: adding a NEW name past the cap is the growth this bound exists for;
	//: replacing an existing one does not grow the set.
	if _, exists := c.private[name]; !exists && len(c.private) >= MaxPrivateClaims {
		//: refuse rather than accept an unbounded claim set.
		return ClaimsValue{}, TooLarge
	}
	//: copy-on-write the MAP too — the value receiver copies the header, not
	//: the buckets, so mutating in place would edit every existing copy of c.
	c.private = maps.Clone(c.private)
	//: a nil map cannot be assigned into; allocate on first use.
	if c.private == nil {
		//: exact-size allocation for the single-claim case.
		c.private = make(map[string][]byte, 1)
	}
	//: clone the encoding so the caller cannot mutate a stored claim.
	c.private[name] = slices.Clone(raw)
	//: hand back the modified copy.
	return c, nil
}
