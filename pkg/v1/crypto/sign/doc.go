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
// ([github.com/kitsunium/sdk/pkg/v1/crypto/hash]) for unkeyed public fingerprints.
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
