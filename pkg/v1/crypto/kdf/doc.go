// Package kdf is the key-derivation facade for KEY SEPARATION.
//
// Expand one strong secret into independent, purpose-bound subkeys:
//
//	enc, _ := kdf.Subkey(kdf.HKDFSHA256, master, salt, "aead-key", 32)
//	mac, _ := kdf.Subkey(kdf.HKDFSHA256, master, salt, "mac-key", 32)
//
// The info label binds each subkey to its purpose, so the two derivations above
// are cryptographically independent even though they share master and salt.
//
// # Hierarchical derivation
//
// [NewKeyTree] returns a [KeyTree] — an immutable, path-addressed node over a
// shared master key. Child appends a path segment; DeriveKey re-derives a
// 32-byte key from the master with the full canonical path as the HKDF info.
// The path encoding is injective, so distinct paths never collide:
//
//	t := kdf.NewKeyTree(kdf.HKDFSHA256, master)
//	dbKey, _ := t.Child("svc").Child("db").DeriveKey()
//
// # NOT for passwords
//
// HKDF assumes a HIGH-ENTROPY input — an AEAD key, a Diffie-Hellman shared
// secret, an HKDF pseudorandom key. It does NOT stretch human passwords: it is
// fast by design, so a weak password stays weak. Password hashing/stretching
// (argon2id) is a separate, deliberately slow surface.
//
// # Algorithms
//
// Importing this package activates HKDF-SHA256 with zero non-stdlib deps:
//
//   - [HKDFSHA256] — HMAC-based extract-and-expand (RFC 5869) over SHA-256.
//
// # Stable algorithm strings
//
// The [Algorithm] constants are frozen post-v1.0.0 — a subkey derived today
// stays reproducible.
package kdf
