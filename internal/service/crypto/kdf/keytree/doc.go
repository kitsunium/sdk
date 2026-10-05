// Package keytree implements path-addressed hierarchical key derivation.
//
// A KeyTree is an immutable node over a shared master Key. Each Child appends
// a path segment; DeriveKey re-derives a 32-byte AEAD Key from the master with
// the full canonical path as the HKDF info. The path encoding is injective —
// each segment is length-prefixed — so Child("a/b").Child("c") can never
// collide with Child("a").Child("b/c"). The root owns the master Zeroize
// lifetime: zeroizing the master invalidates every derived node. This package
// is a pure composition over the registered HKDF Deriver and mints no codes;
// derivation failures forward the core DerivationFailed sentinel.
package keytree
