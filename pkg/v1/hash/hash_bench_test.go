package hash_test

import (
	"crypto/rand"
	"crypto/sha256"
	"io"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/hash"
)

// The three payload sizes the whole crypto-family table is cut on. The ratio
// between the first and the last is what says whether a cost is per call or per
// byte — for a hash it must be per byte, and these numbers are how a reader
// checks that rather than assuming it.
// sizeCross sits between them for one reason: FNV-1a is cheaper than SHA-256 at
// sizeSmall and dearer at sizeMedium, so the two curves cross somewhere in
// between. Solving the two fixed/per-byte fits puts the crossing below
// sizeCross, which is the nearest round size ABOVE that estimate — so SHA-256
// winning at sizeCross is the measurement rather than the arithmetic.
const (
	sizeSmall  int = 64
	sizeCross  int = 256
	sizeMedium int = 4 << 10
	sizeLarge  int = 1 << 20
)

// Package-level sinks. A digest nobody observes is a value the compiler may
// prove dead and delete, and a benchmark over a deleted call measures an empty
// loop.
var (
	bytesSink []byte
	strSink   string
	intSink   int64
	errSink   error
)

// benchPayload returns n bytes drawn from crypto/rand. No shipped hash's cost
// depends on its input's contents; the filling is for realism, not the number.
func benchPayload(b *testing.B, n int) []byte {
	b.Helper()
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		b.Fatalf("rand.Read: %v", err)
	}
	return buf
}

// benchSum is the shared body of every Sum benchmark: one algorithm, one size,
// throughput reported.
func benchSum(b *testing.B, a hash.Algorithm, n int) {
	b.Helper()
	data := benchPayload(b, n)
	b.SetBytes(int64(n))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bytesSink, errSink = hash.Sum(a, data)
	}
}

// BenchmarkSumSHA256_64B through BenchmarkSumFNV1a64_1MiB are the point of this
// file: five algorithms over one workload at three sizes. Three of them are
// collision-resistant and two are not, and the price of that property is the
// column nobody had published.
func BenchmarkSumSHA256_64B(b *testing.B) { benchSum(b, hash.SHA256, sizeSmall) }

func BenchmarkSumSHA256_4KiB(b *testing.B) { benchSum(b, hash.SHA256, sizeMedium) }

func BenchmarkSumSHA256_1MiB(b *testing.B) { benchSum(b, hash.SHA256, sizeLarge) }

func BenchmarkSumSHA512_64B(b *testing.B) { benchSum(b, hash.SHA512, sizeSmall) }

func BenchmarkSumSHA512_4KiB(b *testing.B) { benchSum(b, hash.SHA512, sizeMedium) }

func BenchmarkSumSHA512_1MiB(b *testing.B) { benchSum(b, hash.SHA512, sizeLarge) }

func BenchmarkSumSHA3256_64B(b *testing.B) { benchSum(b, hash.SHA3256, sizeSmall) }

func BenchmarkSumSHA3256_4KiB(b *testing.B) { benchSum(b, hash.SHA3256, sizeMedium) }

func BenchmarkSumSHA3256_1MiB(b *testing.B) { benchSum(b, hash.SHA3256, sizeLarge) }

func BenchmarkSumCRC32C_64B(b *testing.B) { benchSum(b, hash.CRC32C, sizeSmall) }

func BenchmarkSumCRC32C_4KiB(b *testing.B) { benchSum(b, hash.CRC32C, sizeMedium) }

func BenchmarkSumCRC32C_1MiB(b *testing.B) { benchSum(b, hash.CRC32C, sizeLarge) }

func BenchmarkSumFNV1a64_64B(b *testing.B) { benchSum(b, hash.FNV1a64, sizeSmall) }

func BenchmarkSumFNV1a64_4KiB(b *testing.B) { benchSum(b, hash.FNV1a64, sizeMedium) }

func BenchmarkSumFNV1a64_1MiB(b *testing.B) { benchSum(b, hash.FNV1a64, sizeLarge) }

// BenchmarkSumSHA256_256B and BenchmarkSumFNV1a64_256B bracket the crossover.
// The "fast non-cryptographic fingerprint" is only faster below it: FNV-1a is a
// byte-at-a-time loop in portable Go, and SHA-256 on this CPU is a hardware
// instruction, so the cryptographic hash overtakes the cheap one at a size a
// cache key can easily exceed.
func BenchmarkSumSHA256_256B(b *testing.B) { benchSum(b, hash.SHA256, sizeCross) }

func BenchmarkSumFNV1a64_256B(b *testing.B) { benchSum(b, hash.FNV1a64, sizeCross) }

// BenchmarkSumHexSHA256_4KiB against BenchmarkSumSHA256_4KiB prices the hex
// rendering alone. SumHex is the frozen string form a content ID is stored in,
// so a caller who stores one pays this on every write.
func BenchmarkSumHexSHA256_4KiB(b *testing.B) {
	data := benchPayload(b, sizeMedium)
	b.SetBytes(int64(sizeMedium))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink, errSink = hash.SumHex(hash.SHA256, data)
	}
}

// BenchmarkBareSHA256_64B and BenchmarkBareSHA256_1MiB are sha256.Sum256 with
// no SDK between the caller and the primitive. The gap against Sum is the
// facade's envelope: one registry lookup, one interface call, one fresh
// hash.Hash and the returned slice.
func BenchmarkBareSHA256_64B(b *testing.B) { benchBareSHA256(b, sizeSmall) }

func BenchmarkBareSHA256_1MiB(b *testing.B) { benchBareSHA256(b, sizeLarge) }

func benchBareSHA256(b *testing.B, n int) {
	b.Helper()
	data := benchPayload(b, n)
	var digest [sha256.Size]byte
	b.SetBytes(int64(n))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		digest = sha256.Sum256(data)
	}
	bytesSink = digest[:]
}

// BenchmarkNewStreamSHA256_1MiB drives the streaming hash.Hash over the same
// megabyte in 32 KiB writes — the io.Copy shape. It should land on Sum's
// throughput, which is the check that streaming costs nothing extra.
func BenchmarkNewStreamSHA256_1MiB(b *testing.B) {
	data := benchPayload(b, sizeLarge)
	b.SetBytes(int64(sizeLarge))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		h, err := hash.New(hash.SHA256)
		if err != nil {
			b.Fatalf("New: %v", err)
		}
		for off := 0; off < len(data); off += 32 << 10 {
			end := min(off+32<<10, len(data))
			if _, werr := h.Write(data[off:end]); werr != nil {
				b.Fatalf("Write: %v", werr)
			}
		}
		bytesSink = h.Sum(nil)
	}
}

// BenchmarkDigestWriterSHA256_1MiB prices content-addressing WHILE writing: the
// tee costs one hash pass on top of the destination write, and io.Discard makes
// the destination free so the number is the hash pass alone.
func BenchmarkDigestWriterSHA256_1MiB(b *testing.B) {
	data := benchPayload(b, sizeLarge)
	b.SetBytes(int64(sizeLarge))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		w, err := hash.NewDigestWriter(hash.SHA256, io.Discard)
		if err != nil {
			b.Fatalf("NewDigestWriter: %v", err)
		}
		n, werr := w.Write(data)
		if werr != nil {
			b.Fatalf("Write: %v", werr)
		}
		intSink = int64(n)
		strSink = w.SumHex()
	}
}
