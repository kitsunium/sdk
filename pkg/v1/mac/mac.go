//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/mac .

// Package mac is the detached message-authentication facade.
//
// Tag bytes with a secret key, verify the tag with the same key. One key, two
// verbs:
//
//	tag    := mac.Tag(mac.HMACSHA256, key, message)
//	ok, _  := mac.Verify(mac.HMACSHA256, key, message, tag)   // true
//
// # A MAC tag is secret — compare with Verify, never ==
//
// Unlike an unkeyed hash digest (a public fingerprint), a MAC tag is
// secret-comparison-sensitive: comparing it with == or bytes.Equal leaks timing
// about how many bytes matched. Always route equality through [Verify], which
// uses a constant-time comparison. This is the exact inverse of the hash rule.
//
// # MAC vs signature vs AEAD
//
// Use a MAC for detached integrity + authenticity under a SHARED secret (both
// parties hold the key). Use a signature
// ([github.com/kitsunium/sdk/pkg/v1/sign]) for authenticity under a PUBLIC key
// anyone can verify, and the AEAD surface
// ([github.com/kitsunium/sdk/pkg/v1/crypto]) for confidentiality + integrity.
//
// # Algorithms
//
// Importing this package activates HMAC-SHA256 with zero non-stdlib deps:
//
//   - [HMACSHA256] — HMAC (RFC 2104) over SHA-256; a 32-byte tag.
//
// # Stable algorithm strings
//
// The [Algorithm] constants are frozen post-v1.0.0 — a tag produced today stays
// verifiable.
package mac

import (
	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"

	// Activates the stdlib HMAC-SHA256 scheme. Stdlib-only, so importing
	// pkg/v1/mac pulls zero non-stdlib dependencies.
	_ "github.com/kitsunium/sdk/internal/service/crypto/hmacsha2"
)

// KeyLen is the required symmetric key length in bytes (256-bit) — the length
// [NewKey] enforces.
const KeyLen int = corecrypto.KeyLen

// HMACSHA256 is HMAC (RFC 2104) over SHA-256 — the detached-MAC default.
const HMACSHA256 Algorithm = "hmac-sha256"

// Algorithm is the stable identifier of a MAC scheme. It is a defined type
// distinct from the other crypto-family Algorithm types (hash, sign, kdf, …), so
// the compiler rejects feeding a hash or signature constant into a MAC call
// (V104) — the seven registries are separate keyspaces, and the type system now
// enforces that separation the way typed Format/Level discipline does elsewhere.
type Algorithm corecrypto.Algorithm

// Key is an opaque, redacting 256-bit symmetric key — the same key type used by
// the AEAD surface. Build one with [NewKey]; its String output is "<redacted>".
type Key = corecrypto.Key

// NewKey builds a Key from raw, which must be exactly KeyLen (32) bytes. A wrong
// length returns InvalidKey; the bytes are copied defensively.
func NewKey(raw []byte) (key Key, err error) {
	//: delegate to the core constructor; this facade adds no behaviour.
	return corecrypto.NewKey(raw)
}

// Tag returns the authentication tag over message under key for the named
// scheme. An unregistered algorithm returns UnknownMACAlgorithm; a registered
// scheme cannot fail (the redacting Key pins the length).
func Tag(a Algorithm, key Key, message []byte) (tag []byte, err error) {
	//: convert the domain-typed Algorithm to the core key at the boundary.
	return corecrypto.MACTag(corecrypto.Algorithm(a), key, message)
}

// Verify reports whether tag authenticates message under key for the named
// scheme, using a constant-time comparison. An unregistered algorithm returns
// (false, UnknownMACAlgorithm); a bad tag is (false, nil).
func Verify(a Algorithm, key Key, message, tag []byte) (ok bool, err error) {
	//: convert the domain-typed Algorithm to the core key at the boundary.
	return corecrypto.MACVerify(corecrypto.Algorithm(a), key, message, tag)
}
