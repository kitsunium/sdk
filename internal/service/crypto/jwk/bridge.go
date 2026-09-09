// Package jwk — the bridge between a JWK and the key encodings the SDK's own
// schemes already speak, so adopting the format costs no re-encoding by hand:
//
//	service/crypto/ecdsasig    PKIX DER public / SEC1 DER private
//	service/crypto/ed25519sig  raw 32-octet public / raw 64-octet private
//	service/crypto/hmacsha2    core/crypto.Key (redacting, 32 octets)
//
// The out-bound accessors named *Private / Secret hand back live key material,
// exactly as core/crypto.Key.Bytes() does, and carry the same rule: they feed a
// scheme, never a log line. They are in-process handoffs — the serialisation
// guard is MarshalPublic / MarshalPrivate, one layer up.
package jwk

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/x509"
	"math/big"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// FromECDSAPublic builds an EC JWK from a PKIX (SubjectPublicKeyInfo) DER blob
// — the shape service/crypto/ecdsasig returns as its public key.
func FromECDSAPublic(der []byte) (key KeyValue, err error) {
	//: PKIX is the encoding ecdsasig.GenerateKey emits for the public half.
	parsed, perr := x509.ParsePKIXPublicKey(der)
	//: a DER blob this parser rejects is a document problem, not a key.
	if perr != nil {
		//: keep the x509 cause; it describes structure, not key material.
		return KeyValue{}, errs.Wrap(perr, errs.WrapParams{
			Code:    CodeJWKMalformed,
			Reason:  "MALFORMED",
			Public:  "JWK document is malformed",
			Private: "service/crypto/jwk.FromECDSAPublic: x509.ParsePKIXPublicKey rejected the DER blob",
		})
	}
	//: the blob must decode to an ECDSA key specifically.
	pub, ok := parsed.(*ecdsa.PublicKey)
	//: an Ed25519 or RSA SPKI is a different constructor's job.
	if !ok {
		//: refuse rather than reinterpret the blob as another family.
		return KeyValue{}, TypeMismatch
	}
	//: curve naming and point validation live in one place.
	return fromECDSAPublicKey(pub)
}

// FromECDSAPrivate builds an EC JWK carrying "d" from a SEC1 EC DER blob — the
// shape service/crypto/ecdsasig returns as its private key.
func FromECDSAPrivate(der []byte) (key KeyValue, err error) {
	//: SEC1 is the encoding ecdsasig.GenerateKey emits for the private half.
	priv, perr := x509.ParseECPrivateKey(der)
	//: a DER blob this parser rejects is a document problem, not a key.
	if perr != nil {
		//: keep the x509 cause; it describes structure, not key material.
		return KeyValue{}, errs.Wrap(perr, errs.WrapParams{
			Code:    CodeJWKMalformed,
			Reason:  "MALFORMED",
			Public:  "JWK document is malformed",
			Private: "service/crypto/jwk.FromECDSAPrivate: x509.ParseECPrivateKey rejected the DER blob",
		})
	}
	//: the public half validates the curve and both coordinates first.
	base, kerr := fromECDSAPublicKey(&priv.PublicKey)
	//: the public half already failed; its code is the right one.
	if kerr != nil {
		//: already typed.
		return KeyValue{}, kerr
	}
	//: then the scalar, fixed-width like the coordinates.
	return attachScalar(base, priv.D)
}

// attachScalar fixes the private scalar to the curve's width and checks it
// derives the key's declared point, the same way Parse checks a "d" member.
func attachScalar(base KeyValue, scalar *big.Int) (key KeyValue, err error) {
	size := coordLen(base.crv)
	//: FillBytes panics on an over-wide integer, so bound it first.
	if scalar == nil || scalar.BitLen() > size*bitsPerOctet {
		//: a scalar that does not fit the field is not a key on this curve.
		return KeyValue{}, KeyMismatch
	}
	fixed := scalar.FillBytes(make([]byte, size))
	//: range-check AND require it to derive the declared point.
	if serr := checkECScalar(base.crv, fixed, base.x, base.y); serr != nil {
		//: out of range, or a keypair that does not agree with itself.
		return KeyValue{}, serr
	}
	base.priv = fixed
	//: a coherent private key.
	return base, nil
}

// fromECDSAPublicKey converts a parsed *ecdsa.PublicKey into the EC members.
func fromECDSAPublicKey(pub *ecdsa.PublicKey) (key KeyValue, err error) {
	//: only the three JOSE-named curves have a JWK spelling.
	crv, ok := curveFromElliptic(pub.Curve)
	//: P-224 and custom curves have no "crv" name.
	if !ok {
		//: no JOSE curve name means no JWK spelling.
		return KeyValue{}, UnsupportedCurve
	}
	size := coordLen(crv)
	//: FillBytes panics on an over-wide integer, so bound both first.
	if pub.X == nil || pub.Y == nil ||
		pub.X.BitLen() > size*bitsPerOctet || pub.Y.BitLen() > size*bitsPerOctet {
		//: coordinates that do not fit the field are not a point on it.
		return KeyValue{}, KeyMismatch
	}
	//: fixed-width big-endian, leading zeros preserved (RFC 7518 §6.2.1.2).
	xcoord := pub.X.FillBytes(make([]byte, size))
	ycoord := pub.Y.FillBytes(make([]byte, size))
	//: x509 does not check the point, so this package does.
	if perr := checkECPoint(crv, xcoord, ycoord); perr != nil {
		//: off-curve or the identity.
		return KeyValue{}, perr
	}
	//: a validated public EC key.
	return KeyValue{kty: TypeEC, crv: crv, x: xcoord, y: ycoord}, nil
}

// ECDSAPublic renders the key as a PKIX DER blob, ready for
// service/crypto/ecdsasig.Verify or crypto.Verify.
func (k KeyValue) ECDSAPublic() (der []byte, err error) {
	//: only an EC key has an ECDSA spelling.
	pub, kerr := k.ecdsaPublicKey()
	//: wrong family, or a curve with no x509 mapping.
	if kerr != nil {
		//: already typed.
		return nil, kerr
	}
	//: PKIX SubjectPublicKeyInfo, the shape ecdsasig.Verify parses.
	out, merr := x509.MarshalPKIXPublicKey(pub)
	//: defensive — Parse already validated the point, so this cannot fail.
	if merr != nil {
		//: surface typed rather than dropping an impossible error.
		return nil, KeyMismatch
	}
	//: the DER blob.
	return out, nil
}

// ECDSAPrivate renders the key as a SEC1 EC DER blob, ready for
// service/crypto/ecdsasig.Sign. It hands back live private material — the same
// contract core/crypto.Key.Bytes() carries: straight to the scheme, never to a
// log.
func (k KeyValue) ECDSAPrivate() (der []byte, err error) {
	//: only an EC key has an ECDSA spelling.
	pub, kerr := k.ecdsaPublicKey()
	//: wrong family, or a curve with no x509 mapping.
	if kerr != nil {
		//: already typed.
		return nil, kerr
	}
	//: refuse a public-only key instead of emitting a keyless SEC1 blob.
	if !k.IsPrivate() {
		//: the caller asked for material this key does not hold.
		return nil, NoPrivateMaterial
	}
	//: D is validated against x/y at construction, so the pair is coherent.
	priv := &ecdsa.PrivateKey{PublicKey: *pub, D: new(big.Int).SetBytes(k.priv)}
	out, merr := x509.MarshalECPrivateKey(priv)
	//: defensive — a validated keypair always marshals.
	if merr != nil {
		//: surface typed rather than dropping an impossible error.
		return nil, KeyMismatch
	}
	//: the DER blob.
	return out, nil
}

// ecdsaPublicKey rebuilds the crypto/ecdsa public key behind an EC JWK.
func (k KeyValue) ecdsaPublicKey() (pub *ecdsa.PublicKey, err error) {
	//: a non-EC key has no ECDSA rendering.
	if k.kty != TypeEC {
		//: wrong family for this accessor.
		return nil, TypeMismatch
	}
	//: the curve was validated at construction; re-resolve it for x509.
	curve, ok := ellipticCurve(k.crv)
	if !ok {
		//: unreachable for a constructed key; defensive.
		return nil, UnsupportedCurve
	}
	//: coordinates are stored fixed-width big-endian.
	return &ecdsa.PublicKey{
		Curve: curve,
		X:     new(big.Int).SetBytes(k.x),
		Y:     new(big.Int).SetBytes(k.y),
	}, nil
}

// FromEd25519Public builds an OKP JWK from a raw 32-octet Ed25519 public key —
// the shape service/crypto/ed25519sig returns.
func FromEd25519Public(pub []byte) (key KeyValue, err error) {
	//: crypto/ed25519 fixes the public key at 32 octets.
	if len(pub) != ed25519.PublicKeySize {
		//: a wrong-length blob is a data error, not a key.
		return KeyValue{}, errs.Wrap(InvalidEncoding, errs.WrapParams{},
			errs.Int("want", ed25519.PublicKeySize), errs.Int("got", len(pub)))
	}
	//: clone so a later mutation of the caller's slice cannot reach the key.
	return KeyValue{kty: TypeOKP, crv: CurveEd25519, x: bytes.Clone(pub)}, nil
}

// FromEd25519Private builds an OKP JWK carrying "d" from a raw 64-octet
// crypto/ed25519 private key (seed||public) — the shape
// service/crypto/ed25519sig returns. RFC 8037 §2 stores only the 32-octet seed
// in "d", so the expansion is dropped and re-derived on the way out.
func FromEd25519Private(priv []byte) (key KeyValue, err error) {
	//: crypto/ed25519 fixes the private key at 64 octets.
	if len(priv) != ed25519.PrivateKeySize {
		//: a wrong-length blob is a data error, not a key.
		return KeyValue{}, errs.Wrap(InvalidEncoding, errs.WrapParams{},
			errs.Int("want", ed25519.PrivateKeySize), errs.Int("got", len(priv)))
	}
	seed := priv[:ed25519KeyLen]
	//: the caller's tail must be the key the seed actually derives, or the two
	//: halves of the blob describe different keys.
	if !bytes.Equal(ed25519.NewKeyFromSeed(seed)[ed25519KeyLen:], priv[ed25519KeyLen:]) {
		//: seed and public half disagree.
		return KeyValue{}, KeyMismatch
	}
	//: a coherent Edwards keypair.
	return KeyValue{
		kty: TypeOKP, crv: CurveEd25519,
		x: bytes.Clone(priv[ed25519KeyLen:]), priv: bytes.Clone(seed),
	}, nil
}

// Ed25519Public returns the raw 32-octet public key, ready for
// service/crypto/ed25519sig.Verify.
func (k KeyValue) Ed25519Public() (pub []byte, err error) {
	//: only an OKP key has an Ed25519 rendering.
	if k.kty != TypeOKP {
		//: wrong family for this accessor.
		return nil, TypeMismatch
	}
	//: clone so the caller cannot reach into the key.
	return bytes.Clone(k.x), nil
}

// Ed25519Private returns the raw 64-octet private key (seed||public) that
// service/crypto/ed25519sig.Sign expects, re-expanded from the stored seed. It
// hands back live private material — straight to the scheme, never to a log.
func (k KeyValue) Ed25519Private() (priv []byte, err error) {
	//: only an OKP key has an Ed25519 rendering.
	if k.kty != TypeOKP {
		//: wrong family for this accessor.
		return nil, TypeMismatch
	}
	//: refuse a public-only key instead of returning a zero-seeded blob.
	if !k.IsPrivate() {
		//: the caller asked for material this key does not hold.
		return nil, NoPrivateMaterial
	}
	//: the seed was checked against x at construction, so this re-derives the
	//: same public half the JWK declares.
	return ed25519.NewKeyFromSeed(k.priv), nil
}

// FromSecret builds an oct JWK from the SDK's redacting symmetric key — the one
// service/crypto/hmacsha2 tags and verifies with.
//
// The resulting key is still redacting under %v, and still refuses
// MarshalPublic: wrapping a crypto.Key in a JWK must not be a way around the
// protection crypto.Key was given.
func FromSecret(secret corecrypto.Key) (key KeyValue, err error) {
	//: Bytes() already clones; a zero-value crypto.Key yields nil.
	raw := secret.Bytes()
	//: the SDK's symmetric surface is one fixed length.
	if len(raw) != corecrypto.KeyLen {
		//: reuse the core sentinel — the length rule belongs to core/crypto.
		return KeyValue{}, corecrypto.InvalidKey
	}
	//: a symmetric key, private by construction.
	return KeyValue{kty: TypeOct, priv: raw}, nil
}

// Secret returns the key as a core/crypto.Key, re-entering the redacting type
// so the material is protected again the moment it leaves this package.
//
// The SDK's symmetric surface is fixed at crypto.KeyLen octets, so an oct JWK
// of any other size is refused with the core InvalidKey sentinel — RFC 7518
// §6.4 allows any length, but nothing in this SDK could consume it.
func (k KeyValue) Secret() (secret corecrypto.Key, err error) {
	//: only an oct key is a symmetric secret.
	if k.kty != TypeOct {
		//: wrong family for this accessor.
		return corecrypto.Key{}, TypeMismatch
	}
	//: an oct key always carries "k"; a zero key does not reach here.
	if !k.IsPrivate() {
		//: nothing to hand back.
		return corecrypto.Key{}, NoPrivateMaterial
	}
	//: NewKey enforces the 32-octet rule and copies defensively.
	return corecrypto.NewKey(k.priv)
}
