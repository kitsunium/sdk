// Package ed25519sig registers the "ed25519" signature scheme (ADR 0013).
// Importing the package (typically a blank import via pkg/v1/sign) self-registers
// the scheme so crypto.Sign / crypto.Verify / crypto.GenerateKey resolve. It is
// stdlib-only (crypto/ed25519 + crypto/rand), so it pulls zero non-stdlib deps
// and keeps pkg/v1/sign consumers dep-light.
//
// Ed25519 is the modern default: small fixed-size keys and signatures, fast
// verification, and no parameter choices to misconfigure. ECDSA lands later
// under its own scheme package.
package ed25519sig

import (
	"crypto/ed25519"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// algorithm is the canonical registry key for Ed25519.
const algorithm corecrypto.Algorithm = "ed25519"

// Signer is the registered Ed25519 scheme singleton (no init(); package-level
// var initialiser, mirroring the codec/AEAD convention).
var Signer = corecrypto.RegisterSigner(ed25519Signer{})

// ed25519Signer implements core/crypto.Signer over crypto/ed25519.
type ed25519Signer struct{}

// Algorithm reports the canonical algorithm key.
func (ed25519Signer) Algorithm() corecrypto.Algorithm {
	//: the literal key consumers pass to Sign/Verify/GenerateKey.
	return algorithm
}

// GenerateKey draws a fresh Ed25519 keypair from crypto/rand. The only realistic
// failure is a host entropy fault.
func (ed25519Signer) GenerateKey() (pub, priv []byte, err error) {
	//: nil reader selects crypto/rand by default (Go 1.26+); the scheme owns its
	//: entropy source so no crypto/rand import is needed here.
	pubKey, privKey, gerr := ed25519.GenerateKey(nil)
	//: a fault here is a host entropy problem, not a caller error.
	if gerr != nil {
		//: wrap the cause as the typed KeyGenerationFailed sentinel.
		return nil, nil, errs.Wrap(gerr, errs.WrapParams{
			Code:    corecrypto.CodeKeyGenerationFailed,
			Reason:  "KEY_GENERATION_FAILED",
			Public:  "Could not gather entropy for key generation",
			Private: "service/crypto/ed25519sig.GenerateKey: crypto/rand failed in ed25519.GenerateKey",
		})
	}
	//: hand back the raw key bytes ([]byte aliases of the stdlib key types).
	return pubKey, privKey, nil
}

// Sign produces a 64-byte detached signature over message using priv. A priv of
// the wrong length returns SigningFailed rather than panicking (ed25519.Sign
// panics on a bad key length).
func (ed25519Signer) Sign(priv, message []byte) (sig []byte, err error) {
	//: guard the key length so a malformed key is a typed error, not a panic.
	if len(priv) != ed25519.PrivateKeySize {
		//: surface the typed data error.
		return nil, corecrypto.SigningFailed
	}
	//: ed25519.Sign is deterministic and never errors past the length guard.
	return ed25519.Sign(ed25519.PrivateKey(priv), message), nil
}

// Verify reports whether sig is a valid Ed25519 signature for message under pub.
// A pub of the wrong length is reported as false (ed25519.Verify panics on a bad
// public-key length), as is any invalid signature.
func (ed25519Signer) Verify(pub, message, sig []byte) bool {
	//: guard the public-key length so a malformed key is false, not a panic.
	if len(pub) != ed25519.PublicKeySize {
		//: a malformed key cannot verify anything.
		return false
	}
	//: ed25519.Verify is constant-time w.r.t. the signature and never panics on
	//: a wrong signature length (it returns false).
	return ed25519.Verify(ed25519.PublicKey(pub), message, sig)
}
