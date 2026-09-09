// Package jwk — the encode half, and the place the package's one real security
// decision lives.
//
// Serialising a key is the operation that undoes core/crypto.Key's redaction:
// it turns protected material into JSON somebody will write to a file, a
// config map, or an HTTP response. So the two directions are not a boolean
// argument — they are two differently named methods, and the one the language
// reaches for on its own (MarshalJSON, i.e. plain json.Marshal) is the safe
// one. Emitting "d" or "k" requires typing MarshalPrivate at the call site,
// where a reviewer reads it.
package jwk

import (
	"crypto/sha256"
	"encoding/json"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// MarshalPublic renders the public JWK: "kty", "crv", the public members, and
// whatever metadata the key carries. It never emits "d" or "k".
//
// A symmetric key is refused with NoPublicForm rather than rendered without its
// "k": an oct JWK IS its secret, so there is no public projection of one, and
// handing back a key-shaped object with no key would be worse than an error.
func (k KeyValue) MarshalPublic() (document []byte, err error) {
	//: build the public member set, refusing anything that has none.
	raw, werr := k.publicWire()
	//: the key has no publishable member set.
	if werr != nil {
		//: already typed — MissingMember or NoPublicForm.
		return nil, werr
	}
	//: one encode path for both directions.
	return encodeWire(raw)
}

// MarshalPrivate renders the full JWK, private member included: "d" for EC and
// OKP, "k" for oct. It is the ONLY method that emits key material, and it is
// named so the call site says what it does.
//
// A key holding no private material is refused with NoPrivateMaterial rather
// than silently downgraded to MarshalPublic's output — a caller that asked for
// a private JWK and received a public one would ship a broken key store and
// find out at first use.
func (k KeyValue) MarshalPrivate() (document []byte, err error) {
	//: build the full member set, refusing a public-only key.
	raw, werr := k.privateWire()
	//: the key has no exportable member set.
	if werr != nil {
		//: already typed — MissingMember or NoPrivateMaterial.
		return nil, werr
	}
	//: same encode path; only the member set differs.
	return encodeWire(raw)
}

// MarshalJSON implements json.Marshaler by delegating to MarshalPublic, so the
// path taken by plain json.Marshal, by a struct that embeds a KeyValue, and by
// any generic encoder is the public one. There is no flag, no option struct and
// no context value that flips it — the private path has its own name.
func (k KeyValue) MarshalJSON() (document []byte, err error) {
	//: the default must not be the dangerous one (ADR 0030).
	return k.MarshalPublic()
}

// publicWire assembles the public member set for k.
func (k KeyValue) publicWire() (raw keyJSON, err error) {
	//: a zero KeyValue has no kty and therefore no serialisation.
	if k.IsZero() {
		//: nothing to render.
		return keyJSON{}, MissingMember
	}
	//: the refusal this package exists to make explicit.
	if k.kty == TypeOct {
		//: "k" is the secret; there is no public half to emit.
		return keyJSON{}, NoPublicForm
	}
	//: every asymmetric family carries at least "x".
	if len(k.x) == 0 {
		//: a key with no public member cannot be published.
		return keyJSON{}, MissingMember
	}
	raw = keyJSON{
		Kty: string(k.kty), Crv: string(k.crv), Kid: k.kid,
		Use: k.use, Alg: k.alg, KeyOps: k.keyOps,
		X: b64Encode(k.x),
	}
	//: only EC carries a second coordinate; OKP stops at "x".
	if k.kty == TypeEC {
		//: "y" is required for an EC JWK.
		raw.Y = b64Encode(k.y)
	}
	//: the publishable member set.
	return raw, nil
}

// privateWire assembles the full member set for k, private member included.
func (k KeyValue) privateWire() (raw keyJSON, err error) {
	//: a zero KeyValue has no kty and therefore no serialisation.
	if k.IsZero() {
		//: nothing to render.
		return keyJSON{}, MissingMember
	}
	//: refuse rather than quietly emit a public JWK from a private call.
	if !k.IsPrivate() {
		//: the caller asked for material this key does not hold.
		return keyJSON{}, NoPrivateMaterial
	}
	//: a symmetric key has no public members to build on.
	if k.kty == TypeOct {
		//: "k" carries the whole key; metadata rides alongside.
		return keyJSON{
			Kty: string(k.kty), Kid: k.kid, Use: k.use,
			Alg: k.alg, KeyOps: k.keyOps, K: b64Encode(k.priv),
		}, nil
	}
	//: EC and OKP extend their public form with "d".
	raw, werr := k.publicWire()
	//: an EC/OKP key with no public members cannot carry a "d" either.
	if werr != nil {
		//: already typed.
		return keyJSON{}, werr
	}
	raw.D = b64Encode(k.priv)
	//: the full member set.
	return raw, nil
}

// encodeWire renders a keyJSON. The struct holds only strings and a []string,
// so encoding/json cannot fail here; the error is still surfaced typed rather
// than dropped, because a silently ignored error is how an impossible case
// becomes a debugging session.
func encodeWire(raw keyJSON) (document []byte, err error) {
	//: deterministic member order comes from keyJSON's field order.
	out, merr := json.Marshal(raw)
	//: defensive — unreachable for this struct shape.
	if merr != nil {
		//: keep the cause; there is no material in a JSON encoder error.
		return nil, errs.Wrap(merr, errs.WrapParams{
			Code:    CodeJWKMalformed,
			Reason:  "MALFORMED",
			Public:  "JWK document is malformed",
			Private: "service/crypto/jwk: json.Marshal failed on the JWK wire struct",
		})
	}
	//: the rendered JWK.
	return out, nil
}

// Thumbprint returns the RFC 7638 JWK thumbprint of k: the base64url SHA-256 of
// the canonical JSON built from the REQUIRED members of the key type only, in
// lexicographic order. It is the standard way to mint a "kid" that is stable
// across publishers and distinct per key — which is what keeps Set.ByKid from
// hitting the AmbiguousKid refusal.
//
// For a symmetric key the input includes "k". The digest is one-way, but RFC
// 7638 §3.2.1 is explicit that a LOW-ENTROPY secret can be recovered from its
// thumbprint by brute force; the SDK's crypto.Key is 32 random octets, so the
// warning binds only to keys built from elsewhere.
func (k KeyValue) Thumbprint() (thumb string, err error) {
	//: canonical form first — the hash is over exact octets.
	canonical, cerr := k.thumbprintInput()
	//: no canonical form, so no thumbprint.
	if cerr != nil {
		//: already typed.
		return "", cerr
	}
	//: RFC 7638 §3.4 fixes SHA-256 and base64url.
	sum := sha256.Sum256(canonical)
	//: the thumbprint, ready to serve as a kid.
	return b64Encode(sum[:]), nil
}

// WithThumbprintKid returns a copy of k whose "kid" is its RFC 7638
// thumbprint — a deterministic, collision-resistant id derived from the key
// itself rather than from a counter somebody has to remember to bump.
func (k KeyValue) WithThumbprintKid() (identified KeyValue, err error) {
	//: derive from the material, not from the existing kid.
	thumb, terr := k.Thumbprint()
	//: no thumbprint, so no derived kid.
	if terr != nil {
		//: already typed.
		return KeyValue{}, terr
	}
	//: the copy carries the derived id.
	return k.WithKid(thumb), nil
}

// thumbprintInput builds the RFC 7638 §3.2 canonical JSON: required members
// only, lexicographic order, no whitespace, no optional members.
//
// It is assembled by concatenation rather than through encoding/json because
// the canonical form is order-sensitive and Go's map ordering is not. Every
// interpolated value is closed-vocabulary — a validated Curve name or base64url
// output — so none of them can contain a character JSON would need to escape.
func (k KeyValue) thumbprintInput() (canonical []byte, err error) {
	//: the zero KeyValue is rejected the same way every marshaller rejects it.
	if k.IsZero() {
		//: no kty, no required-member list, no thumbprint.
		return nil, MissingMember
	}
	//: each family has its own required-member list (RFC 7638 §3.2).
	switch k.kty {
	//: crv, kty, x, y — in that lexicographic order.
	case TypeEC:
		//: the EC canonical form.
		return k.thumbprintEC()
	//: crv, kty, x (RFC 8037 §2).
	case TypeOKP:
		//: the OKP canonical form.
		return k.thumbprintOKP()
	//: k, kty — the secret itself is the required member.
	case TypeOct:
		//: the oct canonical form.
		return k.thumbprintOct()
	//: any unmodelled family.
	default:
		//: no required-member list is defined for this family.
		return nil, UnsupportedKeyType
	}
}

// thumbprintEC builds the canonical form of an EC key.
func (k KeyValue) thumbprintEC() (canonical []byte, err error) {
	//: an incomplete key has no thumbprint to be identified by.
	if len(k.x) == 0 || len(k.y) == 0 {
		//: nothing to hash.
		return nil, MissingMember
	}
	//: members in lexicographic order, no whitespace.
	return []byte(`{"crv":"` + string(k.crv) + `","kty":"EC","x":"` +
		b64Encode(k.x) + `","y":"` + b64Encode(k.y) + `"}`), nil
}

// thumbprintOKP builds the canonical form of an OKP key.
func (k KeyValue) thumbprintOKP() (canonical []byte, err error) {
	//: an incomplete key has no thumbprint to be identified by.
	if len(k.x) == 0 {
		//: nothing to hash.
		return nil, MissingMember
	}
	//: members in lexicographic order, no whitespace.
	return []byte(`{"crv":"` + string(k.crv) + `","kty":"OKP","x":"` +
		b64Encode(k.x) + `"}`), nil
}

// thumbprintOct builds the canonical form of a symmetric key.
func (k KeyValue) thumbprintOct() (canonical []byte, err error) {
	//: an oct key without "k" holds nothing to hash.
	if !k.IsPrivate() {
		//: nothing to hash.
		return nil, NoPrivateMaterial
	}
	//: members in lexicographic order, no whitespace.
	return []byte(`{"k":"` + b64Encode(k.priv) + `","kty":"oct"}`), nil
}
