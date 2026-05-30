// Package stdhash — FNV-1a 64-bit scheme registration (see package doc in stdhash.go).
package stdhash

import (
	"hash"
	"hash/fnv"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
)

// fnvHasher is FNV-1a 64-bit — a fast NON-cryptographic fingerprint for
// in-memory dedup/sharding keys, NOT a secure digest.
type fnvHasher struct{}

// Self-register FNV-1a 64-bit at package import (no init(); package-level var).
var _ = corecrypto.RegisterHasher(fnvHasher{})

// Algorithm reports the canonical key "fnv1a-64".
func (fnvHasher) Algorithm() corecrypto.Algorithm {
	//: the frozen canonical key for this scheme.
	return "fnv1a-64"
}

// New returns a fresh FNV-1a 64-bit hash (8-byte sum).
func (fnvHasher) New() hash.Hash {
	//: stdlib FNV-1a 64-bit.
	return fnv.New64a()
}
