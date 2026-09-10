package crypto_test

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"io"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/crypto"
)

// The three payload sizes the whole crypto-family table is cut on — a
// token-sized record, a page, and a megabyte — followed by the wire constants
// of the AES-256-GCM box, duplicated here so the bare-primitive benchmarks
// build the SAME framing the SDK does and the comparison stays
// apples-to-apples. Both are frozen post-v1.0.0 (see the package doc).
//
// The RATIO between the first size and the last is the fact this file exists
// for: it is what says whether a cost is paid per call or per byte, which is
// the only thing a caller sizing a hot path needs.
const (
	sizeSmall  int = 64
	sizeMedium int = 4 << 10
	sizeLarge  int = 1 << 20

	boxVersion byte = 0x01
	boxAlgID   byte = 0x01
	nonceLen   int  = 12
	gcmTagLen  int  = 16
	headerLen  int  = 2
)

// Package-level sinks. A sealed box that nobody observes is a value the
// compiler is entitled to prove dead and delete, and a benchmark measuring a
// deleted call reports the cost of an empty loop.
var (
	bytesSink []byte
	strSink   string
	keySink   crypto.Key
	errSink   error
)

// benchKey builds the fixed key every AEAD benchmark seals under, sized from
// [crypto.KeyLen]. It is built ONCE, outside every timed loop: NewKey copies its
// input, and this file measures Seal, not that copy.
func benchKey(b *testing.B) crypto.Key {
	b.Helper()
	raw := make([]byte, crypto.KeyLen)
	for i := range raw {
		raw[i] = byte(i)
	}
	k, err := crypto.NewKey(raw)
	if err != nil {
		b.Fatalf("NewKey: %v", err)
	}
	return k
}

// benchPayload returns n bytes drawn from crypto/rand. GCM's cost does not
// depend on the plaintext's contents; the filling is for realism, never for the
// number.
func benchPayload(b *testing.B, n int) []byte {
	b.Helper()
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		b.Fatalf("rand.Read: %v", err)
	}
	return buf
}

// benchGCM builds the bare stdlib AEAD the SDK dispatches to, once.
func benchGCM(b *testing.B) cipher.AEAD {
	b.Helper()
	block, err := aes.NewCipher(benchKey(b).Bytes())
	if err != nil {
		b.Fatalf("aes.NewCipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		b.Fatalf("cipher.NewGCM: %v", err)
	}
	return gcm
}

// BenchmarkSeal64B through BenchmarkOpen1MiB are the AEAD half of the table.
// b.SetBytes is set on every one of them so the report carries a throughput
// column, which is the actionable form of the number.
func BenchmarkSeal64B(b *testing.B) { benchSeal(b, sizeSmall) }

func BenchmarkSeal4KiB(b *testing.B) { benchSeal(b, sizeMedium) }

func BenchmarkSeal1MiB(b *testing.B) { benchSeal(b, sizeLarge) }

func benchSeal(b *testing.B, n int) {
	b.Helper()
	k := benchKey(b)
	plaintext := benchPayload(b, n)
	b.SetBytes(int64(n))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bytesSink, errSink = crypto.Seal(k, plaintext, nil)
	}
}

func BenchmarkOpen64B(b *testing.B) { benchOpen(b, sizeSmall) }

func BenchmarkOpen4KiB(b *testing.B) { benchOpen(b, sizeMedium) }

func BenchmarkOpen1MiB(b *testing.B) { benchOpen(b, sizeLarge) }

func benchOpen(b *testing.B, n int) {
	b.Helper()
	k := benchKey(b)
	box, err := crypto.Seal(k, benchPayload(b, n), nil)
	if err != nil {
		b.Fatalf("Seal: %v", err)
	}
	b.SetBytes(int64(n))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bytesSink, errSink = crypto.Open(k, box, nil)
	}
}

// BenchmarkBareGCMFreshCipher* runs the SAME stdlib AES-256-GCM, building the
// cipher inside the loop exactly as the service scheme does, and writing the
// same box framing. The gap against Seal is what the SDK adds ON TOP of the key
// schedule: one registry lookup, one interface call, and the Key.Bytes() copy.
func BenchmarkBareGCMFreshCipher64B(b *testing.B) { benchBareFresh(b, sizeSmall) }

func BenchmarkBareGCMFreshCipher1MiB(b *testing.B) { benchBareFresh(b, sizeLarge) }

func benchBareFresh(b *testing.B, n int) {
	b.Helper()
	k := benchKey(b)
	plaintext := benchPayload(b, n)
	b.SetBytes(int64(n))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bytesSink, errSink = bareSeal(k.Bytes(), plaintext, n)
	}
	if errSink != nil {
		b.Fatalf("bareSeal: %v", errSink)
	}
}

// bareSeal is service/crypto/aesgcm.Seal transcribed onto the stdlib with the
// SDK's registry, interface dispatch and typed-error wrapping removed. Keeping
// it a function (rather than an inlined loop body) matters: the SDK's Seal is
// one too, so the escape analysis the two get is the same.
func bareSeal(raw, plaintext []byte, n int) (box []byte, err error) {
	block, berr := aes.NewCipher(raw)
	if berr != nil {
		return nil, berr
	}
	gcm, gerr := cipher.NewGCM(block)
	if gerr != nil {
		return nil, gerr
	}
	var nonce [nonceLen]byte
	if _, rerr := rand.Read(nonce[:]); rerr != nil {
		return nil, rerr
	}
	box = make([]byte, 0, n+nonceLen+gcmTagLen+headerLen)
	box = append(box, boxVersion, boxAlgID)
	box = append(box, nonce[:]...)
	return gcm.Seal(box, nonce[:], plaintext, nil), nil
}

// BenchmarkBareGCMReusedCipher* is the same work with the key schedule hoisted
// out of the loop — the shape the SDK would have if it cached the cipher.AEAD
// per Key. It is the floor the facade is judged against, and the difference
// between it and BareGCMFreshCipher is the price of rebuilding the schedule.
func BenchmarkBareGCMReusedCipher64B(b *testing.B) { benchBareReused(b, sizeSmall) }

func BenchmarkBareGCMReusedCipher1MiB(b *testing.B) { benchBareReused(b, sizeLarge) }

func benchBareReused(b *testing.B, n int) {
	b.Helper()
	gcm := benchGCM(b)
	plaintext := benchPayload(b, n)
	b.SetBytes(int64(n))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bytesSink, errSink = cachedSeal(gcm, plaintext, n)
	}
	if errSink != nil {
		b.Fatalf("cachedSeal: %v", errSink)
	}
}

// cachedSeal is bareSeal with the key schedule hoisted to the caller — the only
// difference between the two, so the delta between their benchmarks is the
// price of rebuilding the schedule and nothing else.
func cachedSeal(gcm cipher.AEAD, plaintext []byte, n int) (box []byte, err error) {
	var nonce [nonceLen]byte
	if _, rerr := rand.Read(nonce[:]); rerr != nil {
		return nil, rerr
	}
	box = make([]byte, 0, n+nonceLen+gcmTagLen+headerLen)
	box = append(box, boxVersion, boxAlgID)
	box = append(box, nonce[:]...)
	return gcm.Seal(box, nonce[:], plaintext, nil), nil
}

// BenchmarkSealAAD4KiB prices the associated-data argument. aad is
// authenticated but not stored, so it should cost the tag pass over its own
// bytes and nothing structural; this is the measurement that says so.
func BenchmarkSealAAD4KiB(b *testing.B) {
	k := benchKey(b)
	plaintext := benchPayload(b, sizeMedium)
	aad := benchPayload(b, sizeSmall)
	b.SetBytes(int64(sizeMedium))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bytesSink, errSink = crypto.Seal(k, plaintext, aad)
	}
}

// BenchmarkOpenTampered4KiB is the refusal path. A verifier is exposed to input
// the caller does not choose, so a refusal that costs MORE than an acceptance
// is an amplification an attacker gets for free.
func BenchmarkOpenTampered4KiB(b *testing.B) {
	k := benchKey(b)
	box, err := crypto.Seal(k, benchPayload(b, sizeMedium), nil)
	if err != nil {
		b.Fatalf("Seal: %v", err)
	}
	box[len(box)-1] ^= 0xFF
	b.SetBytes(int64(sizeMedium))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bytesSink, errSink = crypto.Open(k, box, nil)
	}
}

// BenchmarkSealStream1MiB and BenchmarkOpenStream1MiB price the chunked
// construction against the whole-buffer one at the same size. The streaming
// frame authenticates every 64 KiB chunk separately, so it pays sixteen tags
// where Seal pays one — that is the trade a caller makes for not holding the
// payload in memory, and this is its size.
func BenchmarkSealStream1MiB(b *testing.B) {
	k := benchKey(b)
	plaintext := benchPayload(b, sizeLarge)
	dst := bytes.NewBuffer(make([]byte, 0, sizeLarge+(sizeLarge/(64<<10)+2)*(nonceLen+gcmTagLen)))
	b.SetBytes(int64(sizeLarge))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		dst.Reset()
		w, werr := crypto.SealStream(dst, k, nil)
		if werr != nil {
			b.Fatalf("SealStream: %v", werr)
		}
		if _, cerr := w.Write(plaintext); cerr != nil {
			b.Fatalf("Write: %v", cerr)
		}
		errSink = w.Close()
	}
	bytesSink = dst.Bytes()
}

func BenchmarkOpenStream1MiB(b *testing.B) {
	k := benchKey(b)
	var sealed bytes.Buffer
	w, err := crypto.SealStream(&sealed, k, nil)
	if err != nil {
		b.Fatalf("SealStream: %v", err)
	}
	if _, werr := w.Write(benchPayload(b, sizeLarge)); werr != nil {
		b.Fatalf("Write: %v", werr)
	}
	if cerr := w.Close(); cerr != nil {
		b.Fatalf("Close: %v", cerr)
	}
	stream := sealed.Bytes()
	out := make([]byte, 0, sizeLarge)
	b.SetBytes(int64(sizeLarge))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		r, rerr := crypto.OpenStream(bytes.NewReader(stream), k, nil)
		if rerr != nil {
			b.Fatalf("OpenStream: %v", rerr)
		}
		bytesSink, errSink = readAllInto(out, r)
	}
}

// readAllInto drains r into a reusable buffer so the streaming reader is
// measured, not the growth of a fresh destination slice on every iteration.
func readAllInto(dst []byte, r io.Reader) (plaintext []byte, err error) {
	plaintext = dst[:0]
	buf := make([]byte, 32<<10)
	for {
		n, rerr := r.Read(buf)
		plaintext = append(plaintext, buf[:n]...)
		if rerr == io.EOF {
			return plaintext, nil
		}
		if rerr != nil {
			return plaintext, rerr
		}
	}
}

// BenchmarkNewKey prices the constructor. It copies [crypto.KeyLen] bytes
// defensively, which is the whole point of the type; the number exists so
// nobody hoists it out of a call path believing it is expensive.
func BenchmarkNewKey(b *testing.B) {
	raw := make([]byte, crypto.KeyLen)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		keySink, errSink = crypto.NewKey(raw)
	}
}

// BenchmarkWrapKey and BenchmarkUnwrapKey belong to the SLOW column, and they
// are here rather than in pkg/v1/password because that is where a caller will
// be surprised by them: they stretch a passphrase with PBKDF2-SHA256 at 600 000
// iterations, so they cost what a password hash costs, in a package whose other
// verbs cost microseconds.
func BenchmarkWrapKey(b *testing.B) {
	dek := benchKey(b)
	pass := []byte("correct horse battery staple")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink, errSink = crypto.WrapKey(pass, dek)
	}
}

func BenchmarkUnwrapKey(b *testing.B) {
	pass := []byte("correct horse battery staple")
	envelope, err := crypto.WrapKey(pass, benchKey(b))
	if err != nil {
		b.Fatalf("WrapKey: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		keySink, errSink = crypto.UnwrapKey(pass, envelope)
	}
}
