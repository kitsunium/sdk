// Package crypto — the Hasher port: fingerprint / content-addressing hashing.
package crypto

import "hash"

// Hasher is a fingerprint / content-addressing hash scheme producing a
// non-keyed digest of arbitrary bytes (content IDs, cache keys, dedup keys).
//
// It is explicitly NOT authentication: no Hasher is a MAC, none takes a key,
// and a digest is public — never branch on a secret-dependent comparison of
// one. Keyed/authenticated integrity belongs to the AEAD and (future) signature
// ports. Implementations self-register via RegisterHasher.
//
// IFACE-PLUGIN: the registry hands plug-in Hasher instances back behind this
// interface; concrete scheme types stay unexported in their own packages.
type Hasher interface {
	// Algorithm reports the canonical key under which this hasher registers.
	Algorithm() Algorithm
	// New returns a fresh hash.Hash for streaming (io.Copy) or one-shot use.
	New() hash.Hash
}
