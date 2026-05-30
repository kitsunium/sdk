// Package hkdfsha256 registers the "hkdf-sha256" key-derivation scheme
// (ADR 0013). Importing the package (typically a blank import via pkg/v1/kdf)
// self-registers the scheme so crypto.Subkey resolves. It is stdlib-only
// (crypto/hkdf + crypto/sha256), so it pulls zero non-stdlib deps and keeps
// pkg/v1/kdf consumers dep-light.
//
// HKDF (RFC 5869) is for KEY SEPARATION — expanding one strong secret into
// independent, purpose-bound subkeys — NOT for stretching passwords. Feed it a
// high-entropy secret; argon2id handles human passwords under its own port.
package hkdfsha256

import (
	"crypto/hkdf"
	"crypto/sha256"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
)

// algorithm is the canonical registry key for HKDF-SHA256.
const algorithm corecrypto.Algorithm = "hkdf-sha256"

// Deriver is the registered HKDF-SHA256 scheme singleton (no init();
// package-level var initialiser, mirroring the codec/AEAD convention).
var Deriver = corecrypto.RegisterDeriver(hkdfSHA256{})

// hkdfSHA256 implements core/crypto.Deriver over crypto/hkdf with SHA-256.
type hkdfSHA256 struct{}

// Algorithm reports the canonical algorithm key.
func (hkdfSHA256) Algorithm() corecrypto.Algorithm {
	//: the literal key consumers pass to Subkey.
	return algorithm
}

// Derive runs HKDF-SHA256 extract-and-expand over secret/salt/info. The only
// realistic failure is a length above HKDF's 255*HashLen (8160-byte) ceiling.
func (hkdfSHA256) Derive(secret, salt []byte, info string, length int) (subkey []byte, err error) {
	//: single-call extract-and-expand; salt may be nil (HKDF uses a zero salt).
	out, kerr := hkdf.Key(sha256.New, secret, salt, info, length)
	//: map an over-long request to the typed sentinel; cause is unambiguous.
	if kerr != nil {
		//: surface the typed data error, never the raw stdlib error.
		return nil, corecrypto.DerivationFailed
	}
	//: a fresh purpose-bound subkey.
	return out, nil
}
