//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/agree .

// Package agree is the key-agreement facade: establish a shared symmetric key
// between two parties from their keypairs.
//
// Each party generates a keypair, exchanges public keys, and derives the SAME
// [Key] — without ever transmitting the key itself:
//
//	pubA, privA, _ := agree.GenerateKey(agree.X25519)
//	pubB, privB, _ := agree.GenerateKey(agree.X25519)
//	keyA, _ := agree.SharedKey(agree.X25519, privA, pubB, "app-v1")
//	keyB, _ := agree.SharedKey(agree.X25519, privB, pubA, "app-v1")
//	// keyA and keyB are identical and ready for crypto.Seal.
//
// # The raw Diffie-Hellman secret is never handed back
//
// A raw DH secret is biased key material, unsafe to use directly. [SharedKey]
// runs it through HKDF-SHA256 (bound to the info label) and returns a redacting
// [Key] — the raw secret never leaves the SDK. The info label provides domain
// separation: two applications sharing one keypair derive independent keys.
//
// # Key hygiene
//
// The priv from [GenerateKey] is secret material: hold it like a password, never
// log it, and zero it when done. The returned [Key] redacts in logs; call its
// Zeroize when finished.
//
// # Algorithms
//
// Importing this package activates X25519 (and the HKDF-SHA256 it derives
// through) with zero non-stdlib deps:
//
//   - [X25519] — Diffie-Hellman over Curve25519 (RFC 7748); rejects low-order
//     peer points.
//
// # Stable algorithm strings
//
// The [Algorithm] constants are frozen post-v1.0.0.
package agree

import (
	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"

	// Activates the stdlib HKDF-SHA256 deriver SharedKey runs the raw secret
	// through. Stdlib-only.
	_ "github.com/kitsunium/sdk/internal/service/crypto/hkdfsha256"
	// Activates the stdlib X25519 agreement scheme. Stdlib-only, so importing
	// pkg/v1/agree pulls zero non-stdlib dependencies.
	_ "github.com/kitsunium/sdk/internal/service/crypto/x25519"
)

// kdfAlgorithm is the scheme SharedKey runs the raw DH secret through. HKDF-SHA256
// is activated by this package's blank import above.
const kdfAlgorithm corecrypto.Algorithm = "hkdf-sha256"

// Algorithm is the stable identifier of a key-agreement scheme.
type Algorithm = corecrypto.Algorithm

// Key is an opaque, redacting 256-bit symmetric key — the same key type the AEAD
// surface uses. SharedKey returns one; its String output is "<redacted>".
type Key = corecrypto.Key

// X25519 is Diffie-Hellman over Curve25519 (RFC 7748) — the modern default.
const X25519 Algorithm = "x25519"

// GenerateKey draws a fresh keypair for the named scheme, returning the raw
// public and private key bytes. An unregistered algorithm returns
// UnknownAgreementAlgorithm; treat priv as a secret and zero it when done.
func GenerateKey(a Algorithm) (pub, priv []byte, err error) {
	//: delegate to the core registry dispatcher.
	return corecrypto.GenerateAgreementKey(a)
}

// SharedKey derives a redacting 32-byte symmetric Key from priv and peerPub for
// the named scheme, binding info as HKDF domain separation. The raw DH secret is
// HKDF'd and never returned. An unregistered scheme returns
// UnknownAgreementAlgorithm; a low-order/garbage peerPub returns AgreementFailed.
func SharedKey(a Algorithm, priv, peerPub []byte, info string) (key Key, err error) {
	//: establish the raw shared secret; a bad peer point fails here.
	secret, aerr := corecrypto.AgreementShared(a, priv, peerPub)
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
