// Package stdhash — SHA3-256 scheme registration (see package doc in stdhash.go).
package stdhash

import (
	"crypto/sha3"
	"hash"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
)

// sha3Hasher is SHA3-256 — the Keccak-family 256-bit cryptographic digest.
type sha3Hasher struct{}

// Self-register SHA3-256 at package import (no init(); package-level var).
var _ = corecrypto.RegisterHasher(sha3Hasher{})

// Algorithm reports the canonical key "sha3-256".
func (sha3Hasher) Algorithm() corecrypto.Algorithm {
	//: the frozen canonical key for this scheme.
	return "sha3-256"
}

// New returns a fresh SHA3-256 hash (32-byte digest).
func (sha3Hasher) New() hash.Hash {
	//: stdlib SHA3-256.
	return sha3.New256()
}
