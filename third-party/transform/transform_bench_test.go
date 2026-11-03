package transform_test

import (
	"bytes"
	"fmt"
	"io"
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"

	"github.com/klauspost/compress/s2"
	"github.com/klauspost/compress/zstd"

	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
	_ "github.com/kitsunium/sdk/internal/service/data/transform" // registers the stdlib gzip reference
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
// stdlib gzip already registered by internal/service/data/transform. Comparing SDK
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

// BenchmarkDecompressSized is BenchmarkDecompress into a NON-EMPTY dst — a
// prefix already written, the call a loop appending several payloads into one
// buffer makes — whose spare capacity holds the payload (kitsunium/sdk#1163).
// It runs at 4 KiB and 4 MiB of the JSON corpus and at 4 MiB of the random
// one, for zstd and s2: the rows the tail decode makes allocation-free.
func BenchmarkDecompressSized(b *testing.B) {
	zstdFastest, err := tptransform.NewZstdCompressor(tptransform.ZstdFastest, tptransform.DefaultMaxDecompressedBytes)
	if err != nil {
		b.Fatalf("zstd fastest: %v", err)
	}
	b.Cleanup(func() { closeChecked(b, zstdFastest) })
	corpora := []struct {
		name string
		data []byte
	}{{"json-4KiB", benchJSON[:4<<10]}, {"json", benchJSON}, {"random", benchRandom}}
	for _, corpus := range corpora {
		for _, scheme := range []struct {
			name string
			c    coretransform.Compressor
		}{{"zstd-fastest", zstdFastest}, {"s2", tptransform.S2}} {
			encoded, cerr := scheme.c.Compress(nil, corpus.data)
			if cerr != nil {
				b.Fatal(cerr)
			}
			benchDecompressInto(b, corpus.name+"/"+scheme.name, scheme.c, encoded, len(corpus.data))
		}
	}
}

// benchDecompressInto drives one sized row: a 64-byte prefix and spare
// capacity for the payload, reused across iterations.
func benchDecompressInto(b *testing.B, name string, c coretransform.Compressor, encoded []byte, plainLen int) {
	b.Helper()
	b.Run(name, func(b *testing.B) {
		dst := make([]byte, 64, 64+plainLen)
		b.SetBytes(int64(plainLen))
		b.ReportAllocs()
		for b.Loop() {
			out, derr := c.Decompress(dst, encoded)
			if derr != nil {
				b.Fatal(derr)
			}
			benchSink = out
		}
	})
}

// benchDictionaryRecordSizes are the record sizes the dictionary rows run at
// (kitsunium/sdk#900): a log line, a small RPC frame, a page of either — the
// payloads where a generic encoder has too little history to find matches.
var benchDictionaryRecordSizes = []int{256, 1 << 10, 4 << 10}

// benchRecords cuts n records of size bytes out of the JSON corpus, starting
// at offset, so the training records and the measured ones never overlap.
func benchRecords(offset, size, n int) [][]byte {
	records := make([][]byte, 0, n)
	for i := range n {
		start := offset + i*size
		records = append(records, benchJSON[start:start+size])
	}
	return records
}

// BenchmarkZstdDictionary prices a dictionary trained on the JSON corpus
// (kitsunium/sdk#900) against the generic encoder, both at ZstdFastest, on
// records of 256 B, 1 KiB and 4 KiB. Each iteration compresses, or
// decompresses, one record of a rotating set of 64; the ratio is the set's
// plaintext over its compressed bytes.
func BenchmarkZstdDictionary(b *testing.B) {
	for _, size := range benchDictionaryRecordSizes {
		train := benchRecords(0, size, 600)
		cut := len(train) / 3
		dict, err := zstd.BuildDict(zstd.BuildDictOptions{
			ID: 0x0001_0900, Contents: train[cut:], History: bytes.Join(train[:cut], nil),
			Offsets: [3]int{1, 4, 8}, Level: zstd.SpeedFastest,
		})
		if err != nil {
			b.Fatalf("BuildDict: %v", err)
		}
		withDict, err := tptransform.NewZstdCompressorWithDictionary(tptransform.ZstdFastest, tptransform.DefaultMaxDecompressedBytes, dict)
		if err != nil {
			b.Fatalf("NewZstdCompressorWithDictionary: %v", err)
		}
		plain, err := tptransform.NewZstdCompressor(tptransform.ZstdFastest, tptransform.DefaultMaxDecompressedBytes)
		if err != nil {
			b.Fatalf("NewZstdCompressor: %v", err)
		}
		b.Cleanup(func() {
			closeChecked(b, withDict)
			closeChecked(b, plain)
		})
		records := benchRecords(600*size, size, 64)
		for _, arm := range []struct {
			name string
			c    *tptransform.ZstdCompressor
		}{{"plain", plain}, {"dictionary", withDict}} {
			frames := make([][]byte, len(records))
			total := 0
			for i, record := range records {
				frames[i], err = arm.c.Compress(nil, record)
				if err != nil {
					b.Fatal(err)
				}
				total += len(frames[i])
			}
			ratio := float64(len(records)*size) / float64(total)
			b.Run("compress/"+strconv.Itoa(size)+"/"+arm.name, func(b *testing.B) {
				b.SetBytes(int64(size))
				b.ReportAllocs()
				i := 0
				for b.Loop() {
					out, cerr := arm.c.Compress(benchSink[:0], records[i%len(records)])
					if cerr != nil {
						b.Fatal(cerr)
					}
					benchSink = out
					i++
				}
				b.ReportMetric(ratio, "ratio")
			})
			b.Run("decompress/"+strconv.Itoa(size)+"/"+arm.name, func(b *testing.B) {
				b.SetBytes(int64(size))
				b.ReportAllocs()
				i := 0
				for b.Loop() {
					out, derr := arm.c.Decompress(benchSink[:0], frames[i%len(frames)])
					if derr != nil {
						b.Fatal(derr)
					}
					benchSink = out
					i++
				}
			})
		}
	}
}

// BenchmarkStream prices the stream form of zstd and s2 (ADR 0213,
// kitsunium/sdk#892) against their block form on both 4 MiB corpora: a
// compress through NewWriter into a buffer against Compress, and a decompress
// through NewReader drained to io.Discard against Decompress into nil.
func BenchmarkStream(b *testing.B) {
	for _, corpus := range benchCorpora() {
		for _, scheme := range []struct {
			name string
			c    coretransform.Compressor
		}{{"zstd-fastest", tptransform.Zstd}, {"s2", tptransform.S2}} {
			streaming := scheme.c.(coretransform.StreamingCompressor)
			block, err := scheme.c.Compress(nil, corpus.data)
			if err != nil {
				b.Fatal(err)
			}
			var framed bytes.Buffer
			w, err := streaming.NewWriter(&framed)
			if err != nil {
				b.Fatal(err)
			}
			if _, err = w.Write(corpus.data); err != nil {
				b.Fatal(err)
			}
			if err = w.Close(); err != nil {
				b.Fatal(err)
			}
			benchStreamRows(b, corpus.name+"/"+scheme.name, scheme.c, streaming, corpus.data, block, framed.Bytes())
		}
	}
}

// benchStreamRows runs the four rows of one scheme on one corpus. The inputs
// are parameters rather than captured loop variables, so they do not escape
// through the closures into the measurement.
func benchStreamRows(b *testing.B, name string, c coretransform.Compressor, streaming coretransform.StreamingCompressor, plain, block, framed []byte) {
	b.Helper()
	b.Run(name+"/compress/block", func(b *testing.B) {
		b.SetBytes(int64(len(plain)))
		b.ReportAllocs()
		for b.Loop() {
			out, err := c.Compress(nil, plain)
			if err != nil {
				b.Fatal(err)
			}
			benchSink = out
		}
	})
	b.Run(name+"/compress/stream", func(b *testing.B) {
		var sink bytes.Buffer
		b.SetBytes(int64(len(plain)))
		b.ReportAllocs()
		for b.Loop() {
			sink.Reset()
			w, err := streaming.NewWriter(&sink)
			if err != nil {
				b.Fatal(err)
			}
			if _, err = w.Write(plain); err != nil {
				b.Fatal(err)
			}
			if err = w.Close(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run(name+"/decompress/block", func(b *testing.B) {
		b.SetBytes(int64(len(plain)))
		b.ReportAllocs()
		for b.Loop() {
			out, err := c.Decompress(nil, block)
			if err != nil {
				b.Fatal(err)
			}
			benchSink = out
		}
	})
	b.Run(name+"/decompress/stream", func(b *testing.B) {
		src := bytes.NewReader(framed)
		b.SetBytes(int64(len(plain)))
		b.ReportAllocs()
		for b.Loop() {
			src.Reset(framed)
			r, err := streaming.NewReader(src, int64(len(plain)))
			if err != nil {
				b.Fatal(err)
			}
			if _, err = io.Copy(io.Discard, r); err != nil {
				b.Fatal(err)
			}
			if err = r.Close(); err != nil {
				b.Fatal(err)
			}
		}
	})
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
