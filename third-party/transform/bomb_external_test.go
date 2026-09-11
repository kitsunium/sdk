package transform_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	tptransform "github.com/kitsunium/sdk/third-party/transform"
)

// bombPlain is the plaintext every bomb in this file decompresses to: 64 MiB of
// zeros. It is never materialised by a test that succeeds — only by the ones
// that BUILD a bomb, which is the point.
const bombPlain int = 64 << 20

// bombLimit is the ceiling the compressors under test are built with. It is
// deliberately tiny relative to bombPlain so the refusal is unambiguous.
const bombLimit int64 = 1 << 20 // 1 MiB

// TestZstdRefusesDecompressionBomb builds a real zstd bomb — a few kilobytes
// that decompress to 64 MiB — and proves the compressor refuses it.
//
// The assertion is not "an error came back". It is all four of:
//   - the bomb is real (the ratio is reported, so a future change that made the
//     fixture harmless would be visible rather than silently passing);
//   - the refusal carries CodeDecompressionLimitExceeded, NOT CodeZstdFailed —
//     a bomb must be greppable apart from ordinary corrupt input;
//   - no plaintext comes back with it (dst is returned at its original length);
//   - the same compressor still decodes a legitimate payload afterwards, so the
//     refusal is a refusal and not a wedged decoder.
func TestZstdRefusesDecompressionBomb(t *testing.T) {
	t.Parallel()

	//: an unbounded builder is only used to MAKE the bomb, never to read one.
	builder, err := tptransform.NewZstdCompressor(tptransform.ZstdFastest, int64(bombPlain)*2)
	if err != nil {
		t.Fatalf("build bomb: %v", err)
	}
	defer closeChecked(t, builder)

	bomb, cerr := builder.Compress(nil, bytes.Repeat([]byte{0}, bombPlain))
	if cerr != nil {
		t.Fatalf("compress bomb: %v", cerr)
	}
	ratio := float64(bombPlain) / float64(len(bomb))
	t.Logf("zstd bomb: %d compressed bytes -> %d plaintext bytes (%.0f:1)", len(bomb), bombPlain, ratio)
	if ratio < 1000 {
		t.Fatalf("fixture is not a bomb: ratio %.0f:1", ratio)
	}

	guarded, gerr := tptransform.NewZstdCompressor(tptransform.ZstdFastest, bombLimit)
	if gerr != nil {
		t.Fatalf("build guarded: %v", gerr)
	}
	defer closeChecked(t, guarded)

	dst := []byte("prefix")
	out, derr := guarded.Decompress(dst, bomb)
	if derr == nil {
		t.Fatalf("bomb accepted: %d bytes returned", len(out))
	}
	if !errs.HasCode(derr, tptransform.CodeDecompressionLimitExceeded) {
		t.Fatalf("wrong code: want CodeDecompressionLimitExceeded, got %v", derr)
	}
	if errs.HasCode(derr, tptransform.CodeZstdFailed) {
		t.Fatalf("a bomb must not be reported as a corrupt stream: %v", derr)
	}
	if !bytes.Equal(out, dst) {
		t.Fatalf("refusal leaked output: got %d bytes, want the %d-byte dst back", len(out), len(dst))
	}

	//: the guard refuses, it does not wedge — a legal payload still round-trips.
	legit, lerr := guarded.Compress(nil, []byte("still working"))
	if lerr != nil {
		t.Fatalf("compress after refusal: %v", lerr)
	}
	back, berr := guarded.Decompress(nil, legit)
	if berr != nil || string(back) != "still working" {
		t.Fatalf("decoder wedged after refusal: %q %v", back, berr)
	}
}

// TestZstdRefusesMultiFrameBomb is the sharp one. A zstd stream may carry many
// concatenated frames, and DecodeAll decodes all of them. A guard enforced PER
// FRAME would accept 64 frames each just under the ceiling and materialise 64x
// the ceiling — the classic way an output bound is bypassed without ever
// violating it.
//
// Measured here rather than assumed: the ceiling is enforced across the whole
// call, so the multi-frame stream is refused too.
func TestZstdRefusesMultiFrameBomb(t *testing.T) {
	t.Parallel()

	builder, err := tptransform.NewZstdCompressor(tptransform.ZstdFastest, int64(bombPlain)*2)
	if err != nil {
		t.Fatalf("build bomb: %v", err)
	}
	defer closeChecked(t, builder)

	//: each frame decodes to half the ceiling — individually innocent.
	frame, cerr := builder.Compress(nil, bytes.Repeat([]byte{0}, int(bombLimit/2)))
	if cerr != nil {
		t.Fatalf("compress frame: %v", cerr)
	}
	const frames int = 64
	var multi []byte
	for range frames {
		multi = append(multi, frame...)
	}
	t.Logf("zstd multi-frame bomb: %d frames, %d compressed bytes -> %d plaintext bytes if decoded (ceiling %d)",
		frames, len(multi), frames*int(bombLimit/2), bombLimit)

	guarded, gerr := tptransform.NewZstdCompressor(tptransform.ZstdFastest, bombLimit)
	if gerr != nil {
		t.Fatalf("build guarded: %v", gerr)
	}
	defer closeChecked(t, guarded)

	out, derr := guarded.Decompress(nil, multi)
	if derr == nil {
		t.Fatalf("multi-frame bomb accepted: %d bytes returned", len(out))
	}
	if !errs.HasCode(derr, tptransform.CodeDecompressionLimitExceeded) {
		t.Fatalf("wrong code: want CodeDecompressionLimitExceeded, got %v", derr)
	}
	if len(out) != 0 {
		t.Fatalf("refusal leaked %d bytes", len(out))
	}
}

// TestS2RefusesDecompressionBombBeforeDecoding proves the s2 refusal happens
// from the block header, before any decode.
//
// The fixture is the loudest number in this package: 64 MiB of zeros becomes a
// handful of bytes, a ratio well past a million to one. The guard reads the
// varint length header, compares, and refuses — so a bomb that would have cost
// 64 MiB of RSS costs a few byte-loads instead. The declared length is
// reported in the error fields precisely so an operator can see what was
// claimed without anyone having to decode it.
func TestS2RefusesDecompressionBombBeforeDecoding(t *testing.T) {
	t.Parallel()

	builder, err := tptransform.NewS2Compressor(int64(bombPlain) * 2)
	if err != nil {
		t.Fatalf("build bomb: %v", err)
	}
	bomb, cerr := builder.Compress(nil, bytes.Repeat([]byte{0}, bombPlain))
	if cerr != nil {
		t.Fatalf("compress bomb: %v", cerr)
	}
	ratio := float64(bombPlain) / float64(len(bomb))
	t.Logf("s2 bomb: %d compressed bytes -> %d plaintext bytes (%.0f:1)", len(bomb), bombPlain, ratio)
	if ratio < 1000 {
		t.Fatalf("fixture is not a bomb: ratio %.0f:1", ratio)
	}

	guarded, gerr := tptransform.NewS2Compressor(bombLimit)
	if gerr != nil {
		t.Fatalf("build guarded: %v", gerr)
	}
	dst := []byte("prefix")
	out, derr := guarded.Decompress(dst, bomb)
	if derr == nil {
		t.Fatalf("bomb accepted: %d bytes returned", len(out))
	}
	if !errs.HasCode(derr, tptransform.CodeDecompressionLimitExceeded) {
		t.Fatalf("wrong code: want CodeDecompressionLimitExceeded, got %v", derr)
	}
	if !bytes.Equal(out, dst) {
		t.Fatalf("refusal leaked output: got %d bytes, want the %d-byte dst back", len(out), len(dst))
	}
}

// TestBoundaryIsTheDeclaredSize pins the edge of the s2 ceiling: exactly at the
// limit is accepted, one byte past is refused. A guard nobody has driven to its
// boundary is a guard nobody knows the sign of.
func TestBoundaryIsTheDeclaredSize(t *testing.T) {
	t.Parallel()

	const limit int64 = 4096
	builder, err := tptransform.NewS2Compressor(limit * 4)
	if err != nil {
		t.Fatalf("build builder: %v", err)
	}
	guarded, gerr := tptransform.NewS2Compressor(limit)
	if gerr != nil {
		t.Fatalf("build guarded: %v", gerr)
	}

	for _, tc := range []struct {
		name    string
		size    int
		refused bool
	}{
		{"one below the ceiling", int(limit) - 1, false},
		{"exactly at the ceiling", int(limit), false},
		{"one past the ceiling", int(limit) + 1, true},
	} {
		encoded, cerr := builder.Compress(nil, bytes.Repeat([]byte{'a'}, tc.size))
		if cerr != nil {
			t.Fatalf("%s: compress: %v", tc.name, cerr)
		}
		out, derr := guarded.Decompress(nil, encoded)
		switch {
		case tc.refused && derr == nil:
			t.Fatalf("%s: accepted %d bytes, want refusal", tc.name, len(out))
		case tc.refused && !errs.HasCode(derr, tptransform.CodeDecompressionLimitExceeded):
			t.Fatalf("%s: wrong code: %v", tc.name, derr)
		case !tc.refused && derr != nil:
			t.Fatalf("%s: refused a legal payload: %v", tc.name, derr)
		case !tc.refused && len(out) != tc.size:
			t.Fatalf("%s: got %d bytes, want %d", tc.name, len(out), tc.size)
		}
	}
}

// TestConstructorRefusesNonPositiveLimit pins ADR 0031's refuse half: a ceiling
// that does not bound anything is refused at construction, so a compressor that
// would decompress without a limit never exists to be called. Zero is the case
// that matters — it is in range, it looks deliberate, and its two readings are
// opposites.
func TestConstructorRefusesNonPositiveLimit(t *testing.T) {
	t.Parallel()

	for _, limit := range []int64{0, -1, -(1 << 40)} {
		z, zerr := tptransform.NewZstdCompressor(tptransform.ZstdFastest, limit)
		if zerr == nil {
			closeChecked(t, z)
			t.Fatalf("zstd accepted limit %d", limit)
		}
		if !errs.HasCode(zerr, tptransform.CodeLimitMisconfigured) {
			t.Fatalf("zstd limit %d: wrong code: %v", limit, zerr)
		}
		if z != nil {
			t.Fatalf("zstd limit %d: refusal returned a compressor", limit)
		}

		s, serr := tptransform.NewS2Compressor(limit)
		if serr == nil {
			t.Fatalf("s2 accepted limit %d", limit)
		}
		if !errs.HasCode(serr, tptransform.CodeLimitMisconfigured) {
			t.Fatalf("s2 limit %d: wrong code: %v", limit, serr)
		}
		if s != nil {
			t.Fatalf("s2 limit %d: refusal returned a compressor", limit)
		}
	}
}

// TestRegisteredSingletonsAreBounded proves the guard is not something a caller
// has to remember to switch on: the singletons an import registers carry
// DefaultMaxDecompressedBytes, so the bomb is refused through the registry path
// too — the path a caller who never read this package will actually use.
func TestRegisteredSingletonsAreBounded(t *testing.T) {
	t.Parallel()

	builder, err := tptransform.NewS2Compressor(int64(bombPlain) * 2)
	if err != nil {
		t.Fatalf("build bomb: %v", err)
	}
	//: 4x the default ceiling — a legal payload for the builder, a bomb for S2.
	bomb, cerr := builder.Compress(nil, bytes.Repeat([]byte{0}, int(tptransform.DefaultMaxDecompressedBytes)*4))
	if cerr != nil {
		t.Fatalf("compress bomb: %v", cerr)
	}

	_, derr := tptransform.S2.Decompress(nil, bomb)
	if derr == nil {
		t.Fatal("the registered s2 singleton accepted a payload past its ceiling")
	}
	if !errs.HasCode(derr, tptransform.CodeDecompressionLimitExceeded) {
		t.Fatalf("wrong code from the singleton: %v", derr)
	}
}

// TestCorruptInputIsNotReportedAsABomb is the other half of the classification
// claim. Garbage must come back as ZSTD_FAILED / S2_FAILED, never as the bomb
// code — otherwise the alert built on the bomb code fires on every scanner that
// pokes the endpoint and stops meaning anything.
func TestCorruptInputIsNotReportedAsABomb(t *testing.T) {
	t.Parallel()

	garbage := []byte{0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8}

	z, zerr := tptransform.NewZstdCompressor(tptransform.ZstdFastest, bombLimit)
	if zerr != nil {
		t.Fatalf("build zstd: %v", zerr)
	}
	defer closeChecked(t, z)

	_, derr := z.Decompress(nil, garbage)
	if derr == nil {
		t.Fatal("zstd accepted garbage")
	}
	if errs.HasCode(derr, tptransform.CodeDecompressionLimitExceeded) {
		t.Fatalf("garbage reported as a bomb: %v", derr)
	}
	if !errs.HasCode(derr, tptransform.CodeZstdFailed) {
		t.Fatalf("zstd garbage: wrong code: %v", derr)
	}

	s, serr := tptransform.NewS2Compressor(bombLimit)
	if serr != nil {
		t.Fatalf("build s2: %v", serr)
	}
	_, s2err := s.Decompress(nil, []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	if s2err == nil {
		t.Fatal("s2 accepted garbage")
	}
	if errs.HasCode(s2err, tptransform.CodeDecompressionLimitExceeded) {
		t.Fatalf("garbage reported as a bomb: %v", s2err)
	}
	if !errs.HasCode(s2err, tptransform.CodeS2Failed) {
		t.Fatalf("s2 garbage: wrong code: %v", s2err)
	}
}

// TestRefusalWrapsTheSentinel checks the refusal is matchable with errors.Is
// against the exported sentinel, not only by code — a caller routing on the
// sentinel must not have to know the dotted quad.
func TestRefusalWrapsTheSentinel(t *testing.T) {
	t.Parallel()

	builder, err := tptransform.NewS2Compressor(int64(bombPlain) * 2)
	if err != nil {
		t.Fatalf("build bomb: %v", err)
	}
	bomb, cerr := builder.Compress(nil, bytes.Repeat([]byte{0}, bombPlain))
	if cerr != nil {
		t.Fatalf("compress bomb: %v", cerr)
	}
	guarded, gerr := tptransform.NewS2Compressor(bombLimit)
	if gerr != nil {
		t.Fatalf("build guarded: %v", gerr)
	}
	_, derr := guarded.Decompress(nil, bomb)
	if !errors.Is(derr, tptransform.DecompressionLimitExceeded) {
		t.Fatalf("refusal does not wrap the sentinel: %v", derr)
	}
}
