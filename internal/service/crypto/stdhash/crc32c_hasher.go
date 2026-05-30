// Package stdhash — CRC-32C (Castagnoli) scheme registration (see stdhash.go).
package stdhash

import (
	"hash"
	"hash/crc32"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
)

var (
	// castagnoli is the CRC-32C polynomial table, built once and shared by every
	// crc32cHasher.New() call (the table is read-only after construction).
	castagnoli = crc32.MakeTable(crc32.Castagnoli)
	// Self-register CRC-32C at package import (no init(); package-level var).
	_ = corecrypto.RegisterHasher(crc32cHasher{})
)

// crc32cHasher is CRC-32C (Castagnoli) — a fast NON-cryptographic checksum for
// cache keys and corruption detection, NOT a secure digest.
type crc32cHasher struct{}

// Algorithm reports the canonical key "crc32c".
func (crc32cHasher) Algorithm() corecrypto.Algorithm {
	//: the frozen canonical key for this scheme.
	return "crc32c"
}

// New returns a fresh CRC-32C hash over the shared Castagnoli table (4-byte sum).
func (crc32cHasher) New() hash.Hash {
	//: stdlib CRC-32C with the shared read-only table.
	return crc32.New(castagnoli)
}
