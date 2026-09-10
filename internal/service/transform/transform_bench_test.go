package transform

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/binary"
	"io"
	"math/rand/v2"
	"strconv"
	"testing"

	coretransform "github.com/kitsunium/sdk/internal/core/transform"
)

const (
	// benchLine is one repetition of the highly-compressible corpus: a realistic
	// structured log line, which is what a caller actually compresses.
	benchLine string = `{"ts":"2026-09-10T14:36:05Z","level":"info","msg":"request served","route":"/v1/orders","status":200,"dur_ms":7}` + "\n"
	// benchPairSize is the payload the round-trip and reused-dst rows use. It is
	// the smallest size at which the per-byte cost is already visible, so the
	// round trip can be checked against the sum of its two one-way rows.
	benchPairSize int = 4 << 10
	// benchLevelSize is the payload the zlib level sweep runs at. Levels differ
	// by how far they search the window, so the sweep needs a payload several
	// windows long or every level measures the same startup.
	benchLevelSize int = 64 << 10
	// benchPoolSize is the payload the pool rows run at: small enough that the
	// per-call fixed cost the pool removes is the dominant term, which is the
	// whole point of those rows.
	benchPoolSize int = 256
	// benchBombPlain is the plaintext a decompression bomb expands to. It is the
	// constant the refusal rows must be shown NOT to depend on.
	benchBombPlain int = 32 << 20
)

var (
	// benchSizes are the payload sizes the report is built from. 64 B is below
	// io.ReadAll's 512-byte floor, which is where the decompression tail stops
	// behaving like the larger sizes; 256 B is where the per-call fixed cost (a
	// stdlib writer's history window and hash tables) is visible undiluted;
	// 1 MiB is where the per-byte cost dominates and the fixed cost is rounding
	// error. The two middle sizes show where the crossover sits.
	benchSizes = []int{64, 256, 4 << 10, 64 << 10, 1 << 20}
	// benchTailSizes are the sizes the decompression tail is measured at. They
	// straddle io.ReadAll's 512-byte floor deliberately, because that floor is
	// what decides whether skipping the append saves anything at all.
	benchTailSizes = []int{64, 256, 4 << 10, 1 << 20}
	// benchBombCaps are the ceilings the refusal path is measured at. The
	// production ceiling is 256 MiB; these stand in for it so the cost can be
	// shown to scale with the CAP and not with the bomb's declared expansion.
	benchBombCaps = []int64{64 << 10, 1 << 20, 8 << 20}
	// benchCorpora is the compressibility axis.
	benchCorpora = []benchCorpus{
		{"random", benchIncompressible},
		{"text", benchRepetitive},
	}
	// benchSchemes is the algorithm axis, taken from the registered singletons so
	// the benchmark measures exactly what a caller resolves through the registry.
	benchSchemes = []benchScheme{
		{"gzip", GzipCompressor},
		{"flate", FlateCompressor},
		{"zlib", ZlibCompressor},
	}
	// benchLevels is the level axis of NewZlibCompressor. HuffmanOnly and
	// NoCompression are included because they are the two the constructor treats
	// specially: one is a real (fast, weak) mode, the other is clamped away.
	benchLevels = []benchLevel{
		{"huffman-only", zlib.HuffmanOnly},
		{"best-speed", zlib.BestSpeed},
		{"default", zlib.DefaultCompression},
		{"level-6", 6},
		{"best-compression", zlib.BestCompression},
		{"no-compression-clamped", zlib.NoCompression},
	}
	// benchStealers empties one scheme's writer AND reader pool, which is how a
	// cold-pool row is produced without touching production code: the take that
	// follows finds nothing and constructs, exactly as the first call on a P
	// does. The stolen objects go to the sinks so nothing is elided.
	benchStealers = map[string]func(){
		"gzip":  benchStealGzip,
		"flate": benchStealFlate,
		"zlib":  benchStealZlib,
	}
	// benchSinkBytes and benchSinkErr keep a measured result from being elided.
	benchSinkBytes []byte
	benchSinkErr   error
	// benchSinkGzipWriter, benchSinkFlateWriter and benchSinkZlibWriter hold the
	// encoders a cold row steals out of the pools.
	benchSinkGzipWriter  *gzip.Writer
	benchSinkFlateWriter *flate.Writer
	benchSinkZlibWriter  *zlib.Writer
	// benchSinkBox holds the decoder box a cold row steals out of the pools.
	benchSinkBox *readerBox
)

// benchCorpus pairs a compressibility regime with its generator. The two
// regimes behave so differently that a report quoting only one is misleading:
// on incompressible input DEFLATE stores rather than encodes and the output is
// LARGER than the input.
type benchCorpus struct {
	name string
	make func(size int) []byte
}

// benchScheme pairs a wire format's name with the registered singleton that
// implements it, so every row measures what a caller actually resolves.
type benchScheme struct {
	name string
	comp coretransform.Compressor
}

// benchLevel pairs a zlib level with the name the report prints it under.
type benchLevel struct {
	name  string
	level int
}

// benchIncompressible returns size bytes no DEFLATE window can shorten. The
// generator is seeded deterministically so two runs of this file compress
// literally the same bytes and their numbers are comparable.
func benchIncompressible(size int) []byte {
	gen := rand.New(rand.NewPCG(0x5DEECE66D, 0xB))
	out := make([]byte, 0, size+8)
	var word [8]byte
	for len(out) < size {
		binary.LittleEndian.PutUint64(word[:], gen.Uint64())
		out = append(out, word[:]...)
	}
	//: trim the last partial word so every corpus is exactly size bytes.
	return out[:size]
}

// benchRepetitive returns size bytes of the log-line corpus, which DEFLATE
// shortens by roughly two orders of magnitude.
func benchRepetitive(size int) []byte {
	line := []byte(benchLine)
	out := bytes.Repeat(line, size/len(line)+1)
	//: same exact-size rule as the incompressible corpus.
	return out[:size]
}

// benchStealGzip empties the gzip writer and reader pools.
func benchStealGzip() {
	//: the encoder the next Compress would have reused.
	benchSinkGzipWriter = gzipWriterPool.Get()
	//: the box the next Decompress would have reused.
	benchSinkBox = gzipReaderPool.Get()
}

// benchStealFlate empties the flate writer and reader pools.
func benchStealFlate() {
	//: the encoder the next Compress would have reused.
	benchSinkFlateWriter = flateWriterPool.Get()
	//: the box the next Decompress would have reused.
	benchSinkBox = flateReaderPool.Get()
}

// benchStealZlib empties the zlib writer pool at the level the singleton
// encodes at, and the zlib reader pool.
func benchStealZlib() {
	//: the singleton is built at DefaultCompression, so that is the pool to empty.
	benchSinkZlibWriter = zlibWriterPoolFor(zlib.DefaultCompression).Get()
	//: the box the next Decompress would have reused.
	benchSinkBox = zlibReaderPool.Get()
}

// benchDecompressAppending is the production decompression tail, replicated
// here so the refused fast path below can be measured against a function that
// differs from it in exactly one branch and nothing else. Its numbers are
// checked against BenchmarkDecompress/gzip in the report; a divergence means
// the replica has drifted and the comparison is void.
func benchDecompressAppending(dst, src []byte) (decoded []byte, err error) {
	box, rerr := takeGzipReader(bytes.NewReader(src))
	//: a refused header is the caller's stream, exactly as in production.
	if rerr != nil {
		//: hand the fault back unwrapped; this replica is not the error path.
		return dst, rerr
	}
	//: return the decoder on every exit below.
	defer releaseGzipReader(box)
	plain, overflow, derr := readAllBounded(box.rc, maxDecompressedBytes)
	//: fold a Close fault into the result, as production does.
	if cerr := box.rc.Close(); cerr != nil && derr == nil {
		derr = cerr
	}
	//: a read/close fault is a failure.
	if derr != nil {
		//: hand the fault back unwrapped.
		return dst, derr
	}
	//: an over-cap stream is a failure, never an OOM.
	if overflow {
		//: the sentinel production returns here.
		return dst, GzipFailed
	}
	//: the append-to-dst contract, honoured.
	return append(dst, plain...), nil
}

// benchDecompressRefusedFastPath is benchDecompressAppending plus the one
// branch that was proposed and then REMOVED: when dst has no capacity there is
// nothing to append to, so the freshly-read buffer could be returned as-is.
// It was refused because it abandons the append-to-dst contract the port
// documents in three places, no test was sensitive to it (every call site
// passes dst=nil), and it makes the append branch dead code. It is kept here,
// unexported and unreachable from production, so the refusal ships with its
// number instead of with an assertion.
func benchDecompressRefusedFastPath(dst, src []byte) (decoded []byte, err error) {
	box, rerr := takeGzipReader(bytes.NewReader(src))
	//: a refused header is the caller's stream, exactly as in production.
	if rerr != nil {
		//: hand the fault back unwrapped; this replica is not the error path.
		return dst, rerr
	}
	//: return the decoder on every exit below.
	defer releaseGzipReader(box)
	plain, overflow, derr := readAllBounded(box.rc, maxDecompressedBytes)
	//: fold a Close fault into the result, as production does.
	if cerr := box.rc.Close(); cerr != nil && derr == nil {
		derr = cerr
	}
	//: a read/close fault is a failure.
	if derr != nil {
		//: hand the fault back unwrapped.
		return dst, derr
	}
	//: an over-cap stream is a failure, never an OOM.
	if overflow {
		//: the sentinel production returns here.
		return dst, GzipFailed
	}
	//: THE REFUSED BRANCH — nothing to append to, so skip the copy entirely.
	if cap(dst) == 0 {
		//: hand the reader's own buffer straight out.
		return plain, nil
	}
	//: the append-to-dst contract, honoured.
	return append(dst, plain...), nil
}

// benchEach runs one verb across the whole algorithm x corpus x size matrix.
func benchEach(b *testing.B, run func(b *testing.B, comp coretransform.Compressor, payload []byte)) {
	b.Helper()
	for _, scheme := range benchSchemes {
		for _, corpus := range benchCorpora {
			for _, size := range benchSizes {
				name := scheme.name + "/" + corpus.name + "/" + strconv.Itoa(size)
				b.Run(name, func(b *testing.B) {
					payload := corpus.make(size)
					b.SetBytes(int64(size))
					b.ReportAllocs()
					run(b, scheme.comp, payload)
				})
			}
		}
	}
}

// BenchmarkCompress measures one-way encoding across the full matrix and
// reports the achieved ratio, so the CPU column and the size column of the
// report come from the same run.
func BenchmarkCompress(b *testing.B) {
	benchEach(b, func(b *testing.B, comp coretransform.Compressor, payload []byte) {
		var out []byte
		var err error
		b.ResetTimer()
		for range b.N {
			out, err = comp.Compress(nil, payload)
			if err != nil {
				b.Fatalf("Compress: %v", err)
			}
		}
		b.StopTimer()
		benchSinkBytes = out
		b.ReportMetric(float64(len(out))/float64(len(payload)), "ratio")
	})
}

// BenchmarkDecompress measures one-way decoding across the full matrix. The
// compressed input is built outside the timer, so this row is the reader plus
// the bounded drain and nothing else.
func BenchmarkDecompress(b *testing.B) {
	benchEach(b, func(b *testing.B, comp coretransform.Compressor, payload []byte) {
		encoded, err := comp.Compress(nil, payload)
		if err != nil {
			b.Fatalf("Compress: %v", err)
		}
		var out []byte
		b.ResetTimer()
		for range b.N {
			out, err = comp.Decompress(nil, encoded)
			if err != nil {
				b.Fatalf("Decompress: %v", err)
			}
		}
		b.StopTimer()
		benchSinkBytes = out
	})
}

// BenchmarkRoundTrip measures compress-then-decompress at one size, so the
// report can check its own arithmetic: a round trip must cost the sum of the
// two one-way rows, and a row that does not is noise.
func BenchmarkRoundTrip(b *testing.B) {
	for _, scheme := range benchSchemes {
		for _, corpus := range benchCorpora {
			b.Run(scheme.name+"/"+corpus.name+"/"+strconv.Itoa(benchPairSize), func(b *testing.B) {
				payload := corpus.make(benchPairSize)
				comp := scheme.comp
				b.SetBytes(int64(benchPairSize))
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					encoded, cerr := comp.Compress(nil, payload)
					if cerr != nil {
						b.Fatalf("Compress: %v", cerr)
					}
					plain, derr := comp.Decompress(nil, encoded)
					if derr != nil {
						b.Fatalf("Decompress: %v", derr)
					}
					benchSinkBytes = plain
				}
			})
		}
	}
}

// BenchmarkZlibLevel prices the one caller-visible knob in this package across
// both compressibility regimes, and reports the ratio each level buys.
func BenchmarkZlibLevel(b *testing.B) {
	for _, lvl := range benchLevels {
		for _, corpus := range benchCorpora {
			b.Run(lvl.name+"/"+corpus.name, func(b *testing.B) {
				comp := NewZlibCompressor(lvl.level)
				payload := corpus.make(benchLevelSize)
				var out []byte
				var err error
				b.SetBytes(int64(benchLevelSize))
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					out, err = comp.Compress(nil, payload)
					if err != nil {
						b.Fatalf("Compress: %v", err)
					}
				}
				b.StopTimer()
				benchSinkBytes = out
				b.ReportMetric(float64(len(out))/float64(benchLevelSize), "ratio")
			})
		}
	}
}

// BenchmarkCompressReusedDst measures the append-to-dst convention actually
// being used: the caller hands back a buffer with spare capacity instead of nil.
// This is the only lever a caller has over this package's allocation profile
// without changing algorithm or level.
func BenchmarkCompressReusedDst(b *testing.B) {
	for _, scheme := range benchSchemes {
		for _, corpus := range benchCorpora {
			b.Run(scheme.name+"/"+corpus.name, func(b *testing.B) {
				payload := corpus.make(benchPairSize)
				comp := scheme.comp
				scratch := make([]byte, 0, 2*benchPairSize)
				b.SetBytes(int64(benchPairSize))
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					out, err := comp.Compress(scratch[:0], payload)
					if err != nil {
						b.Fatalf("Compress: %v", err)
					}
					benchSinkBytes = out
				}
			})
		}
	}
}

// BenchmarkCompressPoolPath is the pooling change priced end to end. The warm
// arm is steady state — every take hits. The cold arm empties the pool
// immediately before each call, so every take misses and constructs, which is
// what every call did before pool.go existed. The cold arm carries one extra
// recycler.Pool.Get (the steal); BenchmarkPoolSteal prices it so the row can be
// corrected rather than trusted.
func BenchmarkCompressPoolPath(b *testing.B) {
	for _, scheme := range benchSchemes {
		b.Run(scheme.name+"/warm", func(b *testing.B) {
			payload := benchRepetitive(benchPoolSize)
			comp := scheme.comp
			b.SetBytes(int64(benchPoolSize))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				out, err := comp.Compress(nil, payload)
				if err != nil {
					b.Fatalf("Compress: %v", err)
				}
				benchSinkBytes = out
			}
		})
		b.Run(scheme.name+"/cold", func(b *testing.B) {
			payload := benchRepetitive(benchPoolSize)
			steal := benchStealers[scheme.name]
			comp := scheme.comp
			b.SetBytes(int64(benchPoolSize))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				steal()
				out, err := comp.Compress(nil, payload)
				if err != nil {
					b.Fatalf("Compress: %v", err)
				}
				benchSinkBytes = out
			}
		})
	}
}

// BenchmarkDecompressPoolPath is the same comparison for the decoder half,
// where the pooled object is a boxed io.ReadCloser rather than an encoder and
// the miss also allocates the box.
func BenchmarkDecompressPoolPath(b *testing.B) {
	for _, scheme := range benchSchemes {
		b.Run(scheme.name+"/warm", func(b *testing.B) {
			comp := scheme.comp
			encoded, err := comp.Compress(nil, benchRepetitive(benchPoolSize))
			if err != nil {
				b.Fatalf("Compress: %v", err)
			}
			b.SetBytes(int64(benchPoolSize))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				plain, derr := comp.Decompress(nil, encoded)
				if derr != nil {
					b.Fatalf("Decompress: %v", derr)
				}
				benchSinkBytes = plain
			}
		})
		b.Run(scheme.name+"/cold", func(b *testing.B) {
			comp := scheme.comp
			steal := benchStealers[scheme.name]
			encoded, err := comp.Compress(nil, benchRepetitive(benchPoolSize))
			if err != nil {
				b.Fatalf("Compress: %v", err)
			}
			b.SetBytes(int64(benchPoolSize))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				steal()
				plain, derr := comp.Decompress(nil, encoded)
				if derr != nil {
					b.Fatalf("Decompress: %v", derr)
				}
				benchSinkBytes = plain
			}
		})
	}
}

// BenchmarkPoolSteal prices the instrument the cold rows are made with: one
// recycler.Pool.Get plus one Put against a warm pool. A cold row carries at
// most this much measurement overhead, and the report subtracts it rather than
// hoping it is small.
func BenchmarkPoolSteal(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		benchSinkGzipWriter = gzipWriterPool.Get()
		gzipWriterPool.Put(benchSinkGzipWriter)
	}
}

// BenchmarkBoundedRead isolates what the decompression bound costs against an
// unbounded io.ReadAll over the same bytes: the difference is one
// io.LimitReader and one length comparison, and the report states it as a
// percentage of a decompress rather than in the abstract.
func BenchmarkBoundedRead(b *testing.B) {
	for _, size := range benchSizes {
		b.Run("bounded/"+strconv.Itoa(size), func(b *testing.B) {
			payload := benchRepetitive(size)
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				plain, overflow, err := readAllBounded(bytes.NewReader(payload), maxDecompressedBytes)
				if err != nil || overflow {
					b.Fatalf("readAllBounded: err=%v overflow=%v", err, overflow)
				}
				benchSinkBytes = plain
			}
		})
		b.Run("unbounded/"+strconv.Itoa(size), func(b *testing.B) {
			payload := benchRepetitive(size)
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				plain, err := io.ReadAll(bytes.NewReader(payload))
				if err != nil {
					b.Fatalf("ReadAll: %v", err)
				}
				benchSinkBytes = plain
			}
		})
	}
}

// BenchmarkDecompressOverflowRefusal measures what an over-cap stream costs the
// process that refuses it. The input is a 32 MiB-expanding bomb built once; the
// cap varies. If the bound works, the cost tracks the cap and never the bomb.
func BenchmarkDecompressOverflowRefusal(b *testing.B) {
	bomb, err := GzipCompressor.Compress(nil, make([]byte, benchBombPlain))
	if err != nil {
		b.Fatalf("building bomb: %v", err)
	}
	b.Logf("bomb: %d compressed bytes -> %d plaintext (%.0fx)", len(bomb), benchBombPlain, float64(benchBombPlain)/float64(len(bomb)))
	for _, capBytes := range benchBombCaps {
		b.Run(strconv.FormatInt(capBytes>>10, 10)+"KiB-cap", func(b *testing.B) {
			b.SetBytes(capBytes)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				_, rerr := gzipDecompress(nil, bomb, capBytes)
				if rerr == nil {
					b.Fatal("over-cap stream was accepted")
				}
				benchSinkErr = rerr
			}
		})
	}
}

// BenchmarkDecompressTail prices the refused fast path against the production
// tail it would have replaced, at four sizes chosen to straddle io.ReadAll's
// 512-byte allocation floor. Both arms run the same reader, the same bound and
// the same Close; they differ in one branch.
func BenchmarkDecompressTail(b *testing.B) {
	for _, size := range benchTailSizes {
		b.Run("appending/"+strconv.Itoa(size), func(b *testing.B) {
			encoded, err := GzipCompressor.Compress(nil, benchRepetitive(size))
			if err != nil {
				b.Fatalf("Compress: %v", err)
			}
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				plain, derr := benchDecompressAppending(nil, encoded)
				if derr != nil {
					b.Fatalf("decompress: %v", derr)
				}
				benchSinkBytes = plain
			}
		})
		b.Run("refused-fastpath/"+strconv.Itoa(size), func(b *testing.B) {
			encoded, err := GzipCompressor.Compress(nil, benchRepetitive(size))
			if err != nil {
				b.Fatalf("Compress: %v", err)
			}
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				plain, derr := benchDecompressRefusedFastPath(nil, encoded)
				if derr != nil {
					b.Fatalf("decompress: %v", derr)
				}
				benchSinkBytes = plain
			}
		})
	}
}
