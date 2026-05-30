// Package ecdsasig registers the "ecdsa-p256" signature scheme (ADR 0013).
// Importing the package (typically a blank import via pkg/v1/sign) self-registers
// the scheme so crypto.Sign / crypto.Verify / crypto.GenerateKey resolve. It is
// stdlib-only (crypto/ecdsa + crypto/elliptic + crypto/sha256 + crypto/x509 +
// crypto/rand), so it pulls zero non-stdlib deps and keeps pkg/v1/sign dep-light.
//
// ECDSA over NIST P-256 with SHA-256 digests and ASN.1/DER signatures — the
// interoperable choice for JWTs, X.509, and other ecosystems that expect ECDSA.
// Ed25519 (ed25519sig) is the modern default when interop is not required.
//
// Keys are DER-marshalled: the public key is PKIX (SubjectPublicKeyInfo) and the
// private key is SEC1 (the x509.MarshalECPrivateKey form).
package ecdsasig

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// algorithm is the canonical registry key for ECDSA P-256.
const algorithm corecrypto.Algorithm = "ecdsa-p256"

// Signer is the registered ECDSA P-256 scheme singleton (no init();
// package-level var initialiser, mirroring the codec/AEAD convention).
var Signer = corecrypto.RegisterSigner(ecdsaP256{})

// ecdsaP256 implements core/crypto.Signer over crypto/ecdsa on the P-256 curve.
type ecdsaP256 struct{}

// Algorithm reports the canonical algorithm key.
func (ecdsaP256) Algorithm() corecrypto.Algorithm {
	//: the literal key consumers pass to Sign/Verify/GenerateKey.
	return algorithm
}

// GenerateKey draws a fresh P-256 keypair and returns it DER-marshalled (PKIX
// public, SEC1 private). The only realistic failure is a host entropy fault.
func (ecdsaP256) GenerateKey() (pub, priv []byte, err error) {
	//: P-256 keypair; nil reader selects crypto/rand by default (Go 1.26+).
	key, gerr := ecdsa.GenerateKey(elliptic.P256(), nil)
	//: a fault here is a host entropy problem, not a caller error.
	if gerr != nil {
		//: wrap the cause as the typed KeyGenerationFailed sentinel.
		return nil, nil, errs.Wrap(gerr, errs.WrapParams{
			Code:    corecrypto.CodeKeyGenerationFailed,
			Reason:  "KEY_GENERATION_FAILED",
			Public:  "Could not gather entropy for key generation",
			Private: "service/crypto/ecdsasig.GenerateKey: crypto/rand failed in ecdsa.GenerateKey",
		})
	}
	//: SEC1 DER for the private key.
	privDER, merr := x509.MarshalECPrivateKey(key)
	//: PKIX DER for the public key.
	pubDER, perr := x509.MarshalPKIXPublicKey(&key.PublicKey)
	//: marshalling a freshly generated key never fails (defensive guard).
	if merr != nil || perr != nil {
		//: surface a typed failure rather than a raw marshalling error.
		return nil, nil, corecrypto.KeyGenerationFailed
	}
	//: hand back both DER blobs.
	return pubDER, privDER, nil
}

// Sign hashes message with SHA-256 and returns an ASN.1/DER ECDSA signature
// under the SEC1-DER private key priv. A malformed priv returns SigningFailed.
func (ecdsaP256) Sign(priv, message []byte) (sig []byte, err error) {
	//: parse the SEC1 DER private key; a bad blob is a typed data error.
	key, perr := x509.ParseECPrivateKey(priv)
	//: reject a malformed key rather than panicking.
	if perr != nil {
		//: surface the typed data error.
		return nil, corecrypto.SigningFailed
	}
	//: ECDSA signs a digest, not the message — hash with SHA-256 first.
	digest := sha256.Sum256(message)
	//: SignASN1 draws its nonce from crypto/rand and emits DER.
	out, serr := ecdsa.SignASN1(rand.Reader, key, digest[:])
	//: a signing fault (e.g. entropy) collapses to the typed sentinel.
	if serr != nil {
		//: never a panic — typed failure.
		return nil, corecrypto.SigningFailed
	}
	//: the DER signature.
	return out, nil
}

// Verify reports whether sig is a valid ASN.1/DER ECDSA signature over message
// under the PKIX-DER public key pub. A malformed key or signature is false.
func (ecdsaP256) Verify(pub, message, sig []byte) bool {
	//: parse the PKIX DER public key; a bad blob cannot verify anything.
	parsed, perr := x509.ParsePKIXPublicKey(pub)
	//: a malformed key is false, never a panic.
	if perr != nil {
		//: clean false.
		return false
	}
	//: the blob must decode to an ECDSA public key specifically.
	key, ok := parsed.(*ecdsa.PublicKey)
	//: a non-ECDSA key (e.g. RSA, Ed25519) cannot verify this scheme.
	if !ok {
		//: clean false.
		return false
	}
	//: hash the message the same way Sign did, then verify the DER signature.
	digest := sha256.Sum256(message)
	//: VerifyASN1 is constant-time w.r.t. the signature and returns a bool.
	return ecdsa.VerifyASN1(key, digest[:], sig)
}
