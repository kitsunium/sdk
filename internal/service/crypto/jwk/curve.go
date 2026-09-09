// Package jwk — curve arithmetic and the two validity checks a JWK parser is
// most often missing: that the declared point is actually ON the declared
// curve, and that a declared private scalar actually derives that point.
//
// Both go through crypto/ecdh rather than crypto/elliptic: ecdh.Curve's
// NewPublicKey / NewPrivateKey validate their input (on-curve, not the identity,
// scalar in [1, n-1]) whereas the elliptic.Curve point methods are deprecated
// precisely because they do not.
package jwk

import (
	"bytes"
	"crypto/ecdh"
	"crypto/elliptic"
)

// uncompressedPointTag is the SEC1 §2.3.3 prefix of an uncompressed point,
// the encoding crypto/ecdh accepts for a NIST public key.
const uncompressedPointTag byte = 0x04

// bitsPerOctet bounds a big.Int against a fixed-width field element: an integer
// wider than coordLen(crv)*bitsPerOctet cannot be a coordinate on that curve,
// and would make math/big.Int.FillBytes panic.
const bitsPerOctet int = 8

// p256CoordLen is the fixed octet width of one coordinate on NIST P-256.
const p256CoordLen int = 32

// p384CoordLen is the fixed octet width of one coordinate on NIST P-384.
const p384CoordLen int = 48

// p521CoordLen is the fixed octet width of one coordinate on NIST P-521 — the
// odd one out, because that curve's bit length is not a multiple of eight and
// therefore rounds up to a whole extra octet.
const p521CoordLen int = 66

// coordLen returns the fixed octet length of one field element on crv, or 0
// when crv is not a NIST prime curve this package models. The length is fixed
// by RFC 7518 §6.2.1.2: leading zero octets are part of the encoding, so a
// "compact" 31-octet P-256 coordinate is a different number, not a tidier one.
func coordLen(crv Curve) int {
	//: the three JOSE NIST curves; anything else has no EC coordinate length.
	switch crv {
	//: the ES256 curve.
	case CurveP256:
		//: 32 octets.
		return p256CoordLen
	//: the ES384 curve.
	case CurveP384:
		//: 48 octets.
		return p384CoordLen
	//: the ES512 curve, whose width rounds up.
	case CurveP521:
		//: 66 octets.
		return p521CoordLen
	//: OKP curves and unknown names are not EC curves.
	default:
		//: no EC coordinate length applies.
		return 0
	}
}

// ecdhCurve maps a JWK curve name onto the crypto/ecdh curve that validates
// its points and scalars. ok is false for OKP and unknown curves.
func ecdhCurve(crv Curve) (curve ecdh.Curve, ok bool) {
	//: the ecdh package covers exactly the three NIST curves JOSE names.
	switch crv {
	//: P-256 / ES256.
	case CurveP256:
		//: the ecdh curve that validates P-256 points and scalars.
		return ecdh.P256(), true
	//: P-384 / ES384.
	case CurveP384:
		//: the ecdh curve that validates P-384 points and scalars.
		return ecdh.P384(), true
	//: P-521 / ES512.
	case CurveP521:
		//: the ecdh curve that validates P-521 points and scalars.
		return ecdh.P521(), true
	//: Ed25519 and unknown names have no ecdh counterpart here.
	default:
		//: nothing to validate against.
		return nil, false
	}
}

// ellipticCurve maps a JWK curve name onto the crypto/elliptic curve that
// crypto/x509 needs to marshal an ECDSA key. ok is false outside the NIST set.
func ellipticCurve(crv Curve) (curve elliptic.Curve, ok bool) {
	//: mirrors ecdhCurve; the two packages model the same three curves.
	switch crv {
	//: P-256.
	case CurveP256:
		//: the elliptic curve crypto/x509 marshals P-256 against.
		return elliptic.P256(), true
	//: P-384.
	case CurveP384:
		//: the elliptic curve crypto/x509 marshals P-384 against.
		return elliptic.P384(), true
	//: P-521.
	case CurveP521:
		//: the elliptic curve crypto/x509 marshals P-521 against.
		return elliptic.P521(), true
	//: not a NIST prime curve.
	default:
		//: nothing to marshal against.
		return nil, false
	}
}

// curveFromElliptic is the reverse map, naming the JWK curve of a parsed
// crypto/ecdsa key. The elliptic.PXXX constructors return singletons, so
// interface comparison is the idiomatic identity test (service/crypto/ecdsasig
// pins its curve the same way).
func curveFromElliptic(curve elliptic.Curve) (crv Curve, ok bool) {
	//: identity comparison against the three singletons.
	switch curve {
	//: P-256.
	case elliptic.P256():
		//: the JOSE name of P-256.
		return CurveP256, true
	//: P-384.
	case elliptic.P384():
		//: the JOSE name of P-384.
		return CurveP384, true
	//: P-521.
	case elliptic.P521():
		//: the JOSE name of P-521.
		return CurveP521, true
	//: P-224 and any custom curve are out of scope: JOSE names none.
	default:
		//: no JOSE name exists for this curve.
		return "", false
	}
}

// uncompressedPoint assembles the SEC1 uncompressed encoding 0x04||x||y that
// crypto/ecdh consumes and produces.
func uncompressedPoint(x, y []byte) []byte {
	//: exact capacity — one tag plus two field elements.
	point := make([]byte, 0, 1+len(x)+len(y))
	//: tag, then the two coordinates in order.
	point = append(point, uncompressedPointTag)
	point = append(point, x...)
	point = append(point, y...)
	//: the SEC1 blob crypto/ecdh expects.
	return point
}

// checkECPoint reports whether (x, y) is a point on crv. It is the check that
// separates "these are two well-formed integers" from "this is a public key":
// an off-curve or identity point is accepted by any parser that only measures
// coordinate lengths, and using one is a documented route to key recovery.
func checkECPoint(crv Curve, x, y []byte) error {
	//: an unmapped curve cannot validate anything.
	curve, ok := ecdhCurve(crv)
	if !ok {
		//: the caller declared a curve this package does not model.
		return UnsupportedCurve
	}
	//: NewPublicKey rejects off-curve points and the point at infinity.
	if _, err := curve.NewPublicKey(uncompressedPoint(x, y)); err != nil {
		//: the material is well-encoded but is not a key on this curve.
		return KeyMismatch
	}
	//: a genuine public point.
	return nil
}

// checkECScalar reports whether d is a valid private scalar on crv AND derives
// exactly the public point (x, y). The second half matters: a JWK whose "d"
// disagrees with its "x"/"y" is either corrupt or an attempt to make a verifier
// and a signer disagree about which key they hold.
func checkECScalar(crv Curve, d, x, y []byte) error {
	//: an unmapped curve cannot validate anything.
	curve, ok := ecdhCurve(crv)
	if !ok {
		//: the caller declared a curve this package does not model.
		return UnsupportedCurve
	}
	//: NewPrivateKey enforces the exact length and the [1, n-1] range; the
	//: short-circuit keeps the derived-point comparison off the error path.
	priv, err := curve.NewPrivateKey(d)
	//: an out-of-range scalar, or one that derives a different point.
	if err != nil || !bytes.Equal(priv.PublicKey().Bytes(), uncompressedPoint(x, y)) {
		//: the two halves of the keypair do not agree.
		return KeyMismatch
	}
	//: consistent keypair.
	return nil
}
