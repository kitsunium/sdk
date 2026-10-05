// Package stdhash — CRC-32C (Castagnoli) scheme registration (see stdhash.go).
//
// Package stdhash — DigestWriter streaming content-address ergonomics (see the
// package doc in stdhash.go).
//
// Package stdhash — FNV-1a 64-bit scheme registration (see package doc in stdhash.go).
//
// Package stdhash — SHA-256 scheme registration (see package doc in stdhash.go).
//
// Package stdhash — SHA3-256 scheme registration (see package doc in stdhash.go).
//
// Package stdhash — SHA-512 scheme registration (see package doc in stdhash.go).
//
// Package stdhash registers the stdlib fingerprint/content-addressing hashers
// (ADR 0013): sha256, sha512, sha3-256, crc32c, fnv1a-64. Blank-importing the
// package self-registers them so crypto.Sum / crypto.SumHex / crypto.NewHash
// resolve. It is stdlib-only (crypto/sha256, crypto/sha512, crypto/sha3,
// hash/crc32, hash/fnv), so it pulls zero non-stdlib deps and keeps
// pkg/v1/crypto/hash consumers dep-light.
//
// These are NOT authentication: none is keyed, and a digest is public. Keyed
// integrity belongs to the AEAD / signature ports.
//
// Each scheme lives in its own file (one comparable empty-struct Hasher per
// file, per KTN-STRUCT-ONEFILE) and self-registers via a package-level var —
// no init(), mirroring the codec/AEAD convention.
//
// Package stdhash — VerifyingReader streaming content-address ergonomics (see
// the package doc in stdhash.go).
package stdhash
