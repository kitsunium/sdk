//go:build !race

// Package transform — the allocation gates of the decompression tail
// (kitsunium/sdk#1102). Race off, because the race detector allocates on every
// memory access: the alloc lane (tools/alloc-lane-targets.txt) is their only
// gate.
package transform

import (
	"bytes"
	"strconv"
	"testing"

	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
)

// decompressAllocRuns is how many calls a window counts: enough that a defect
// allocating once in a while still shows, few enough to keep the 64 KiB rows
// quick.
const decompressAllocRuns int = 200

// decompressWarmCalls is how many calls run before a window opens: the
// runtime builds a type assertion's cache on about one miss in 1024, and the
// decode path asserts on its reader in compress/gzip, compress/flate and the
// pool, so a few hundred warm calls leave one of those caches unbuilt often
// enough to fail a zero gate. 30 000 leaves one unbuilt with a probability
// near 2e-13 — kit's own figure for its generated gates (ADR 0165).
const decompressWarmCalls int = 30000

// decompressAllocSizes are the plaintext sizes the gate holds: the issue's
// two, one below and one above the 32 KiB DEFLATE window a decoder reads
// through.
var decompressAllocSizes = []int{4 << 10, 64 << 10}

// decompressAllocSchemes are the registered singletons, so the gate measures
// what a caller resolves through the registry.
var decompressAllocSchemes = []struct {
	name string
	comp coretransform.Compressor
	// sized is how many allocations a call into a sized dst may make: zero,
	// except zlib's one — compress/zlib's Reset builds a fresh Adler-32
	// digest (adler32.New) on every stream, a cost of the standard library's
	// that no caller-side buffer can shed.
	sized uint64
}{
	{"gzip", GzipCompressor, 0},
	{"flate", FlateCompressor, 0},
	{"zlib", ZlibCompressor, 1},
}

// decompressWarmed records the payloads warmDecompress already ran, so the
// second gate over a payload does not pay its warm-up again. The gates are
// not parallel: they pin GOMAXPROCS and the collector, which are the process's.
var decompressWarmed = map[string]bool{}

// warmDecompress runs comp's decode path decompressWarmCalls times over the
// payload a window is about to count, into a sized dst and into nil, so every
// type-assertion cache on both tails is built first. The payload has to be the
// measured one: a 64 KiB text decodes through compress/flate branches — a
// stored block, a long copy — that a small one never reaches, each with its
// own assertion.
func warmDecompress(t *testing.T, name string, comp coretransform.Compressor, encoded []byte, size int) {
	t.Helper()
	key := name + "/" + strconv.Itoa(size)
	//: once per payload for the whole file.
	if decompressWarmed[key] {
		return
	}
	decompressWarmed[key] = true
	dst := make([]byte, 0, size)
	for i := range decompressWarmCalls {
		//: every call into the sized dst, one in a hundred into nil: the
		//: spill tail's assertions are few and the nil call allocates.
		if _, derr := comp.Decompress(dst, encoded); derr != nil {
			t.Fatalf("Decompress: %v", derr)
		}
		if i%100 == 0 {
			if _, derr := comp.Decompress(nil, encoded); derr != nil {
				t.Fatalf("Decompress: %v", derr)
			}
		}
	}
}

// TestDecompressIntoASizedDstAllocatesNothing is #1102's first acceptance
// line: a dst whose spare capacity holds the plaintext is decoded into in
// place, so a caller reusing one buffer pays no allocation for the payload —
// not the io.ReadAll doubling chain, and not the copy onto dst after it.
func TestDecompressIntoASizedDstAllocatesNothing(t *testing.T) {
	for _, sc := range decompressAllocSchemes {
		for _, size := range decompressAllocSizes {
			t.Run(sc.name+"/"+strconv.Itoa(size), func(t *testing.T) {
				plain := benchRepetitive(size)
				encoded, err := sc.comp.Compress(nil, plain)
				if err != nil {
					t.Fatalf("Compress: %v", err)
				}
				warmDecompress(t, sc.name, sc.comp, encoded, size)
				dst := make([]byte, 0, size)
				var out []byte
				var derr error
				got := mallocsOver(decompressAllocRuns, func() {
					out, derr = sc.comp.Decompress(dst, encoded)
				})
				if derr != nil || !bytes.Equal(out, plain) {
					t.Fatalf("Decompress: err=%v, %d bytes, want the %d-byte payload", derr, len(out), size)
				}
				reportWindow(t, sc.name, got)
				if want := sc.sized * uint64(decompressAllocRuns); got.own > want {
					t.Fatalf("%d decompressions into a %d-byte dst made %d allocations, want at most %d (%d a call)",
						decompressAllocRuns, size, got.own, want, sc.sized)
				}
			})
		}
	}
}

// TestDecompressIntoANilDstAllocatesThePayloadOnce is the issue's second
// line: with no dst at all, the bytes allocated stay within 1.5 times the
// plaintext — one buffer of the payload's size, where io.ReadAll's doubling
// chain and the copy after it cost 3.5 times as much.
func TestDecompressIntoANilDstAllocatesThePayloadOnce(t *testing.T) {
	for _, sc := range decompressAllocSchemes {
		for _, size := range decompressAllocSizes {
			t.Run(sc.name+"/"+strconv.Itoa(size), func(t *testing.T) {
				plain := benchRepetitive(size)
				encoded, err := sc.comp.Compress(nil, plain)
				if err != nil {
					t.Fatalf("Compress: %v", err)
				}
				warmDecompress(t, sc.name, sc.comp, encoded, size)
				var out []byte
				var derr error
				got := mallocsOver(decompressAllocRuns, func() {
					out, derr = sc.comp.Decompress(nil, encoded)
				})
				if derr != nil || !bytes.Equal(out, plain) {
					t.Fatalf("Decompress: err=%v, %d bytes, want the %d-byte payload", derr, len(out), size)
				}
				reportWindow(t, sc.name, got)
				//: one allocation for the result, plus zlib's digest.
				if want := (1 + sc.sized) * uint64(decompressAllocRuns); got.own > want {
					t.Fatalf("%d decompressions into a nil dst made %d allocations, want at most %d (%d a call)",
						decompressAllocRuns, got.own, want, 1+sc.sized)
				}
				//: and that one allocation is the payload's size, not a guess.
				if cap(out) > size+size/2 {
					t.Fatalf("the result holds %d bytes of capacity for %d of payload, want at most 1.5 times", cap(out), size)
				}
			})
		}
	}
}
