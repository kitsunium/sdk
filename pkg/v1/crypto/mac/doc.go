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
// ([github.com/kitsunium/sdk/pkg/v1/crypto/sign]) for authenticity under a PUBLIC key
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
