// Package stdhash — SHA-512 scheme registration (see package doc in stdhash.go).
package stdhash

import (
	"crypto/sha512"
	"hash"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
)

// sha512Hasher is SHA-512 — a secure 512-bit digest.
type sha512Hasher struct{}

// Self-register SHA-512 at package import (no init(); package-level var).
var _ = corecrypto.RegisterHasher(sha512Hasher{})

// Algorithm reports the canonical key "sha512".
func (sha512Hasher) Algorithm() corecrypto.Algorithm {
	//: the frozen canonical key for this scheme.
	return "sha512"
}

// New returns a fresh SHA-512 hash (64-byte digest).
func (sha512Hasher) New() hash.Hash {
	//: stdlib SHA-512.
	return sha512.New()
}
