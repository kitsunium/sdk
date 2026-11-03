//go:build !race

// The allocation gate of the decompression tail (kitsunium/sdk#1163). Race
// off, because the race detector allocates on every memory access: the alloc
// lane (tools/alloc-lane-targets.txt) is its only gate.
package transform_test

import (
	"bytes"
	"runtime"
	"strconv"
	"testing"

	tptransform "github.com/kitsunium/sdk/third-party/transform"
)

// spareAllocRuns is how many calls a window counts. Few, because the 4 MiB
// row decodes 4 MiB per call; enough that a per-call allocation shows.
const spareAllocRuns int = 20

// spareWarmCalls is how many calls run before a window opens, so the
// decoder's lazily built state and the runtime's type-assertion caches are
// paid outside it.
const spareWarmCalls int = 200

// spareAllocPrefix is the caller's bytes already in dst: the non-empty dst
// the tail treatment is for.
const spareAllocPrefix string = "frame header the caller wrote first"

// TestDecompressIntoSpareCapacityAllocatesNothing is #1163's first acceptance
// line: with a non-empty dst whose spare capacity holds the payload, zstd and
// s2 decode into that capacity — no fresh buffer, no copy onto dst — so the
// call allocates nothing, at 4 KiB and at 4 MiB.
//
// It is measured, and held, on amd64, where klauspost/compress decodes through
// its assembly and where the alloc lane runs. Elsewhere — 386 in CI's test-386
// job, measured — the library's portable
// decoder borrows its huff0 buffers from a sync.Pool, which the window's
// collection empties, so the first calls of the window refill it: two to six
// allocations over twenty calls, measured, none of them the payload. There the
// gate is skipped, after the architecture is checked, rather than loosened.
func TestDecompressIntoSpareCapacityAllocatesNothing(t *testing.T) {
	//: the claim is the assembly decoder's; the portable one refills a pool.
	if runtime.GOARCH != "amd64" {
		t.Skipf("GOARCH=%s decodes through the library's portable path, whose huff0 buffer pool refills after the window's collection", runtime.GOARCH)
	}
	zstdC, err := tptransform.NewZstdCompressor(tptransform.ZstdFastest, tptransform.DefaultMaxDecompressedBytes)
	if err != nil {
		t.Fatalf("NewZstdCompressor: %v", err)
	}
	defer closeChecked(t, zstdC)
	s2C, err := tptransform.NewS2Compressor(tptransform.DefaultMaxDecompressedBytes)
	if err != nil {
		t.Fatalf("NewS2Compressor: %v", err)
	}
	for _, sc := range []struct {
		name string
		c    interface {
			Compress(dst, src []byte) ([]byte, error)
			Decompress(dst, src []byte) ([]byte, error)
		}
	}{{"zstd", zstdC}, {"s2", s2C}} {
		for _, size := range []int{4 << 10, 4 << 20} {
			t.Run(sc.name+"/"+strconv.Itoa(size), func(t *testing.T) {
				plain := buildJSONCorpus(size)[:size]
				encoded, cerr := sc.c.Compress(nil, plain)
				if cerr != nil {
					t.Fatalf("Compress: %v", cerr)
				}
				dst := append(make([]byte, 0, len(spareAllocPrefix)+size), spareAllocPrefix...)
				var out []byte
				var derr error
				for range spareWarmCalls {
					out, derr = sc.c.Decompress(dst, encoded)
				}
				got := mallocsOver(spareAllocRuns, func() {
					out, derr = sc.c.Decompress(dst, encoded)
				})
				if derr != nil || !bytes.Equal(out[len(spareAllocPrefix):], plain) || string(out[:len(spareAllocPrefix)]) != spareAllocPrefix {
					t.Fatalf("Decompress: err=%v, %d bytes, want the prefix and the %d-byte payload", derr, len(out), size)
				}
				reportWindow(t, sc.name, got)
				if got.own != 0 {
					t.Fatalf("%d decompressions into a non-empty dst with %d bytes of spare capacity made %d allocations, want 0",
						spareAllocRuns, size, got.own)
				}
			})
		}
	}
}
