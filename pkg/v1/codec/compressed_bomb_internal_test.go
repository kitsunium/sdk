// Package codec — the decompression-bomb guard measured by the WORK it does,
// not by the verdict it returns.
package codec

import (
	"bytes"
	"compress/gzip"
	"errors"
	"runtime"
	"testing"

	coretransform "github.com/kitsunium/sdk/internal/core/transform"
)

// Sizing for the bomb probe. The point of these numbers is that the frame
// layer's OWN ceiling is maxDecompressedFrameBytes (64 MiB), while the service
// layer behind it will hand back up to 256 MiB — so a bomb aimed between the
// two is rejected either way, and the only thing that distinguishes a guard
// which BOUNDS the work from one which merely JUDGES the result is how many
// bytes were touched getting there.
const (
	// bombProbeTargetBytes is the plaintext the crafted frame expands to. It
	// sits above the frame ceiling and at the service backstop, which is
	// exactly the window where the two guards disagree about the work.
	bombProbeTargetBytes int = 256 << 20
	// bombProbeChunkBytes is the buffer reused while writing the bomb, so
	// BUILDING it costs one megabyte rather than the full target.
	bombProbeChunkBytes int = 1 << 20
	// bombProbeAllocFactor is how many times the frame ceiling a correct
	// implementation may allocate. io.ReadAll grows by doubling, so reaching
	// an N-byte output costs roughly 2N cumulatively, plus the final buffer:
	// three is the honest allowance for a guard that stops at the ceiling.
	// Against a guard that stops only at the service backstop this budget is
	// exceeded by a wide margin, which is what makes the test discriminating
	// rather than decorative.
	bombProbeAllocFactor uint64 = 3
)

// Widths the ratio guard has to survive on a 32-bit build. maxExpansionRatio is
// 1000, so inLen*maxExpansionRatio leaves the int32 range once inLen passes
// math.MaxInt32/1000 — a little over two megabytes of COMPRESSED payload, which
// is an ordinary size rather than an adversarial one.
const (
	// bombOverflowInLen is a compressed size just past the 32-bit threshold.
	bombOverflowInLen int = 2_200_000
	// bombOverflowOutLen expands it by barely more than twice — three orders of
	// magnitude under the 1000x bound the guard actually enforces, so no
	// correct implementation may call this a bomb.
	bombOverflowOutLen int = 5_000_000
)

// buildGzipBomb returns a gzip stream that expands to bombProbeTargetBytes of
// zeros. It writes one reused chunk repeatedly so constructing the probe costs
// a megabyte, not a quarter of a gigabyte — otherwise the construction would
// dominate the very measurement the test exists to take.
func buildGzipBomb(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	//: BestCompression is what makes a run of zeros collapse to a few hundred
	//: kilobytes, which is the whole shape of the attack.
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	//: a writer this test cannot build is a broken test, not a finding.
	if err != nil {
		//: surface it as a setup failure.
		t.Fatalf("gzip.NewWriterLevel: %v", err)
	}
	//: one chunk, written repeatedly — the compressor sees a long run.
	chunk := make([]byte, bombProbeChunkBytes)
	//: emit exactly the target size.
	for range bombProbeTargetBytes / bombProbeChunkBytes {
		//: a write fault here is a setup failure too.
		if _, werr := zw.Write(chunk); werr != nil {
			//: surface it.
			t.Fatalf("gzip write: %v", werr)
		}
	}
	//: Close flushes the trailer; without it the stream is truncated.
	if cerr := zw.Close(); cerr != nil {
		//: surface it.
		t.Fatalf("gzip close: %v", cerr)
	}
	//: the compressed bomb, ready to be framed.
	return buf.Bytes()
}

// TestUnmarshalCompressedBoundsTheWorkNotJustTheVerdict is the regression that
// #215 needs, and it deliberately does NOT assert "the frame is rejected".
//
// That assertion passes today. It passed before the fix and it passes after,
// because the verdict was never wrong: decompressBounded returns
// CompressedFrameInvalid for a bomb either way. What was wrong is WHEN it
// decides — the payload is materialised in full first, and only then measured.
// The frame layer documents a 64 MiB ceiling described as "tighter than the
// service layer's 256 MiB backstop", but that tighter ceiling bounds the
// verdict and never the work, so the looser of the two is what actually caps
// allocation.
//
// So the assertion below is on TotalAlloc: how many bytes the call touched.
// A guard that stops at its own ceiling stays inside the budget; a guard that
// materialises first blows through it. That is the difference this test exists
// to see, and it is invisible to any check written against the return value.
func TestUnmarshalCompressedBoundsTheWorkNotJustTheVerdict(t *testing.T) {
	//: build the probe BEFORE the measurement window so its cost is excluded.
	box := appendFrame(algGzip, JSON, buildGzipBomb(t))
	//: the attack shape in one line: a tiny frame promising a huge payload.
	t.Logf("frame is %d bytes and declares %d bytes of plaintext", len(box), bombProbeTargetBytes)
	//: settle the heap so the delta below is this call's own work.
	runtime.GC()
	var before, after runtime.MemStats
	//: baseline.
	runtime.ReadMemStats(&before)
	var decoded any
	err := UnmarshalCompressed(box, &decoded)
	//: cumulative bytes allocated across the call — the work, not the peak.
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc

	//: the verdict must still be the documented rejection. This is a guard on
	//: the guard: a "fix" that bounded the work by silently accepting a
	//: truncated payload would be far worse than the defect.
	if !errors.Is(err, coretransform.CompressedFrameInvalid) {
		//: name what came back instead.
		t.Fatalf("UnmarshalCompressed on a bomb returned %v, want CompressedFrameInvalid", err)
	}
	//: THE ASSERTION — the work must be bounded by the frame layer's own
	//: ceiling, which is the bound that layer advertises.
	budget := uint64(maxDecompressedFrameBytes) * bombProbeAllocFactor
	//: report the number either way so a passing run still carries evidence.
	t.Logf("allocated %d bytes (%.1f MiB) for a %d-byte frame; budget %d bytes (%.1f MiB)",
		allocated, float64(allocated)/(1<<20), len(box), budget, float64(budget)/(1<<20))
	//: a guard that materialises before it measures fails here.
	if allocated > budget {
		//: state the amplification, since that is the exploitable quantity.
		t.Fatalf("a %d-byte frame drove %.1f MiB of allocation (%.0fx the wire size) before the guard rejected it; "+
			"the frame ceiling is %.0f MiB, so the work is bounded by the service backstop rather than by the frame's own limit",
			len(box), float64(allocated)/(1<<20), float64(allocated)/float64(len(box)),
			float64(maxDecompressedFrameBytes)/(1<<20))
	}
}

// TestIsBombSurvivesA32BitInt pins the ratio guard against integer overflow.
//
// `outLen > inLen*maxExpansionRatio` is evaluated in `int`. On a 64-bit build
// that is 64 bits wide and the product never overflows at these sizes; on a
// 32-bit build it is 32 bits, and 2 200 000 * 1000 wraps NEGATIVE. Every
// output is greater than a negative number, so the guard reports a bomb for a
// payload that expanded 2.3x — a false REJECTION of ordinary traffic, not a
// missed attack.
//
// This repository builds and tests linux/386, so the 32-bit path is shipped,
// not hypothetical. The assertion is written on isBomb directly because it is a
// pure function of two ints: reproducing it needs no 2 MB payload, only the two
// numbers. Run it with GOARCH=386 to see the failure this guards against.
func TestIsBombSurvivesA32BitInt(t *testing.T) {
	//: report the width so a run on either architecture says which it exercised.
	t.Logf("int is %d bits on this build", 32<<(^uint(0)>>63))
	//: the product is computed through a VARIABLE on purpose. As a constant
	//: expression the compiler rejects the overflow outright — which is how
	//: this very line first failed to build on GOARCH=386. isBomb overflows at
	//: RUNTIME instead, on values the compiler never sees, which is exactly why
	//: nothing caught it.
	inLen := bombOverflowInLen
	//: a 2.3x expansion is not a bomb by any reading of the ratio bound.
	if isBomb(inLen, bombOverflowOutLen) {
		//: name the arithmetic, since the numbers alone look innocuous.
		t.Fatalf("isBomb(%d, %d) reported a bomb for a %.1fx expansion: "+
			"inLen*maxExpansionRatio = %d overflowed int and went negative",
			inLen, bombOverflowOutLen,
			float64(bombOverflowOutLen)/float64(inLen),
			inLen*maxExpansionRatio)
	}
}
