package token

import (
	"maps"
	"slices"
	"time"
)

// withIssuer is ClaimsValue.WithIssuer's body: decl_gen.go writes ClaimsValue.WithIssuer, from the
// design, as one call of it.
func (c ClaimsValue) withIssuer(iss string) ClaimsValue {
	//: the value receiver already gave us the copy.
	c.issuer = iss
	//: hand back the modified copy, receiver untouched.
	return c
}

// withSubject is ClaimsValue.WithSubject's body: decl_gen.go writes ClaimsValue.WithSubject, from the
// design, as one call of it.
func (c ClaimsValue) withSubject(sub string) ClaimsValue {
	//: same copy-on-write shape as WithIssuer.
	c.subject = sub
	//: hand back the modified copy.
	return c
}

// withID is ClaimsValue.WithID's body: decl_gen.go writes ClaimsValue.WithID, from the
// design, as one call of it.
func (c ClaimsValue) withID(jti string) ClaimsValue {
	//: same copy-on-write shape as WithIssuer.
	c.id = jti
	//: hand back the modified copy.
	return c
}

// withAudience is ClaimsValue.WithAudience's body: decl_gen.go writes ClaimsValue.WithAudience, from the
// design, as one call of it.
func (c ClaimsValue) withAudience(aud ...string) ClaimsValue {
	//: clone so the claim set does not alias the caller's slice.
	c.audience = slices.Clone(aud)
	//: hand back the modified copy.
	return c
}

// withExpiry is ClaimsValue.WithExpiry's body: decl_gen.go writes ClaimsValue.WithExpiry, from the
// design, as one call of it.
func (c ClaimsValue) withExpiry(exp time.Time) ClaimsValue {
	//: time.Time is a value; assignment is the copy.
	c.expiry = exp
	//: hand back the modified copy.
	return c
}

// withNotBefore is ClaimsValue.WithNotBefore's body: decl_gen.go writes ClaimsValue.WithNotBefore, from the
// design, as one call of it.
func (c ClaimsValue) withNotBefore(nbf time.Time) ClaimsValue {
	//: time.Time is a value; assignment is the copy.
	c.notBefore = nbf
	//: hand back the modified copy.
	return c
}

// withIssuedAt is ClaimsValue.WithIssuedAt's body: decl_gen.go writes ClaimsValue.WithIssuedAt, from the
// design, as one call of it.
func (c ClaimsValue) withIssuedAt(iat time.Time) ClaimsValue {
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
