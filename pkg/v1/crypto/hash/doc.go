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
