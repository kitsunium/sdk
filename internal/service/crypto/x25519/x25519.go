// Package x25519 registers the "x25519" key-agreement scheme (ADR 0014).
// Importing the package (typically a blank import via pkg/v1/agree) self-registers
// the scheme so crypto.GenerateAgreementKey / crypto.AgreementShared resolve. It
// is stdlib-only (crypto/ecdh + crypto/rand), so it pulls zero non-stdlib deps
// and keeps pkg/v1/agree consumers dep-light.
//
// X25519 (RFC 7748) is the modern Diffie-Hellman default: a 32-byte public key,
// a 32-byte private key, and a 32-byte shared secret. crypto/ecdh rejects
// low-order peer points, so a malformed or attacker-chosen peer key surfaces as
// a typed error rather than a degenerate secret.
//
// Key-hygiene contract: the priv returned by GenerateKey and the secret returned
// by Shared are RAW key material. The caller (the pkg/v1/agree facade) MUST run
// the secret through a KDF before use and Zeroize both when done — this scheme
// never logs or retains either.
package x25519

import (
	"crypto/ecdh"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// algorithm is the canonical registry key for X25519.
const algorithm corecrypto.Algorithm = "x25519"

// Agreement is the registered X25519 scheme singleton (no init(); package-level
// var initialiser, mirroring the codec/AEAD convention).
var Agreement = corecrypto.RegisterAgreement(x25519Agreement{})

// x25519Agreement implements core/crypto.Agreement over crypto/ecdh's X25519 curve.
type x25519Agreement struct{}

// Algorithm reports the canonical algorithm key.
func (x25519Agreement) Algorithm() corecrypto.Algorithm {
	//: the literal key consumers pass to GenerateAgreementKey/AgreementShared.
	return algorithm
}

// GenerateKey draws a fresh X25519 keypair from crypto/rand and returns the raw
// 32-byte public and private key bytes. The only realistic failure is a host
// entropy fault. Treat priv as secret material and Zeroize it when done.
func (x25519Agreement) GenerateKey() (pub, priv []byte, err error) {
	//: draw an ephemeral private key; nil selects crypto/rand (Go 1.26+), and
	//: the curve clamps the scalar internally.
	sk, gerr := ecdh.X25519().GenerateKey(nil)
	//: a fault here is a host entropy problem, not a caller error.
	if gerr != nil {
		//: wrap the cause as the typed KeyGenerationFailed sentinel, no key bytes.
		return nil, nil, errs.Wrap(gerr, errs.WrapParams{
			Code:    corecrypto.CodeKeyGenerationFailed,
			Reason:  "KEY_GENERATION_FAILED",
			Public:  "Could not gather entropy for key generation",
			Private: "service/crypto/x25519.GenerateKey: crypto/rand failed in ecdh.X25519().GenerateKey",
		})
	}
	//: hand back the raw 32-byte public + private key bytes.
	return sk.PublicKey().Bytes(), sk.Bytes(), nil
}

// Shared derives the raw shared secret from priv and peerPub. crypto/ecdh
// rejects low-order or malformed peer points, so a bad peerPub (or priv) returns
// a non-nil error rather than a degenerate secret. The returned secret is RAW
// key material — the caller MUST KDF it before use and Zeroize it when done.
func (x25519Agreement) Shared(priv, peerPub []byte) (secret []byte, err error) {
	//: rebuild the local private key from its raw bytes (validates the length).
	sk, perr := ecdh.X25519().NewPrivateKey(priv)
	//: a malformed local key cannot derive anything; surface the cause to wrap.
	if perr != nil {
		//: hand the cause up; the dispatcher wraps it as AgreementFailed.
		return nil, perr
	}
	//: rebuild the peer public key; this rejects low-order/garbage points.
	pk, kerr := ecdh.X25519().NewPublicKey(peerPub)
	//: a malformed/low-order peer key is the attacker-controlled failure path.
	if kerr != nil {
		//: hand the cause up; the dispatcher wraps it without leaking key bytes.
		return nil, kerr
	}
	//: ECDH itself also rejects an all-zero shared output (low-order guard).
	return sk.ECDH(pk)
}
