package sign

import (
	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"

	// Activates the stdlib Ed25519 + ECDSA-P256 signers. Stdlib-only, so importing
	// pkg/v1/crypto/sign pulls zero non-stdlib dependencies.
	_ "github.com/kitsunium/sdk/internal/service/crypto/sign/ecdsasig"
	_ "github.com/kitsunium/sdk/internal/service/crypto/sign/ed25519sig"
)

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
