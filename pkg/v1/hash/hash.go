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
// digest is public. The package deliberately offers no secret-comparison
// equality helper: never branch on a secret-dependent comparison of a digest.
// Keyed integrity lives in the crypto AEAD and signature surfaces (constant-time
// by construction there).
//
// # Streaming content addressing
//
// [NewDigestWriter] tees writes into a destination while computing the digest of
// everything written; [NewVerifyingReader] verifies a stream against an expected
// hex digest, failing only on the final (EOF) read with DigestMismatch. This is
// public-digest, non-oracle verification — a content-ID check on public data, not
// a secret-comparison oracle — so it does not contradict the NOT-authentication
// rule above.
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
	"io"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/service/crypto/stdhash"
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

// DigestWriter tees writes into both a destination io.Writer and a running hash,
// exposing the digest of everything written so far. See [NewDigestWriter].
type DigestWriter = stdhash.DigestWriter

// VerifyingReader wraps a source reader and verifies its digest against an
// expected value on the final (EOF) read. See [NewVerifyingReader].
type VerifyingReader = stdhash.VerifyingReader

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

// NewDigestWriter returns a DigestWriter that tees writes into dst while hashing
// them under the named algorithm, so the content-address digest of everything
// written is available via Sum/SumHex. An unregistered algorithm returns
// UnknownHashAlgorithm.
func NewDigestWriter(a Algorithm, dst io.Writer) (writer *DigestWriter, err error) {
	//: delegate to the stdhash emitter — the facade is alias + delegation only.
	return stdhash.NewDigestWriter(a, dst)
}

// NewVerifyingReader returns a VerifyingReader over src that hashes the stream
// under the named algorithm and verifies it against wantHex on the final (EOF)
// read — a public-digest, non-oracle, EOF-typed check that never fails
// mid-stream. An unregistered algorithm returns UnknownHashAlgorithm; a digest
// mismatch surfaces as DigestMismatch only at EOF.
func NewVerifyingReader(a Algorithm, src io.Reader, wantHex string) (reader *VerifyingReader, err error) {
	//: delegate to the stdhash emitter — the facade is alias + delegation only.
	return stdhash.NewVerifyingReader(a, src, wantHex)
}
