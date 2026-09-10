package transform_test

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/klauspost/compress/s2"

	coretransform "github.com/kitsunium/sdk/internal/core/transform"
	_ "github.com/kitsunium/sdk/internal/service/transform" // registers the stdlib gzip reference
	tptransform "github.com/kitsunium/sdk/third-party/transform"
)

// benchCorpusSize is the payload size every benchmark runs over. 4 MiB is large
// enough that per-call setup is noise and small enough to stay comfortably
// inside this VM's memory budget.
const benchCorpusSize int = 4 << 20

// benchJSON is the corpus a wire compressor is actually deployed on: repeated
// keys, a bounded vocabulary, high-entropy identifiers. Deterministic, so a
// re-run compares like with like.
var benchJSON = buildJSONCorpus(benchCorpusSize)

// benchRandom is the worst case — already-compressed or encrypted bytes, where
// no scheme can win and the only question is how much CPU it burns proving it.
var benchRandom = buildRandomCorpus(benchCorpusSize)

// buildJSONCorpus renders at least n bytes of deterministic JSON records.
func buildJSONCorpus(n int) []byte {
	r := rand.New(rand.NewPCG(0x5EED, 0xC0FFEE))
	levels := []string{"debug", "info", "warn", "error"}
	svcs := []string{"api-gateway", "billing", "auth", "search", "notifier"}
	msgs := []string{
		"request completed", "cache miss, falling through to origin",
		"token verified", "retry scheduled after transient failure",
		"connection pool saturated, queuing",
	}
	var b bytes.Buffer
	b.Grow(n + 4096)
	ts := int64(1_756_000_000_000)
	for b.Len() < n {
		ts += int64(r.UintN(5000))
		fmt.Fprintf(&b,
			`{"ts":%d,"level":%q,"service":%q,"trace_id":"%032x","span_id":"%016x","msg":%q,"duration_ms":%d,"status":%d}`+"\n",
			ts, levels[r.IntN(len(levels))], svcs[r.IntN(len(svcs))],
			r.Uint64(), r.Uint64(), msgs[r.IntN(len(msgs))],
			r.IntN(2500), 200+r.IntN(5)*100)
	}
	return b.Bytes()
}

// buildRandomCorpus renders n bytes of deterministic pseudo-random data.
func buildRandomCorpus(n int) []byte {
	r := rand.New(rand.NewPCG(0xDEAD, 0xBEEF))
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(r.Uint32())
	}
	return out
}

// benchCorpora pairs each corpus with the name it appears under in BENCH.md.
func benchCorpora() []struct {
	name string
	data []byte
} {
	return []struct {
		name string
		data []byte
	}{{"json", benchJSON}, {"random", benchRandom}}
}

// benchSchemes resolves every compressor under measurement, including the
// stdlib gzip already registered by internal/service/transform. Comparing SDK
// compressor against SDK compressor — same port, same append-to-dst convention,
// same bounded decompression — is the only comparison that answers "which one
// should I register?".
func benchSchemes(tb testing.TB) []struct {
	name string
	c    coretransform.Compressor
} {
	tb.Helper()
	gzip, ok := coretransform.Lookup("gzip")
	if !ok {
		tb.Fatal("gzip is not registered; the blank import is missing")
	}
	zstdFastest, err := tptransform.NewZstdCompressor(tptransform.ZstdFastest, tptransform.DefaultMaxDecompressedBytes)
	if err != nil {
		tb.Fatalf("zstd fastest: %v", err)
	}
	zstdDefault, err := tptransform.NewZstdCompressor(tptransform.ZstdDefault, tptransform.DefaultMaxDecompressedBytes)
	if err != nil {
		tb.Fatalf("zstd default: %v", err)
	}
	zstdBetter, err := tptransform.NewZstdCompressor(tptransform.ZstdBetter, tptransform.DefaultMaxDecompressedBytes)
	if err != nil {
		tb.Fatalf("zstd better: %v", err)
	}
	tb.Cleanup(func() {
		closeChecked(tb, zstdFastest)
		closeChecked(tb, zstdDefault)
		closeChecked(tb, zstdBetter)
	})
	return []struct {
		name string
		c    coretransform.Compressor
	}{
		{"gzip-stdlib", gzip},
		{"zstd-fastest", zstdFastest},
		{"zstd-default", zstdDefault},
		{"zstd-better", zstdBetter},
		{"s2", tptransform.S2},
	}
}

// TestReportRatios is not a benchmark and asserts nothing about speed: it
// prints the compression ratio of every scheme on every corpus, which is the
// axis `go test -bench` cannot report and the one a choice table needs beside
// the throughput. It runs in the ordinary suite because a ratio that silently
// collapsed would be a defect, and it costs a few hundred milliseconds.
func TestReportRatios(t *testing.T) {
	for _, corpus := range benchCorpora() {
		for _, scheme := range benchSchemes(t) {
			out, err := scheme.c.Compress(nil, corpus.data)
			if err != nil {
				t.Fatalf("%s/%s: %v", corpus.name, scheme.name, err)
			}
			t.Logf("RATIO %-7s %-13s in=%d out=%d ratio=%.3f saved=%.1f%%",
				corpus.name, scheme.name, len(corpus.data), len(out),
				float64(len(corpus.data))/float64(len(out)),
				100*(1-float64(len(out))/float64(len(corpus.data))))
		}
	}
}

// BenchmarkCompress reports MB/s of PLAINTEXT for every scheme, which is the
// only unit comparable across schemes that produce different output sizes.
func BenchmarkCompress(b *testing.B) {
	for _, corpus := range benchCorpora() {
		for _, scheme := range benchSchemes(b) {
			b.Run(corpus.name+"/"+scheme.name, func(b *testing.B) {
				b.SetBytes(int64(len(corpus.data)))
				b.ReportAllocs()
				for b.Loop() {
					out, err := scheme.c.Compress(nil, corpus.data)
					if err != nil {
						b.Fatal(err)
					}
					benchSink = out
				}
			})
		}
	}
}

// BenchmarkDecompress reports MB/s of plaintext produced.
func BenchmarkDecompress(b *testing.B) {
	for _, corpus := range benchCorpora() {
		for _, scheme := range benchSchemes(b) {
			encoded, err := scheme.c.Compress(nil, corpus.data)
			if err != nil {
				b.Fatal(err)
			}
			benchDecompress(b, corpus.name+"/"+scheme.name, scheme.c, encoded, len(corpus.data))
		}
	}
}

// BenchmarkS2CompressIntoTail versus BenchmarkS2CompressThenCopy is the
// measurement behind the one optimisation in this package.
//
// s2.Encode treats its first argument as scratch rather than as a prefix, so
// the append-to-dst contract has two honest implementations: encode into a
// fresh buffer and copy the result onto dst, or grow dst and let s2 encode
// straight into its tail. The second is what ships; these two rows are why, and
// the pprof profile behind them is quoted in BENCH.md.
func BenchmarkS2CompressIntoTail(b *testing.B) {
	for _, corpus := range benchCorpora() {
		b.Run(corpus.name, func(b *testing.B) {
			b.SetBytes(int64(len(corpus.data)))
			b.ReportAllocs()
			for b.Loop() {
				out, err := tptransform.S2.Compress(nil, corpus.data)
				if err != nil {
					b.Fatal(err)
				}
				benchSink = out
			}
		})
	}
}

// BenchmarkS2CompressThenCopy is the control: the obvious implementation, kept
// here and nowhere else so the production choice stays justified by a number
// rather than by a comment.
func BenchmarkS2CompressThenCopy(b *testing.B) {
	for _, corpus := range benchCorpora() {
		b.Run(corpus.name, func(b *testing.B) {
			b.SetBytes(int64(len(corpus.data)))
			b.ReportAllocs()
			for b.Loop() {
				//: the naive shape — encode to a fresh buffer, then copy onto dst.
				encoded := s2.Encode(nil, corpus.data)
				benchSink = slices.Clone(encoded)
			}
		})
	}
}

// benchDecompress drives one decompression row. The encoded payload is a
// PARAMETER rather than a captured loop variable so it never escapes to the
// heap through the closure — the measurement would otherwise carry an
// allocation that belongs to the benchmark harness, not to the scheme.
func benchDecompress(b *testing.B, name string, c coretransform.Compressor, encoded []byte, plainLen int) {
	b.Helper()
	b.Run(name, func(b *testing.B) {
		b.SetBytes(int64(plainLen))
		b.ReportAllocs()
		for b.Loop() {
			out, derr := c.Decompress(nil, encoded)
			if derr != nil {
				b.Fatal(derr)
			}
			benchSink = out
		}
	})
}

// benchSink keeps the compiler from eliding the work under measurement.
var benchSink []byte
