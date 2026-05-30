// Package crypto — the MAC port: keyed, detached message authentication.
package crypto

import "hash"

// MAC is a detached message-authentication-code scheme: it binds a secret Key
// to a message and yields an authentication tag. Unlike a Hasher digest, a MAC
// tag IS secret-comparison-sensitive — callers MUST route equality through
// Verify (constant-time), never `==` or bytes.Equal, which is the exact inverse
// of the Hasher rule. Implementations self-register via RegisterMAC.
//
// IFACE-PLUGIN: the registry hands plug-in MAC instances back behind this
// interface; concrete scheme types stay unexported in their own packages.
type MAC interface {
	// Algorithm reports the canonical key under which this MAC registers.
	Algorithm() Algorithm
	// Tag returns the authentication tag over message under key.
	Tag(key Key, message []byte) []byte
	// Verify reports whether tag authenticates message under key, using a
	// constant-time comparison so it never becomes a timing oracle.
	Verify(key Key, message, tag []byte) bool
	// New returns a fresh streaming hash.Hash keyed by key, paralleling
	// Hasher.New, for incremental tagging over large inputs.
	New(key Key) hash.Hash
}
