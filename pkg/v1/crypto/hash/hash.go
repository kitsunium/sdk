package hash

import (
	"hash"
	"io"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/service/crypto/hash/stdhash"
)

// SHA256 is SHA-256: a 256-bit cryptographic digest for content addressing.
const SHA256 Algorithm = "sha256"

// SHA512 is SHA-512: a 512-bit cryptographic digest.
const SHA512 Algorithm = "sha512"

// SHA3256 is SHA3-256: the Keccak-family 256-bit cryptographic digest.
const SHA3256 Algorithm = "sha3-256"

// CRC32C is CRC-32C (Castagnoli): a fast NON-cryptographic checksum.
const CRC32C Algorithm = "crc32c"

// FNV1a64 is FNV-1a 64-bit: a fast NON-cryptographic fingerprint.
const FNV1a64 Algorithm = "fnv1a-64"

// Algorithm is the stable identifier of a hash scheme. It is a defined type
// distinct from the other crypto-family Algorithm types (mac, sign, kdf, …), so
// the compiler rejects feeding a MAC or signature constant into a hash call
// (V104) — the seven registries are separate keyspaces, and the type system now
// enforces that separation the way typed Format/Level discipline does elsewhere.
type Algorithm corecrypto.Algorithm

// Sum returns the digest of data under the named algorithm. An unregistered
// algorithm returns UnknownHashAlgorithm.
func Sum(a Algorithm, data []byte) (digest []byte, err error) {
	//: convert the domain-typed Algorithm to the core key at the boundary.
	return corecrypto.Sum(corecrypto.Algorithm(a), data)
}

// SumHex returns Sum as canonical lowercase hex — the frozen string form for
// content IDs and cache keys.
func SumHex(a Algorithm, data []byte) (digest string, err error) {
	//: convert the domain-typed Algorithm to the core key at the boundary.
	return corecrypto.SumHex(corecrypto.Algorithm(a), data)
}

// New returns a fresh streaming hash.Hash for the named algorithm, for io.Copy
// over large inputs. An unregistered algorithm returns UnknownHashAlgorithm.
func New(a Algorithm) (h hash.Hash, err error) {
	//: convert the domain-typed Algorithm to the core key at the boundary.
	return corecrypto.NewHash(corecrypto.Algorithm(a))
}

// NewDigestWriter returns a DigestWriter that tees writes into dst while hashing
// them under the named algorithm, so the content-address digest of everything
// written is available via Sum/SumHex. An unregistered algorithm returns
// UnknownHashAlgorithm.
func NewDigestWriter(a Algorithm, dst io.Writer) (writer *DigestWriter, err error) {
	//: convert the domain-typed Algorithm to the core key at the boundary.
	return stdhash.NewDigestWriter(corecrypto.Algorithm(a), dst)
}

// NewVerifyingReader returns a VerifyingReader over src that hashes the stream
// under the named algorithm and verifies it against wantHex on the final (EOF)
// read — a public-digest, non-oracle, EOF-typed check that never fails
// mid-stream. An unregistered algorithm returns UnknownHashAlgorithm; a digest
// mismatch surfaces as DigestMismatch only at EOF.
func NewVerifyingReader(a Algorithm, src io.Reader, wantHex string) (reader *VerifyingReader, err error) {
	//: convert the domain-typed Algorithm to the core key at the boundary.
	return stdhash.NewVerifyingReader(corecrypto.Algorithm(a), src, wantHex)
}
