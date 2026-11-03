// Package transform — the level knob of all three schemes (kitsunium/sdk#900):
// gzip and flate gained NewGzipCompressor and NewFlateCompressor beside
// zlib's NewZlibCompressor, one clamp for the three, and a writer pool per
// level for each.
package transform

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"io"
	"math/rand/v2"
	"strconv"
	"testing"

	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
)

// levelScheme is one scheme's level knob: its constructor, its registered
// singleton, the stdlib encode the pooled one must equal at every level, and
// its failure sentinel.
type levelScheme struct {
	name     string
	build    func(level int) coretransform.Compressor
	single   coretransform.Compressor
	fresh    func(tb testing.TB, level int, src []byte) []byte
	sentinel error
}

// levelSchemes returns the three schemes' knobs.
func levelSchemes() []levelScheme {
	return []levelScheme{
		{"gzip", NewGzipCompressor, GzipCompressor, freshGzip, coretransform.GzipFailed},
		{"flate", NewFlateCompressor, FlateCompressor, freshFlate, coretransform.FlateFailed},
		{"zlib", NewZlibCompressor, ZlibCompressor, freshZlib, coretransform.ZlibFailed},
	}
}

// freshGzip encodes src with a gzip writer built for this call at level.
func freshGzip(tb testing.TB, level int, src []byte) []byte {
	tb.Helper()
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, level)
	if err != nil {
		tb.Fatalf("gzip.NewWriterLevel(%d): %v", level, err)
	}
	return freshClose(tb, &buf, w, src)
}

// freshFlate encodes src with a flate writer built for this call at level.
func freshFlate(tb testing.TB, level int, src []byte) []byte {
	tb.Helper()
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, level)
	if err != nil {
		tb.Fatalf("flate.NewWriter(%d): %v", level, err)
	}
	return freshClose(tb, &buf, w, src)
}

// freshZlib encodes src with a zlib writer built for this call at level.
func freshZlib(tb testing.TB, level int, src []byte) []byte {
	tb.Helper()
	var buf bytes.Buffer
	w, err := zlib.NewWriterLevel(&buf, level)
	if err != nil {
		tb.Fatalf("zlib.NewWriterLevel(%d): %v", level, err)
	}
	return freshClose(tb, &buf, w, src)
}

// freshClose writes src through w, closes it and returns what buf holds.
func freshClose(tb testing.TB, buf *bytes.Buffer, w io.WriteCloser, src []byte) []byte {
	tb.Helper()
	if _, err := w.Write(src); err != nil {
		tb.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		tb.Fatalf("Close: %v", err)
	}
	return buf.Bytes()
}

// TestEveryLevelKnobNeverYieldsAnInertCompressor extends ADR 0031's clamp to
// the two knobs #900 added: every level — in band, NoCompression, out of band
// — gives a compressor that shrinks a compressible payload and round-trips it.
// The observable outcome is asserted, never the stored field.
func TestEveryLevelKnobNeverYieldsAnInertCompressor(t *testing.T) {
	t.Parallel()
	payload := bytes.Repeat([]byte("kitsunium-sdk "), 4096)
	levels := []int{
		flate.DefaultCompression, flate.HuffmanOnly, flate.BestSpeed, 6, flate.BestCompression,
		flate.NoCompression, flate.HuffmanOnly - 1, flate.BestCompression + 1, 1 << 20,
	}
	for _, sc := range levelSchemes() {
		for _, level := range levels {
			t.Run(sc.name+"/"+strconv.Itoa(level), func(t *testing.T) {
				t.Parallel()
				comp := sc.build(level)
				boxed, err := comp.Compress(nil, payload)
				if err != nil {
					t.Fatalf("Compress: %v", err)
				}
				//: the whole point — the compressor must actually compress.
				if len(boxed) >= len(payload) {
					t.Fatalf("compressed %d >= input %d: an inert compressor", len(boxed), len(payload))
				}
				//: clamping must not cost correctness: the payload round-trips.
				got, derr := comp.Decompress(nil, boxed)
				if derr != nil || !bytes.Equal(got, payload) {
					t.Fatalf("round trip: err=%v, %d bytes, want %d", derr, len(got), len(payload))
				}
			})
		}
	}
}

// TestEveryLevelEncodesAsTheStdlibDoes pins the per-level writer pools: a
// writer is recycled into the pool of the level it was built at, because
// Reset keeps the level, so a pooled encode at any level is byte-identical to
// a fresh stdlib writer at that level. The levels run interleaved, twice, so
// each take after the first is a pool hit — a single pool shared across levels
// would hand a BestSpeed writer to a BestCompression call and fail here.
func TestEveryLevelEncodesAsTheStdlibDoes(t *testing.T) {
	t.Parallel()
	payload := benchRepetitive(64 << 10)
	for _, sc := range levelSchemes() {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			for round := range 2 {
				for level := flate.HuffmanOnly; level <= flate.BestCompression; level++ {
					//: NoCompression is clamped away: it has no pooled encode.
					if level == flate.NoCompression {
						continue
					}
					got, err := sc.build(level).Compress(nil, payload)
					if err != nil {
						t.Fatalf("round %d, level %d: Compress: %v", round, level, err)
					}
					if want := sc.fresh(t, level, payload); !bytes.Equal(got, want) {
						t.Fatalf("round %d, level %d: %d bytes, the stdlib writes %d at that level", round, level, len(got), len(want))
					}
				}
			}
		})
	}
}

// levelWords is the vocabulary of levelPayload: few enough words to compress,
// drawn at random so the levels' match searches find different matches — a
// single repeated line compresses to the same bytes at BestSpeed and at the
// default, measured, so it cannot tell the levels apart.
var levelWords = []string{"request", "served", "route", "status", "cache", "miss", "retry", "orders", "billing", "token", "200", "404", "503", "ms", "{", "}", ":", ","}

// levelPayload is 64 KiB of levelWords in a deterministic random order.
func levelPayload() []byte {
	r := rand.New(rand.NewPCG(0x900, 0x1e7e1))
	var b bytes.Buffer
	for b.Len() < 64<<10 {
		b.WriteString(levelWords[r.IntN(len(levelWords))])
		b.WriteByte(' ')
	}
	return b.Bytes()
}

// TestALevelChangesTheBytes is #900's first acceptance line in a test: the
// knob reaches the writer. BestSpeed and BestCompression must give different
// bytes from the default, and BestCompression no more than BestSpeed.
func TestALevelChangesTheBytes(t *testing.T) {
	t.Parallel()
	payload := levelPayload()
	for _, sc := range levelSchemes() {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			def, derr := sc.single.Compress(nil, payload)
			fast, ferr := sc.build(flate.BestSpeed).Compress(nil, payload)
			best, berr := sc.build(flate.BestCompression).Compress(nil, payload)
			if err := errors.Join(derr, ferr, berr); err != nil {
				t.Fatalf("Compress: %v", err)
			}
			if bytes.Equal(fast, def) || bytes.Equal(best, def) {
				t.Fatalf("a level did not reach the writer: default %d bytes, BestSpeed %d, BestCompression %d", len(def), len(fast), len(best))
			}
			if len(best) > len(fast) {
				t.Fatalf("BestCompression wrote %d bytes, more than BestSpeed's %d", len(best), len(fast))
			}
		})
	}
}

// TestTheDefaultLevelIsTheSingleton pins the representation: a constructor
// asked for the default returns the very value the registry holds, so
// registering it again is the idempotent republish and not a conflict.
func TestTheDefaultLevelIsTheSingleton(t *testing.T) {
	t.Parallel()
	if NewGzipCompressor(gzip.DefaultCompression) != GzipCompressor {
		t.Fatal("NewGzipCompressor(DefaultCompression) is not the registered gzip singleton")
	}
	if NewFlateCompressor(flate.DefaultCompression) != FlateCompressor {
		t.Fatal("NewFlateCompressor(DefaultCompression) is not the registered flate singleton")
	}
	//: the clamp lands on the singleton too.
	if NewGzipCompressor(gzip.NoCompression) != GzipCompressor || NewFlateCompressor(42) != FlateCompressor {
		t.Fatal("a clamped level is not the registered singleton")
	}
	//: and re-registering it is the idempotent path, not a boot panic.
	if coretransform.Register(NewGzipCompressor(gzip.DefaultCompression)) != GzipCompressor {
		t.Fatal("re-registering the default gzip compressor did not return the singleton")
	}
}

// TestTheThreeLevelBandsAreOne pins what one clamp and one pool band for the
// three schemes rest on: compress/gzip and compress/zlib re-export
// compress/flate's level constants.
func TestTheThreeLevelBandsAreOne(t *testing.T) {
	t.Parallel()
	gz := [4]int{gzip.HuffmanOnly, gzip.NoCompression, gzip.DefaultCompression, gzip.BestCompression}
	zl := [4]int{zlib.HuffmanOnly, zlib.NoCompression, zlib.DefaultCompression, zlib.BestCompression}
	fl := [4]int{flate.HuffmanOnly, flate.NoCompression, flate.DefaultCompression, flate.BestCompression}
	if gz != fl || zl != fl {
		t.Fatalf("the level constants differ: gzip %v, zlib %v, flate %v", gz, zl, fl)
	}
}

// TestAnOutOfBandLevelFailsTyped drives the branch the constructors' clamp
// makes unreachable, through an in-package struct literal: a level the stdlib
// refuses fails with the scheme's sentinel and dst untouched, and takes the
// unpooled branch rather than a pool it has none of.
func TestAnOutOfBandLevelFailsTyped(t *testing.T) {
	t.Parallel()
	dst := []byte("prefix")
	for _, c := range []struct {
		name     string
		comp     coretransform.Compressor
		sentinel error
	}{
		{"gzip", gzipCompressor{level: flate.BestCompression + 1}, coretransform.GzipFailed},
		{"flate", flateCompressor{level: flate.HuffmanOnly - 1}, coretransform.FlateFailed},
	} {
		out, err := c.comp.Compress(dst, []byte("payload"))
		if !errors.Is(err, c.sentinel) {
			t.Fatalf("%s: err=%v, want %v", c.name, err, c.sentinel)
		}
		if !bytes.Equal(out, dst) {
			t.Fatalf("%s: a refusal returned %q, want dst back", c.name, out)
		}
	}
}
