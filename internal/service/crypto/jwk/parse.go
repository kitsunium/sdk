// Package jwk — the decode half: JSON document to KeyValue, with every
// RFC 7517 / 7518 / 8037 rejection made explicit and typed.
//
// Parsing accepts private members. That is not the asymmetry this package
// guards: a caller parsing its own private JWK already holds the material. The
// guarded direction is serialisation (see marshal.go), where a protected value
// would become publishable JSON.
//
// There is deliberately no KeyValue.UnmarshalJSON. Parse is the single entry
// point, so there is exactly one place where a document becomes a key and
// exactly one set of checks it must pass — and KeyValue keeps a uniform value
// receiver, which is what makes MarshalJSON fire on a plain (non-pointer) key
// and therefore what makes the safe default actually reachable.
package jwk

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"slices"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Parse decodes one JWK object (RFC 7517 §4). It accepts a public or a private
// key and validates it fully: required members present, members spelled as
// unpadded base64url, coordinates at the curve's fixed length, the point on the
// declared curve, and — when "d" is present — the private scalar deriving
// exactly the declared public key.
//
// Members this package does not model are ignored, per RFC 7517 §4, and are NOT
// carried through to a later Marshal: re-emitting a member we never understood
// would be vouching for it.
func Parse(document []byte) (key KeyValue, err error) {
	var raw keyJSON
	//: one strict pass; unknown members fall on the floor by struct shape.
	if uerr := json.Unmarshal(document, &raw); uerr != nil {
		//: keep the decoder's cause — it names an offset, never the material.
		return KeyValue{}, errs.Wrap(uerr, errs.WrapParams{
			Code:    CodeJWKMalformed,
			Reason:  "MALFORMED",
			Public:  "JWK document is malformed",
			Private: "service/crypto/jwk.Parse: json.Unmarshal rejected the document",
		})
	}
	//: shape validated, now the cryptographic content.
	return fromWire(raw)
}

// fromWire validates a decoded keyJSON and builds the immutable KeyValue.
func fromWire(raw keyJSON) (key KeyValue, err error) {
	//: "kty" is the one member RFC 7517 §4.1 makes unconditionally required.
	if raw.Kty == "" {
		//: without it there is nothing to dispatch on.
		return KeyValue{}, MissingMember
	}
	//: metadata is common to every family; material is family-specific.
	base := KeyValue{
		kty:    Type(raw.Kty),
		crv:    Curve(raw.Crv),
		kid:    raw.Kid,
		use:    raw.Use,
		alg:    raw.Alg,
		keyOps: slices.Clone(raw.KeyOps),
	}
	//: dispatch on the declared family.
	switch base.kty {
	//: NIST prime curve, members x/y (+d).
	case TypeEC:
		//: hand off to the EC validator.
		return parseEC(base, raw)
	//: Edwards curve, members x (+d).
	case TypeOKP:
		//: hand off to the OKP validator.
		return parseOKP(base, raw)
	//: symmetric, member k only.
	case TypeOct:
		//: hand off to the oct validator.
		return parseOct(base, raw)
	//: RSA lands here on purpose — the SDK ships no RSA scheme.
	default:
		//: an unmodelled family, RSA included.
		return KeyValue{}, UnsupportedKeyType
	}
}

// parseEC validates an "EC" JWK (RFC 7518 §6.2).
func parseEC(base KeyValue, raw keyJSON) (key KeyValue, err error) {
	//: an unmodelled curve (including Ed25519 mislabelled as EC) has no length.
	size := coordLen(base.crv)
	//: unknown curve, or a curve valid under another kty.
	if size == 0 {
		//: the declared curve has no fixed coordinate length here.
		return KeyValue{}, UnsupportedCurve
	}
	//: both coordinates are required for a public EC key.
	if raw.X == "" || raw.Y == "" {
		//: an EC JWK without x/y describes nothing.
		return KeyValue{}, MissingMember
	}
	//: fixed-length decode, twice.
	xcoord, xerr := b64DecodeFixed(raw.X, size)
	//: already typed as InvalidEncoding.
	if xerr != nil {
		//: propagate the encoding refusal untouched.
		return KeyValue{}, xerr
	}
	ycoord, yerr := b64DecodeFixed(raw.Y, size)
	//: already typed as InvalidEncoding.
	if yerr != nil {
		//: propagate the encoding refusal untouched.
		return KeyValue{}, yerr
	}
	//: the check a length-only parser skips: is this a point on the curve?
	if perr := checkECPoint(base.crv, xcoord, ycoord); perr != nil {
		//: off-curve or the identity.
		return KeyValue{}, perr
	}
	base.x, base.y = xcoord, ycoord
	//: "d" is optional; its absence simply makes this a public key.
	return attachECPrivate(base, raw.D, size)
}

// attachECPrivate validates and attaches the optional EC "d" member.
func attachECPrivate(base KeyValue, member string, size int) (key KeyValue, err error) {
	//: no "d" — a public key, already complete.
	if member == "" {
		//: nothing to attach.
		return base, nil
	}
	//: the scalar shares the coordinates' fixed length.
	scalar, derr := b64DecodeFixed(member, size)
	//: already typed as InvalidEncoding.
	if derr != nil {
		//: propagate the encoding refusal untouched.
		return KeyValue{}, derr
	}
	//: range-check the scalar AND require it to derive the declared point.
	if serr := checkECScalar(base.crv, scalar, base.x, base.y); serr != nil {
		//: out of range, or a keypair that does not agree with itself.
		return KeyValue{}, serr
	}
	base.priv = scalar
	//: a coherent private key.
	return base, nil
}

// parseOKP validates an "OKP" JWK (RFC 8037 §2). Ed25519 is the only OKP curve
// the SDK signs with, so it is the only one accepted: representing X25519 or
// Ed448 here would promise an interop no registered scheme can honour.
func parseOKP(base KeyValue, raw keyJSON) (key KeyValue, err error) {
	//: exactly one accepted curve.
	if base.crv != CurveEd25519 {
		//: unknown curve, or a NIST curve mislabelled as OKP.
		return KeyValue{}, UnsupportedCurve
	}
	//: the public key is required.
	if raw.X == "" {
		//: an OKP JWK without x describes nothing.
		return KeyValue{}, MissingMember
	}
	//: Ed25519 public keys are exactly 32 octets.
	pub, xerr := b64DecodeFixed(raw.X, ed25519KeyLen)
	//: already typed as InvalidEncoding.
	if xerr != nil {
		//: propagate the encoding refusal untouched.
		return KeyValue{}, xerr
	}
	base.x = pub
	//: "d" is optional; when present it is the 32-octet SEED, not the 64-octet
	//: crypto/ed25519 private key (RFC 8037 §2).
	return attachOKPPrivate(base, raw.D)
}

// attachOKPPrivate validates and attaches the optional OKP "d" seed.
func attachOKPPrivate(base KeyValue, member string) (key KeyValue, err error) {
	//: no "d" — a public key, already complete.
	if member == "" {
		//: nothing to attach.
		return base, nil
	}
	//: RFC 8037 §2 puts the 32-octet seed in "d", never the expanded key.
	seed, derr := b64DecodeFixed(member, ed25519KeyLen)
	//: already typed as InvalidEncoding.
	if derr != nil {
		//: propagate the encoding refusal untouched.
		return KeyValue{}, derr
	}
	//: NewKeyFromSeed returns seed||public, so the tail is the derived public
	//: key; it must equal the declared x or the JWK contradicts itself.
	if !bytes.Equal(ed25519.NewKeyFromSeed(seed)[ed25519KeyLen:], base.x) {
		//: d and x describe different keys.
		return KeyValue{}, KeyMismatch
	}
	base.priv = seed
	//: a coherent private key.
	return base, nil
}

// parseOct validates an "oct" JWK (RFC 7518 §6.4). The whole key is the secret,
// which is why every serialisation path treats this family apart.
func parseOct(base KeyValue, raw keyJSON) (key KeyValue, err error) {
	//: a curve on a symmetric key is a contradiction, not a stray member.
	if base.crv != "" {
		//: reject rather than silently drop, so the confusion surfaces.
		return KeyValue{}, UnsupportedCurve
	}
	//: "k" is required.
	if raw.K == "" {
		//: an oct JWK without k carries no key.
		return KeyValue{}, MissingMember
	}
	//: no fixed length here — RFC 7518 §6.4 leaves the size to the algorithm;
	//: Secret() applies the SDK's 32-octet rule at the crypto.Key boundary.
	secret, kerr := b64Decode(raw.K)
	//: already typed as InvalidEncoding.
	if kerr != nil {
		//: propagate the encoding refusal untouched.
		return KeyValue{}, kerr
	}
	base.priv = secret
	//: a symmetric key, private by construction.
	return base, nil
}
