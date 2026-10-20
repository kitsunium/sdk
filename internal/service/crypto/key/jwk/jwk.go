package jwk

import (
	"bytes"
	"fmt"
	"io"
	"slices"
	"strconv"

	corejwk "github.com/kitsunium/sdk/internal/core/crypto/key/jwk"
)

// ed25519KeyLen is the octet length of an Ed25519 public key and of the seed
// RFC 8037 §2 puts in "d" — both 32, unlike crypto/ed25519's 64-octet private
// key, which is seed||public.
const ed25519KeyLen int = 32

// Type is the JWK "kty" member — the key-type family a JWK describes.
type Type string

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

// Curve is the JWK "crv" member — the curve a key lives on.
type Curve string

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

// KeyValue is one parsed or constructed JSON Web Key. It is an immutable value:
// the constructors copy every octet slice in, the accessors copy every octet
// slice out, and the With* methods return a modified copy rather than mutating
// the receiver.
//
// Every field is unexported so encoding/json cannot reach the material by
// reflection; the only serialisation paths are the named MarshalPublic /
// MarshalPrivate pair and the MarshalJSON delegate.
//
// Every rendering — String, GoString and Format, so every verb — writes only
// non-secret metadata. The private member sits behind a pointer, so a struct
// holding a KeyValue in an UNEXPORTED field, which fmt prints by reflection
// without calling Format, prints an address rather than the secret octets.
type KeyValue struct {
	// kty is the key-type family; the empty string marks the zero KeyValue.
	kty Type
	// crv is the curve for EC and OKP keys, empty for oct.
	crv Curve
	// kid is the optional "kid" member — a hint, never an authenticator.
	kid string
	// use is the optional "use" member ("sig" or "enc").
	use string
	// alg is the optional "alg" member (e.g. "ES256", "EdDSA", "HS256").
	alg string
	// keyOps is the optional "key_ops" member, preserved verbatim.
	keyOps []string
	// x holds the EC affine x coordinate, or the OKP public key.
	x []byte
	// y holds the EC affine y coordinate; nil for OKP and oct.
	y []byte
	// priv holds the secret member: the EC "d" scalar, the OKP "d" seed, or the
	// oct "k" key. Nil on a public-only key. One field, because exactly one of
	// those members can be set and "does this key hold a secret" must be a
	// single question. A pointer rather than a slice, so fmt walking a KeyValue
	// held in an unexported field prints an address, never the octets.
	priv *privateMember
}

// privateMember is the one allocation holding a KeyValue's secret octets. It is
// a separate type only so that the field a KeyValue carries is a pointer — see
// the KeyValue doc comment.
type privateMember struct {
	// raw is the secret member, never empty: no secret is a nil pointer.
	raw []byte
}

// holdPrivate wraps secret octets for a KeyValue, nil when there are none, so
// IsPrivate stays the one question "is the pointer set". It does not copy: the
// callers already hand over octets they own.
func holdPrivate(raw []byte) *privateMember {
	//: an empty secret is no secret.
	if len(raw) == 0 {
		//: a public-only key.
		return nil
	}
	//: the caller's octets, now the key's.
	return &privateMember{raw: raw}
}

// secret returns the secret octets without copying, nil on a public-only key.
// It never leaves the package; every exported path copies or encodes.
func (k KeyValue) secret() []byte {
	//: a public-only key has none.
	if k.priv == nil {
		//: nil, so len and bytes.Equal treat it as empty.
		return nil
	}
	//: the held octets, shared and never mutated.
	return k.priv.raw
}

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

// IsZero reports whether k is the zero KeyValue — no kty, hence nothing to
// serialise. Every marshaller rejects it with MissingMember.
func (k KeyValue) IsZero() bool {
	//: kty is required by RFC 7517 §4.1, so its absence marks the zero value.
	return k.kty == ""
}

// IsPrivate reports whether k holds private material ("d" or "k"). It is the
// question to ask before publishing a set: a true here means MarshalPrivate,
// and only MarshalPrivate, can serialise this key whole.
func (k KeyValue) IsPrivate() bool {
	//: the single secret member covers EC "d", OKP "d" and oct "k".
	return k.priv != nil
}

// WithKid returns a copy of k carrying kid. Passing the empty string clears it.
func (k KeyValue) WithKid(kid string) KeyValue {
	//: the value receiver already gives us the copy; slices stay shared but the
	//: type exposes no way to mutate them.
	k.kid = kid
	//: hand back the modified copy, receiver untouched.
	return k
}

// WithUse returns a copy of k carrying use ("sig" / "enc"). The value is not
// validated — RFC 7517 §4.2 leaves the vocabulary open.
func (k KeyValue) WithUse(use string) KeyValue {
	//: same copy-on-write shape as WithKid.
	k.use = use
	//: hand back the modified copy.
	return k
}

// WithAlg returns a copy of k carrying alg (e.g. "ES256"). The value is not
// checked against crv: an "alg" is a publisher's assertion, and rejecting an
// unfamiliar one here would break interop for no security gain.
func (k KeyValue) WithAlg(alg string) KeyValue {
	//: same copy-on-write shape as WithKid.
	k.alg = alg
	//: hand back the modified copy.
	return k
}

// WithKeyOps returns a copy of k carrying key_ops. The slice is copied.
func (k KeyValue) WithKeyOps(ops ...string) KeyValue {
	//: clone so a later mutation of the caller's slice cannot reach the key.
	k.keyOps = slices.Clone(ops)
	//: hand back the modified copy.
	return k
}

// Public returns the public half of k: the same key with its private member
// dropped. It refuses a symmetric key with NoPublicForm, because "the public
// half of an oct key" is not a thing that exists — the k member IS the secret.
func (k KeyValue) Public() (pub KeyValue, err error) {
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
		bytes.Equal(k.secret(), other.secret())
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

// GoString implements fmt.GoStringer so %#v stays redacted too — fmt bypasses
// String for Go-syntax formatting and would otherwise dump the raw slices.
func (k KeyValue) GoString() string {
	//: same rendering; the point is that no path reaches the material.
	return k.String()
}

// Format implements fmt.Formatter, so EVERY verb writes the same metadata-only
// rendering as String — %d included, which bypasses String and would
// otherwise print the octet slices. %q writes it quoted.
func (k KeyValue) Format(state fmt.State, verb rune) {
	//: %q quotes, so a surrounding quoted rendering stays well formed.
	if verb == 'q' {
		//: the metadata, quoted.
		writeRendering(state, strconv.Quote(k.String()))
		//: nothing else to write.
		return
	}
	//: every other verb writes the bare metadata.
	writeRendering(state, k.String())
}

// writeRendering writes a rendering into the state fmt handed to Format.
// fmt.Formatter has no error return and fmt discards what a write into its own
// buffer reports, so the error is read here and dropped as a visible decision.
func writeRendering(state fmt.State, text string) {
	//: fmt's buffer is the only destination, and it does not fail.
	if _, err := io.WriteString(state, text); err != nil {
		//: nothing to report to — fmt drops the same error.
		return
	}
}
