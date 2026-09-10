package sign_test

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"slices"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/sign"
)

// A signature is taken over a DIGEST, so the message size is not the axis here
// the way it is for an AEAD or a hash: sizeSmall is the token-sized case and
// sizeMedium is there only to show that it is not.
const (
	sizeSmall  int = 64
	sizeMedium int = 4 << 10
)

// Package-level sinks. A signature nobody observes is a value the compiler may
// prove dead and delete.
var (
	bytesSink []byte
	boolSink  bool
	errSink   error
)

// benchPayload returns n bytes drawn from crypto/rand.
func benchPayload(b *testing.B, n int) []byte {
	b.Helper()
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		b.Fatalf("rand.Read: %v", err)
	}
	return buf
}

// benchKeypair draws one keypair for a scheme, ONCE, outside the timed loop.
// Generating inside it would measure key generation — which for ECDSA costs
// more than the signature and would swamp the number this file is about.
func benchKeypair(b *testing.B, a sign.Algorithm) (pub, priv []byte) {
	b.Helper()
	pub, priv, err := sign.GenerateKey(a)
	if err != nil {
		b.Fatalf("GenerateKey(%s): %v", a, err)
	}
	return pub, priv
}

// BenchmarkSignEd25519_64B through BenchmarkVerifyECDSAP256_64B are the point
// of this file. The two schemes differ in WHICH side is expensive, and that
// asymmetry — not the absolute nanoseconds — is what decides a design: a
// signature is produced once and verified by everyone, so a verifier-heavy
// scheme multiplies its cost by the audience.
func BenchmarkSignEd25519_64B(b *testing.B) { benchSign(b, sign.Ed25519, sizeSmall) }

func BenchmarkSignEd25519_4KiB(b *testing.B) { benchSign(b, sign.Ed25519, sizeMedium) }

func BenchmarkSignECDSAP256_64B(b *testing.B) { benchSign(b, sign.ECDSAP256, sizeSmall) }

func BenchmarkSignECDSAP256_4KiB(b *testing.B) { benchSign(b, sign.ECDSAP256, sizeMedium) }

func benchSign(b *testing.B, a sign.Algorithm, n int) {
	b.Helper()
	_, priv := benchKeypair(b, a)
	message := benchPayload(b, n)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bytesSink, errSink = sign.Sign(a, priv, message)
	}
}

func BenchmarkVerifyEd25519_64B(b *testing.B) { benchVerify(b, sign.Ed25519, sizeSmall) }

func BenchmarkVerifyEd25519_4KiB(b *testing.B) { benchVerify(b, sign.Ed25519, sizeMedium) }

func BenchmarkVerifyECDSAP256_64B(b *testing.B) { benchVerify(b, sign.ECDSAP256, sizeSmall) }

func BenchmarkVerifyECDSAP256_4KiB(b *testing.B) { benchVerify(b, sign.ECDSAP256, sizeMedium) }

func benchVerify(b *testing.B, a sign.Algorithm, n int) {
	b.Helper()
	pub, priv := benchKeypair(b, a)
	message := benchPayload(b, n)
	sig, err := sign.Sign(a, priv, message)
	if err != nil {
		b.Fatalf("Sign(%s): %v", a, err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		boolSink, errSink = sign.Verify(a, pub, message, sig)
	}
}

// BenchmarkVerify*Tampered64B is the realistic refusal: the signature is
// STRUCTURALLY VALID and the message changed under it, so the whole check must
// run before the answer is known. A verifier is exposed to input the caller
// does not choose, so a refusal costing MORE than an acceptance would be an
// amplification an attacker gets for free; these are the numbers that say it is
// not.
func BenchmarkVerifyEd25519Tampered64B(b *testing.B) { benchVerifyTampered(b, sign.Ed25519) }

func BenchmarkVerifyECDSAP256Tampered64B(b *testing.B) { benchVerifyTampered(b, sign.ECDSAP256) }

func benchVerifyTampered(b *testing.B, a sign.Algorithm) {
	b.Helper()
	pub, priv := benchKeypair(b, a)
	message := benchPayload(b, sizeSmall)
	sig, err := sign.Sign(a, priv, message)
	if err != nil {
		b.Fatalf("Sign(%s): %v", a, err)
	}
	tampered := slices.Clone(message)
	tampered[0] ^= 0xFF
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		boolSink, errSink = sign.Verify(a, pub, tampered, sig)
	}
}

// BenchmarkVerify*Malformed64B is the OTHER refusal, and it is a different
// number for a stated reason. Flipping the high byte of an Ed25519 signature
// pushes its scalar S out of the canonical range RFC 8032 §5.1.7 requires, and
// that bound is checked BEFORE the scalar multiplication it would fund — so the
// refusal is orders of magnitude cheaper than an acceptance. The same flip on a
// DER-encoded ECDSA signature usually leaves an in-range s, so the full check
// still runs. Neither leaks anything: a signature is public, and the bound is a
// property of the bytes, not of the key.
func BenchmarkVerifyEd25519Malformed64B(b *testing.B) { benchVerifyMalformed(b, sign.Ed25519) }

func BenchmarkVerifyECDSAP256Malformed64B(b *testing.B) { benchVerifyMalformed(b, sign.ECDSAP256) }

func benchVerifyMalformed(b *testing.B, a sign.Algorithm) {
	b.Helper()
	pub, priv := benchKeypair(b, a)
	message := benchPayload(b, sizeSmall)
	sig, err := sign.Sign(a, priv, message)
	if err != nil {
		b.Fatalf("Sign(%s): %v", a, err)
	}
	sig[len(sig)-1] ^= 0xFF
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		boolSink, errSink = sign.Verify(a, pub, message, sig)
	}
}

// BenchmarkGenerateKeyEd25519 and BenchmarkGenerateKeyECDSAP256 price the
// keypair. It is drawn once per identity rather than once per request, so it is
// here for completeness — and because the ECDSA number explains why the
// benchmarks above hoist it out of the loop.
func BenchmarkGenerateKeyEd25519(b *testing.B) { benchGenerate(b, sign.Ed25519) }

func BenchmarkGenerateKeyECDSAP256(b *testing.B) { benchGenerate(b, sign.ECDSAP256) }

func benchGenerate(b *testing.B, a sign.Algorithm) {
	b.Helper()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bytesSink, _, errSink = sign.GenerateKey(a)
	}
}

// BenchmarkBareEd25519Sign through BenchmarkBareECDSAP256Verify are the same
// primitives with the keys ALREADY PARSED, which is the shape a caller holding
// a *ecdsa.PrivateKey would have. The SDK's port is byte-slice-in, byte-slice-
// out, so it re-parses DER on every call; these four benchmarks are what that
// re-parse costs, separated from the elliptic-curve work it wraps.
func BenchmarkBareEd25519Sign(b *testing.B) {
	_, priv := benchKeypair(b, sign.Ed25519)
	key := ed25519.PrivateKey(priv)
	message := benchPayload(b, sizeSmall)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bytesSink = ed25519.Sign(key, message)
	}
}

func BenchmarkBareEd25519Verify(b *testing.B) {
	pub, priv := benchKeypair(b, sign.Ed25519)
	key := ed25519.PublicKey(pub)
	message := benchPayload(b, sizeSmall)
	sig := ed25519.Sign(ed25519.PrivateKey(priv), message)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		boolSink = ed25519.Verify(key, message, sig)
	}
}

func BenchmarkBareECDSAP256Sign(b *testing.B) {
	key := benchECDSAPrivate(b)
	digest := sha256.Sum256(benchPayload(b, sizeSmall))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bytesSink, errSink = ecdsa.SignASN1(rand.Reader, key, digest[:])
	}
}

func BenchmarkBareECDSAP256Verify(b *testing.B) {
	key := benchECDSAPrivate(b)
	digest := sha256.Sum256(benchPayload(b, sizeSmall))
	sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		b.Fatalf("SignASN1: %v", err)
	}
	pub := &key.PublicKey
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		boolSink = ecdsa.VerifyASN1(pub, digest[:], sig)
	}
}

// BenchmarkParseECPrivateKey and BenchmarkParsePKIXPublicKey isolate the DER
// decode the SDK pays per call, so the envelope above is attributed rather than
// inferred.
func BenchmarkParseECPrivateKey(b *testing.B) {
	_, priv := benchKeypair(b, sign.ECDSAP256)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		key, err := x509.ParseECPrivateKey(priv)
		errSink = err
		boolSink = key != nil
	}
}

func BenchmarkParsePKIXPublicKey(b *testing.B) {
	pub, _ := benchKeypair(b, sign.ECDSAP256)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		key, err := x509.ParsePKIXPublicKey(pub)
		errSink = err
		boolSink = key != nil
	}
}

// benchECDSAPrivate draws a P-256 key as a live *ecdsa.PrivateKey, bypassing
// the DER round trip. nil selects crypto/rand (Go 1.26+).
func benchECDSAPrivate(b *testing.B) *ecdsa.PrivateKey {
	b.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), nil)
	if err != nil {
		b.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	return key
}
