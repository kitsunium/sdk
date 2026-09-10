package mac_test

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/mac"
)

// The three payload sizes the whole crypto-family table is cut on.
const (
	sizeSmall  int = 64
	sizeMedium int = 4 << 10
	sizeLarge  int = 1 << 20
)

// Package-level sinks. A tag nobody observes is a value the compiler may prove
// dead and delete, and a benchmark over a deleted call measures an empty loop.
var (
	bytesSink []byte
	boolSink  bool
	errSink   error
)

// benchKey builds the fixed 32-byte key every MAC benchmark tags under, ONCE,
// outside every timed loop.
func benchKey(b *testing.B) mac.Key {
	b.Helper()
	raw := make([]byte, mac.KeyLen)
	for i := range raw {
		raw[i] = byte(i)
	}
	k, err := mac.NewKey(raw)
	if err != nil {
		b.Fatalf("NewKey: %v", err)
	}
	return k
}

// benchPayload returns n bytes drawn from crypto/rand.
func benchPayload(b *testing.B, n int) []byte {
	b.Helper()
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		b.Fatalf("rand.Read: %v", err)
	}
	return buf
}

// BenchmarkTag64B through BenchmarkTag1MiB are the MAC half of the table.
func BenchmarkTag64B(b *testing.B) { benchTag(b, sizeSmall) }

func BenchmarkTag4KiB(b *testing.B) { benchTag(b, sizeMedium) }

func BenchmarkTag1MiB(b *testing.B) { benchTag(b, sizeLarge) }

func benchTag(b *testing.B, n int) {
	b.Helper()
	k := benchKey(b)
	message := benchPayload(b, n)
	b.SetBytes(int64(n))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bytesSink, errSink = mac.Tag(mac.HMACSHA256, k, message)
	}
}

// BenchmarkVerify* is the read side. Verify recomputes the tag and then
// compares in constant time, so it should cost Tag plus a 32-byte compare — and
// a WRONG tag must cost the same as a right one, which is what the constant-time
// comparison buys. BenchmarkVerifyBadTag4KiB is that check.
func BenchmarkVerify64B(b *testing.B) { benchVerify(b, sizeSmall, true) }

func BenchmarkVerify4KiB(b *testing.B) { benchVerify(b, sizeMedium, true) }

func BenchmarkVerifyBadTag4KiB(b *testing.B) { benchVerify(b, sizeMedium, false) }

func benchVerify(b *testing.B, n int, valid bool) {
	b.Helper()
	k := benchKey(b)
	message := benchPayload(b, n)
	tag, err := mac.Tag(mac.HMACSHA256, k, message)
	if err != nil {
		b.Fatalf("Tag: %v", err)
	}
	if !valid {
		tag[0] ^= 0xFF
	}
	b.SetBytes(int64(n))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		boolSink, errSink = mac.Verify(mac.HMACSHA256, k, message, tag)
	}
}

// BenchmarkBareHMAC* is crypto/hmac driven directly, building the keyed hash
// per call exactly as the service scheme does. The gap against Tag is the
// facade's envelope: a registry lookup, an interface call and the Key.Bytes()
// copy.
func BenchmarkBareHMAC64B(b *testing.B) { benchBareHMAC(b, sizeSmall) }

func BenchmarkBareHMAC1MiB(b *testing.B) { benchBareHMAC(b, sizeLarge) }

func benchBareHMAC(b *testing.B, n int) {
	b.Helper()
	raw := benchKey(b).Bytes()
	message := benchPayload(b, n)
	b.SetBytes(int64(n))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		h := hmac.New(sha256.New, raw)
		h.Write(message)
		bytesSink = h.Sum(nil)
	}
}

// BenchmarkBareSHA256_64B and BenchmarkBareSHA256_1MiB are the UNKEYED hash of
// the same bytes, measured in this package so both halves of the comparison
// come out of one run on one machine. Tag minus this is what authentication
// costs — the question this file exists to answer.
func BenchmarkBareSHA256_64B(b *testing.B) { benchBareSHA256(b, sizeSmall) }

func BenchmarkBareSHA256_1MiB(b *testing.B) { benchBareSHA256(b, sizeLarge) }

func benchBareSHA256(b *testing.B, n int) {
	b.Helper()
	message := benchPayload(b, n)
	var digest [sha256.Size]byte
	b.SetBytes(int64(n))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		digest = sha256.Sum256(message)
	}
	bytesSink = digest[:]
}
