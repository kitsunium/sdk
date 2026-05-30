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

import (
	"encoding/binary"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"

	_ "github.com/kitsunium/sdk/internal/service/crypto/hkdfsha256"
)

// lenPrefixBytes is the width of the big-endian length prefix that precedes
// each path segment in the canonical HKDF info, making the encoding injective.
const lenPrefixBytes int = 4

// KeyTree is an immutable node in a path-addressed key-derivation tree. It
// holds the derivation algorithm, a shared master Key, and the accumulated
// path; Child returns a new node and never mutates the receiver.
type KeyTree struct {
	algo   corecrypto.Algorithm
	master corecrypto.Key
	path   []string
}

// NewKeyTree returns the root KeyTree for master, deriving children under algo.
func NewKeyTree(algo corecrypto.Algorithm, master corecrypto.Key) KeyTree {
	//: the root carries an empty path; children append to a copy
	return KeyTree{algo: algo, master: master, path: nil}
}

// Child returns a new KeyTree one segment deeper; the receiver is unchanged.
func (t KeyTree) Child(segment string) KeyTree {
	//: copy the path so the receiver stays immutable across children
	next := make([]string, len(t.path), len(t.path)+1)
	copy(next, t.path)
	next = append(next, segment)
	//: the new node shares the master by reference and owns its own path
	return KeyTree{algo: t.algo, master: t.master, path: next}
}

// DeriveKey re-derives a 32-byte AEAD Key from the master at this node's path.
// An unregistered algo yields UnknownKDFAlgorithm; an over-long request yields
// DerivationFailed — both forwarded verbatim from the core dispatcher.
func (t KeyTree) DeriveKey() (key corecrypto.Key, err error) {
	info := encodePath(t.path)
	raw, derr := corecrypto.Subkey(t.algo, t.master.Bytes(), nil, info, corecrypto.KeyLen)
	//: a derivation failure forwards the core sentinel verbatim
	if derr != nil {
		//: surface the derivation fault to the caller
		return corecrypto.Key{}, derr
	}
	//: the derived bytes are transient — wipe after copying into the Key
	defer clear(raw)
	//: wrap the derived bytes into a redacting Key
	return corecrypto.NewKey(raw)
}

// encodePath builds the injective canonical HKDF info: for each segment, a
// 4-byte big-endian length prefix followed by the segment bytes. The length
// prefix makes the encoding prefix-free, so distinct paths never collide
// regardless of where "/" appears inside a segment. The bytes are returned as
// a string because Subkey takes a string info (Go strings hold arbitrary bytes).
func encodePath(path []string) string {
	var buf []byte
	//: length-prefix each segment so the concatenation is injective
	for _, seg := range path {
		var lp [lenPrefixBytes]byte
		binary.BigEndian.PutUint32(lp[:], uint32(len(seg)))
		buf = append(buf, lp[:]...)
		buf = append(buf, seg...)
	}
	//: Subkey takes a string info; Go strings hold arbitrary bytes
	return string(buf)
}
