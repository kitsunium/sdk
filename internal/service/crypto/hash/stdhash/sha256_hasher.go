// Package stdhash — SHA-256 scheme registration (see package doc in stdhash.go).
package stdhash

import (
	"crypto/sha256"
	"hash"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
)

// sha256Hasher is SHA-256 — a secure 256-bit digest for content addressing.
type sha256Hasher struct{}

// Self-register SHA-256 at package import (no init(); package-level var).
var _ = corecrypto.RegisterHasher(sha256Hasher{})

// Algorithm reports the canonical key "sha256".
func (sha256Hasher) Algorithm() corecrypto.Algorithm {
	//: the frozen canonical key for this scheme.
	return "sha256"
}

// New returns a fresh SHA-256 hash (32-byte digest).
func (sha256Hasher) New() hash.Hash {
	//: stdlib SHA-256.
	return sha256.New()
}
