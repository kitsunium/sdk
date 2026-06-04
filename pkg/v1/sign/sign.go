//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/sign .

// Package sign is the detached digital-signature facade.
//
// Generate a keypair, sign bytes with the private key, verify with the public
// key. One verb-set, pluggable schemes:
//
//	pub, priv, _ := sign.GenerateKey(sign.Ed25519)
//	sig, _       := sign.Sign(sign.Ed25519, priv, message)
//	ok, _        := sign.Verify(sign.Ed25519, pub, message, sig)   // true
//
// # Keys are raw bytes — the private key is secret
//
// Keys and signatures are scheme-specific byte slices (Ed25519: 32-byte public,
// 64-byte private, 64-byte signature). The private key is secret material: hold
// it like a password, never log it, and zero it when done. [Verify] runs in
// constant time with respect to the signature, so a failed check never leaks
// timing about WHERE it failed.
//
// # Signatures vs AEAD vs hashing
//
// Use signatures for authenticity + non-repudiation under a public key anyone
// can verify. Use the AEAD surface ([github.com/kitsunium/sdk/pkg/v1/crypto])
// for confidentiality with a shared secret, and the hash surface
// ([github.com/kitsunium/sdk/pkg/v1/hash]) for unkeyed public fingerprints.
//
// # Algorithms
//
// Importing this package activates both schemes with zero non-stdlib deps:
//
//   - [Ed25519] — the modern default: small fixed-size keys, fast verification,
//     nothing to misconfigure.
//   - [ECDSAP256] — ECDSA over NIST P-256 with SHA-256 + ASN.1/DER signatures;
//     the interoperable choice for JWT/X.509 ecosystems. Keys are DER-marshalled
//     (PKIX public, SEC1 private).
//
// # Stable algorithm strings
//
// The [Algorithm] constants are frozen post-v1.0.0 — a signature produced today
// stays verifiable.
package sign

import (
	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"

	// Activates the stdlib Ed25519 + ECDSA-P256 signers. Stdlib-only, so importing
	// pkg/v1/sign pulls zero non-stdlib dependencies.
	_ "github.com/kitsunium/sdk/internal/service/crypto/ecdsasig"
	_ "github.com/kitsunium/sdk/internal/service/crypto/ed25519sig"
)

// Algorithm is the stable identifier of a signature scheme. It is a defined type
// distinct from the other crypto-family Algorithm types (hash, mac, kdf, …), so
// the compiler rejects feeding a hash or MAC constant into a signature call
// (V104) — the seven registries are separate keyspaces, and the type system now
// enforces that separation the way typed Format/Level discipline does elsewhere.
type Algorithm corecrypto.Algorithm

// Ed25519 is the EdDSA signature scheme over Curve25519 (RFC 8032): a 256-bit
// public key, fast constant-time verification, and no parameter choices.
const Ed25519 Algorithm = "ed25519"

// ECDSAP256 is ECDSA over NIST P-256 with SHA-256 and ASN.1/DER signatures — the
// interoperable choice for JWT/X.509 ecosystems. Keys are DER-marshalled.
const ECDSAP256 Algorithm = "ecdsa-p256"

// GenerateKey draws a fresh keypair for the named scheme, returning the public
// and private key bytes. An unregistered algorithm returns
// UnknownSignatureAlgorithm; treat priv as a secret.
func GenerateKey(a Algorithm) (pub, priv []byte, err error) {
	//: convert the domain-typed Algorithm to the core key at the boundary.
	return corecrypto.GenerateKey(corecrypto.Algorithm(a))
}

// Sign returns a detached signature over message using priv under the named
// scheme. An unregistered algorithm returns UnknownSignatureAlgorithm; a
// malformed priv returns SigningFailed.
func Sign(a Algorithm, priv, message []byte) (sig []byte, err error) {
	//: convert the domain-typed Algorithm to the core key at the boundary.
	return corecrypto.Sign(corecrypto.Algorithm(a), priv, message)
}

// Verify reports whether sig is a valid signature for message under pub for the
// named scheme. An unregistered algorithm returns
// (false, UnknownSignatureAlgorithm); an invalid signature is (false, nil).
func Verify(a Algorithm, pub, message, sig []byte) (ok bool, err error) {
	//: convert the domain-typed Algorithm to the core key at the boundary.
	return corecrypto.Verify(corecrypto.Algorithm(a), pub, message, sig)
}
