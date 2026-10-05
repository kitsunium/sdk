package jwk

import (
	"bytes"
	"slices"
	"strconv"

	corejwk "github.com/kitsunium/sdk/internal/core/crypto/key/jwk"
)

// ed25519KeyLen is the octet length of an Ed25519 public key and of the seed
// RFC 8037 §2 puts in "d" — both 32, unlike crypto/ed25519's 64-octet private
// key, which is seed||public.
const ed25519KeyLen int = 32

const (
	// TypeEC is an elliptic-curve key over a NIST prime curve (RFC 7518 §6.2):
	// public members "x"/"y", private member "d".
	TypeEC Type = "EC"
	// TypeOKP is an octet key pair — an Edwards-curve key (RFC 8037 §2):
	// public member "x", private member "d" (the 32-octet seed).
	TypeOKP Type = "OKP"
	// TypeOct is a symmetric key carried whole in "k" (RFC 7518 §6.4). It has
	// no public half, which is why MarshalPublic refuses it.
	TypeOct Type = "oct"
)

const (
	// CurveP256 is NIST P-256, the curve behind JOSE "ES256" and the SDK's
	// registered "ecdsa-p256" signer.
	CurveP256 Curve = "P-256"
	// CurveP384 is NIST P-384 (JOSE "ES384"). Representable here, but the SDK
	// registers no signer for it — see the package CLAUDE.md.
	CurveP384 Curve = "P-384"
	// CurveP521 is NIST P-521 (JOSE "ES512"); field elements are 66 octets, not
	// 65, because 521 bits round up to 66 whole octets.
	CurveP521 Curve = "P-521"
	// CurveEd25519 is the Edwards curve behind JOSE "EdDSA" (RFC 8037).
	CurveEd25519 Curve = "Ed25519"
)

// Kty reports the key's "kty" member. The zero KeyValue reports the empty Type.
func (k KeyValue) Kty() Type {
	//: direct read of the immutable member.
	return k.kty
}

// Crv reports the key's "crv" member, empty for a symmetric key.
func (k KeyValue) Crv() Curve {
	//: direct read of the immutable member.
	return k.crv
}

// Kid reports the key's "kid" member, empty when the key carries none. A kid is
// a selection hint chosen by whoever published the key — never trust it as
// proof of anything.
func (k KeyValue) Kid() string {
	//: direct read of the immutable member.
	return k.kid
}

// Use reports the key's "use" member ("sig" / "enc"), empty when absent.
func (k KeyValue) Use() string {
	//: direct read of the immutable member.
	return k.use
}

// Alg reports the key's "alg" member, empty when absent.
func (k KeyValue) Alg() string {
	//: direct read of the immutable member.
	return k.alg
}

// KeyOps returns a copy of the key's "key_ops" member, nil when absent.
func (k KeyValue) KeyOps() []string {
	//: clone so a caller cannot reach back into the key through the slice.
	return slices.Clone(k.keyOps)
}

// isZero is KeyValue.IsZero's body: decl_gen.go writes KeyValue.IsZero, from the
// design, as one call of it.
func (k KeyValue) isZero() bool {
	//: kty is required by RFC 7517 §4.1, so its absence marks the zero value.
	return k.kty == ""
}

// isPrivate is KeyValue.IsPrivate's body: decl_gen.go writes KeyValue.IsPrivate, from the
// design, as one call of it.
func (k KeyValue) isPrivate() bool {
	//: the single secret member covers EC "d", OKP "d" and oct "k".
	return len(k.priv) != 0
}

// withKid is KeyValue.WithKid's body: decl_gen.go writes KeyValue.WithKid, from the
// design, as one call of it.
func (k KeyValue) withKid(kid string) KeyValue {
	//: the value receiver already gives us the copy; slices stay shared but the
	//: type exposes no way to mutate them.
	k.kid = kid
	//: hand back the modified copy, receiver untouched.
	return k
}

// withUse is KeyValue.WithUse's body: decl_gen.go writes KeyValue.WithUse, from the
// design, as one call of it.
func (k KeyValue) withUse(use string) KeyValue {
	//: same copy-on-write shape as WithKid.
	k.use = use
	//: hand back the modified copy.
	return k
}

// withAlg is KeyValue.WithAlg's body: decl_gen.go writes KeyValue.WithAlg, from the
// design, as one call of it.
func (k KeyValue) withAlg(alg string) KeyValue {
	//: same copy-on-write shape as WithKid.
	k.alg = alg
	//: hand back the modified copy.
	return k
}

// withKeyOps is KeyValue.WithKeyOps's body: decl_gen.go writes KeyValue.WithKeyOps, from the
// design, as one call of it.
func (k KeyValue) withKeyOps(ops ...string) KeyValue {
	//: clone so a later mutation of the caller's slice cannot reach the key.
	k.keyOps = slices.Clone(ops)
	//: hand back the modified copy.
	return k
}

// public is KeyValue.Public's body: decl_gen.go writes KeyValue.Public, from the
// design, as one call of it.
func (k KeyValue) public() (pub KeyValue, err error) {
	//: the zero KeyValue has nothing to reduce.
	if k.IsZero() {
		//: nothing to publish.
		return KeyValue{}, corejwk.MissingMember
	}
	//: a symmetric key has no public half; refusing beats returning an empty
	//: shell the caller might publish believing it is a real public key.
	if k.kty == TypeOct {
		//: the same refusal MarshalPublic raises, at the value level.
		return KeyValue{}, corejwk.NoPublicForm
	}
	//: drop the secret member; every other member is public metadata.
	k.priv = nil
	//: hand back the reduced copy.
	return k, nil
}

// Equal reports whether k and other describe the same key, comparing every
// member including the private one. The comparison is NOT constant-time and
// must not be used to authenticate anything; it exists so round-trip tests and
// set de-duplication can ask a precise question.
func (k KeyValue) Equal(other KeyValue) bool {
	//: metadata and material are separate questions; both must hold.
	return k.equalMetadata(other) && k.equalMaterial(other)
}

// equalMetadata compares the non-secret members, key_ops included.
func (k KeyValue) equalMetadata(other KeyValue) bool {
	//: the scalar members first, then the operation list verbatim — key_ops is
	//: order-significant as published, so it is compared, not normalised.
	return k.kty == other.kty && k.crv == other.crv && k.kid == other.kid &&
		k.use == other.use && k.alg == other.alg &&
		slices.Equal(k.keyOps, other.keyOps)
}

// equalMaterial compares the public members and the secret alike.
func (k KeyValue) equalMaterial(other KeyValue) bool {
	//: all three octet members, so a dropped secret never reads as equal.
	return bytes.Equal(k.x, other.x) && bytes.Equal(k.y, other.y) &&
		bytes.Equal(k.priv, other.priv)
}

// String implements fmt.Stringer and renders only non-secret metadata, so a
// stray %v or %s can never print key material — the same contract
// core/crypto.Key holds, extended to the members a JWK adds around it.
func (k KeyValue) String() string {
	//: kty/crv/kid are published members by definition; the secret is reported
	//: as a boolean, never as octets.
	return "jwk.KeyValue{kty:" + string(k.kty) +
		" crv:" + string(k.crv) +
		" kid:" + strconv.Quote(k.kid) +
		" private:" + strconv.FormatBool(k.IsPrivate()) + "}"
}

// goString is KeyValue.GoString's body: decl_gen.go writes KeyValue.GoString, from the
// design, as one call of it.
func (k KeyValue) goString() string {
	//: same rendering; the point is that no path reaches the material.
	return k.String()
}
