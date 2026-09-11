package agree_test

import (
	"crypto/ecdh"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/agree"
)

// Package-level sinks. A shared key nobody observes is a value the compiler may
// prove dead and delete.
var (
	bytesSink []byte
	keySink   agree.Key
	errSink   error
)

// benchPair draws one X25519 keypair, ONCE, outside every timed loop.
func benchPair(b *testing.B) (pub, priv []byte) {
	b.Helper()
	pub, priv, err := agree.GenerateKey(agree.X25519)
	if err != nil {
		b.Fatalf("GenerateKey: %v", err)
	}
	return pub, priv
}

// BenchmarkGenerateKey prices the ephemeral half of a handshake: a fresh
// keypair is drawn PER SESSION by a party that wants forward secrecy, so unlike
// a signing key this one is on a request path.
func BenchmarkGenerateKey(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bytesSink, _, errSink = agree.GenerateKey(agree.X25519)
	}
}

// BenchmarkSharedKey is the agreement itself as the SDK spells it: one scalar
// multiplication, then HKDF-SHA256 over the raw secret, then a redacting Key.
// The raw DH secret is never handed back, so those three steps are one call and
// this is their combined price.
func BenchmarkSharedKey(b *testing.B) {
	_, privA := benchPair(b)
	pubB, _ := benchPair(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		keySink, errSink = agree.SharedKey(agree.X25519, privA, pubB, "app-v1")
	}
}

// BenchmarkBareECDHRaw is crypto/ecdh with both keys ALREADY PARSED and no KDF
// — the raw scalar multiplication alone. The gap against SharedKey is
// everything the facade insists on: re-parsing both keys from their raw bytes
// (which is where the low-order peer-point rejection lives), the HKDF pass that
// turns a biased secret into uniform key material, and the Key wrapper.
func BenchmarkBareECDHRaw(b *testing.B) {
	_, privA := benchPair(b)
	pubB, _ := benchPair(b)
	sk, err := ecdh.X25519().NewPrivateKey(privA)
	if err != nil {
		b.Fatalf("NewPrivateKey: %v", err)
	}
	pk, err := ecdh.X25519().NewPublicKey(pubB)
	if err != nil {
		b.Fatalf("NewPublicKey: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bytesSink, errSink = sk.ECDH(pk)
	}
}

// BenchmarkBareECDHWithParse adds back the two key parses the SDK pays per
// call, so the envelope is attributed between "re-parsing keys" and "HKDF plus
// the Key wrapper" rather than left as one lump.
func BenchmarkBareECDHWithParse(b *testing.B) {
	_, privA := benchPair(b)
	pubB, _ := benchPair(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sk, serr := ecdh.X25519().NewPrivateKey(privA)
		if serr != nil {
			errSink = serr
			continue
		}
		pk, perr := ecdh.X25519().NewPublicKey(pubB)
		if perr != nil {
			errSink = perr
			continue
		}
		bytesSink, errSink = sk.ECDH(pk)
	}
	if errSink != nil {
		b.Fatalf("ECDH: %v", errSink)
	}
}
