package agree

import (
	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"

	// Activates the stdlib HKDF-SHA256 deriver SharedKey runs the raw secret
	// through. Stdlib-only.
	_ "github.com/kitsunium/sdk/internal/service/crypto/kdf/hkdfsha256"
	// Activates the stdlib X25519 agreement scheme. Stdlib-only, so importing
	// pkg/v1/crypto/agree pulls zero non-stdlib dependencies.
	_ "github.com/kitsunium/sdk/internal/service/crypto/agree/x25519"
)

// kdfAlgorithm is the scheme SharedKey runs the raw DH secret through. HKDF-SHA256
// is activated by this package's blank import above.
const kdfAlgorithm corecrypto.Algorithm = "hkdf-sha256"

// X25519 is Diffie-Hellman over Curve25519 (RFC 7748) — the modern default.
const X25519 Algorithm = "x25519"

// GenerateKey draws a fresh keypair for the named scheme, returning the raw
// public and private key bytes. An unregistered algorithm returns
// UnknownAgreementAlgorithm; treat priv as a secret and zero it when done.
func GenerateKey(a Algorithm) (pub, priv []byte, err error) {
	//: convert the domain-typed Algorithm to the core key at the boundary.
	return corecrypto.GenerateAgreementKey(corecrypto.Algorithm(a))
}

// SharedKey derives a redacting 32-byte symmetric Key from priv and peerPub for
// the named scheme, binding info as HKDF domain separation. The raw DH secret is
// HKDF'd and never returned. An unregistered scheme returns
// UnknownAgreementAlgorithm; a low-order/garbage peerPub returns AgreementFailed.
func SharedKey(a Algorithm, priv, peerPub []byte, info string) (key Key, err error) {
	//: establish the raw shared secret; a bad peer point fails here.
	secret, aerr := corecrypto.AgreementShared(corecrypto.Algorithm(a), priv, peerPub)
	//: surface the typed agreement failure without leaking key bytes.
	if aerr != nil {
		//: propagate UnknownAgreementAlgorithm / AgreementFailed unchanged.
		return Key{}, aerr
	}
	//: the raw secret is biased — HKDF it into a KeyLen subkey bound to info.
	derived, derr := corecrypto.Subkey(kdfAlgorithm, secret, nil, info, corecrypto.KeyLen)
	//: zeroize the raw secret as soon as it has been consumed by the KDF.
	clear(secret)
	//: an (unreachable) over-long derivation surfaces the typed sentinel.
	if derr != nil {
		//: never return the raw secret on the error path.
		return Key{}, derr
	}
	//: wrap the derived bytes in a redacting Key, then clear the loose copy.
	k, kerr := corecrypto.NewKey(derived)
	clear(derived)
	//: a length fault is impossible (Subkey returned exactly KeyLen), but typed.
	return k, kerr
}
