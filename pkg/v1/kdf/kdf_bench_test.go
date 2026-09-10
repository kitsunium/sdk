package kdf_test

import (
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/kdf"
)

// outLen is the subkey width every benchmark asks for — the length of every
// symmetric Key in this SDK ([kdf.KeyLen]), so the table reads as "one subkey".
const outLen int = kdf.KeyLen

// Package-level sinks. A subkey nobody observes is a value the compiler may
// prove dead and delete.
var (
	bytesSink []byte
	keySink   kdf.Key
	treeSink  kdf.KeyTree
	errSink   error
)

// benchSecret returns the high-entropy master HKDF is designed for. HKDF is NOT
// a password stretcher — feeding it a human secret is the mistake the package
// doc refuses, and it is not what is measured here.
func benchSecret(b *testing.B) []byte {
	b.Helper()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		b.Fatalf("rand.Read: %v", err)
	}
	return secret
}

// BenchmarkSubkey32B and BenchmarkSubkey64B price one key separation. HKDF
// extracts once and expands in HashLen-sized blocks, so 64 bytes costs one more
// expand round than 32 — the second number is there so a reader can see that
// the output length is a per-BLOCK cost and not a per-byte one.
func BenchmarkSubkey32B(b *testing.B) { benchSubkey(b, outLen) }

func BenchmarkSubkey64B(b *testing.B) { benchSubkey(b, 2*outLen) }

func benchSubkey(b *testing.B, length int) {
	b.Helper()
	secret := benchSecret(b)
	salt := benchSecret(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bytesSink, errSink = kdf.Subkey(kdf.HKDFSHA256, secret, salt, "aead-key", length)
	}
}

// BenchmarkSubkeyNoSalt32B is the nil-salt path. HKDF substitutes a zero salt of
// HashLen bytes, so this should cost the same as the salted call; the number
// exists so nobody skips the salt believing it buys speed.
func BenchmarkSubkeyNoSalt32B(b *testing.B) {
	secret := benchSecret(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bytesSink, errSink = kdf.Subkey(kdf.HKDFSHA256, secret, nil, "aead-key", outLen)
	}
}

// BenchmarkBareHKDF32B is crypto/hkdf driven directly. The gap against Subkey
// is the facade's envelope: one registry lookup and one interface call.
func BenchmarkBareHKDF32B(b *testing.B) {
	secret := benchSecret(b)
	salt := benchSecret(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bytesSink, errSink = hkdf.Key(sha256.New, secret, salt, "aead-key", outLen)
	}
}

// BenchmarkKeyTreeDeriveDepth1 and BenchmarkKeyTreeDeriveDepth3 price the
// hierarchical path. DeriveKey re-derives from the MASTER with the full
// canonical path as the HKDF info, so depth is a string-encoding cost and not a
// chain of derivations — these two numbers are how a reader checks that claim
// instead of taking it.
func BenchmarkKeyTreeDeriveDepth1(b *testing.B) { benchKeyTree(b, 1) }

func BenchmarkKeyTreeDeriveDepth3(b *testing.B) { benchKeyTree(b, 3) }

func benchKeyTree(b *testing.B, depth int) {
	b.Helper()
	master, err := kdf.NewKey(benchSecret(b))
	if err != nil {
		b.Fatalf("NewKey: %v", err)
	}
	node := kdf.NewKeyTree(kdf.HKDFSHA256, master)
	for i := range depth {
		node = node.Child(string(rune('a' + i)))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		keySink, errSink = node.DeriveKey()
	}
}

// BenchmarkKeyTreeChild prices descending alone, without the derivation, so the
// two halves of a `t.Child("svc").Child("db").DeriveKey()` call site are
// separable.
func BenchmarkKeyTreeChild(b *testing.B) {
	master, err := kdf.NewKey(benchSecret(b))
	if err != nil {
		b.Fatalf("NewKey: %v", err)
	}
	root := kdf.NewKeyTree(kdf.HKDFSHA256, master)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		treeSink = root.Child("svc")
	}
}
