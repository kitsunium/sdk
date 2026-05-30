//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/hash .

// Package hash is the fingerprint / content-addressing hashing facade.
//
// One verb, many algorithms. [Sum] and [SumHex] hash arbitrary bytes to a
// digest; [New] returns a streaming [hash.Hash] for io.Copy over large inputs.
//
//	id, _ := hash.SumHex(hash.SHA256, payload)   // "9f86d0…" content id
//	ck    := hash.SumHex(hash.CRC32C, payload)   // fast cache key
//
// # NOT authentication
//
// This package is for content IDs, cache keys, and dedup keys — NOT message
// authentication, password storage, or signatures. No hasher is keyed, and a
// digest is public. The package deliberately offers no equality helper: never
// branch on a secret-dependent comparison of a digest. Keyed integrity lives in
// the crypto AEAD and signature surfaces (constant-time by construction there).
//
// # Algorithms
//
// Importing this package activates all of them with zero non-stdlib deps:
//
//   - [SHA256], [SHA512], [SHA3256] — cryptographic-strength digests for
//     content addressing and integrity (collision-resistant).
//   - [CRC32C], [FNV1a64] — fast NON-cryptographic checksums/fingerprints for
//     cache keys and in-memory dedup; do NOT use them where collision
//     resistance matters.
//
// # Stable algorithm strings
//
// The [Algorithm] constants are frozen post-v1.0.0 — a SumHex value computed
// today stays reproducible.
package hash

import (
	"hash"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"

	// Activates the stdlib hashers (sha256/sha512/sha3-256/crc32c/fnv1a-64).
	// Stdlib-only, so importing pkg/v1/hash pulls zero non-stdlib dependencies.
	_ "github.com/kitsunium/sdk/internal/service/crypto/stdhash"
)

// Algorithm is the stable identifier of a hash scheme.
type Algorithm = corecrypto.Algorithm

const (
	// SHA256 is SHA-256: a 256-bit cryptographic digest for content addressing.
	SHA256 Algorithm = "sha256"
	// SHA512 is SHA-512: a 512-bit cryptographic digest.
	SHA512 Algorithm = "sha512"
	// SHA3256 is SHA3-256: the Keccak-family 256-bit cryptographic digest.
	SHA3256 Algorithm = "sha3-256"
	// CRC32C is CRC-32C (Castagnoli): a fast NON-cryptographic checksum.
	CRC32C Algorithm = "crc32c"
	// FNV1a64 is FNV-1a 64-bit: a fast NON-cryptographic fingerprint.
	FNV1a64 Algorithm = "fnv1a-64"
)

// Sum returns the digest of data under the named algorithm. An unregistered
// algorithm returns UnknownHashAlgorithm.
func Sum(a Algorithm, data []byte) (digest []byte, err error) {
	//: delegate to the core registry dispatcher.
	return corecrypto.Sum(a, data)
}

// SumHex returns Sum as canonical lowercase hex — the frozen string form for
// content IDs and cache keys.
func SumHex(a Algorithm, data []byte) (digest string, err error) {
	//: delegate to the core registry dispatcher.
	return corecrypto.SumHex(a, data)
}

// New returns a fresh streaming hash.Hash for the named algorithm, for io.Copy
// over large inputs. An unregistered algorithm returns UnknownHashAlgorithm.
func New(a Algorithm) (h hash.Hash, err error) {
	//: delegate to the core registry dispatcher.
	return corecrypto.NewHash(a)
}
