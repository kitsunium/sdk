//go:build !race

// Package codec — the decompression-bomb guard measured by the WORK it does,
// not by the verdict it returns.
//
// This file carries //go:build !race, so it runs in exactly one lane: the
// race-off alloc lane (tools/alloc-lane-targets.txt, `make test-alloc`,
// `bazel test --config=alloc`). That is not a convenience. The quantity it
// asserts is a count of bytes allocated, and the race detector doubles it for
// reasons that have nothing to do with this package:
//
//	go test -run TestProbe                  read=67108865 TotalAlloc=157.7 MiB  (2.46x)
//	go test -race -run TestProbe            read=67108865 TotalAlloc=315.4 MiB  (4.93x)
//
// Those two lines come from a 20-line program outside this repository whose
// whole body is io.ReadAll(io.LimitReader(bytes.NewReader(zeros), 64<<20+1)).
// Identical figures — to within 56 KiB, 0.03 % — came out of this very test:
// 157.7 MiB race off, 315.4 MiB race on, against a budget of 192 MiB. So the
// race build does not report a guard doing twice the work; it reports the same
// work priced twice, by io.ReadAll and not by anything under pkg/v1/codec.
// Pinning a security budget to that number would pin the detector's
// bookkeeping. .bazelrc §alloc already rules on this for AllocsPerRun; a total
// malloc delta has the same defect, and the same lane is the answer.
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
	// implementation may allocate. It is calibrated on two measured endpoints
	// rather than on a growth story: draining 64 MiB through io.ReadAll costs
	// 2.46x the payload (157.7 MiB, reproducible to 0.03 % across runs and in
	// a stdlib-only probe), while the guard this test was written against
	// allocated 12.9x it (828.1 MiB). Any factor strictly between the two
	// discriminates; 4 sits 1.63x above the correct figure and 3.2x below the
	// defective one, which leaves room for a change in ReadAll's growth
	// strategy without leaving room for the defect.
	bombProbeAllocFactor uint64 = 4
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
// Measured against the guard as it stood before this PR: 828.1 MiB for a
// 260 169-byte frame, 3 338x the wire size. After: 157.7 MiB.
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
