package transform

import (
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_encoderLevel pins the ZstdLevel -> library level mapping AND the clamp
// target. The external suite asserts the observable outcome (a level never
// yields an inert compressor); this one asserts the table itself, so a mapping
// that silently drifted — ZstdBest quietly becoming SpeedDefault, say — fails
// here rather than showing up as a ratio nobody rechecked.
func Test_encoderLevel(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		level ZstdLevel
		want  zstd.EncoderLevel
	}{
		{"fastest", ZstdFastest, zstd.SpeedFastest},
		{"default", ZstdDefault, zstd.SpeedDefault},
		{"better", ZstdBetter, zstd.SpeedBetterCompression},
		{"best", ZstdBest, zstd.SpeedBestCompression},
		//: the ADR 0031 clamp half — every unrecognised value lands on default.
		{"zero clamps", 0, zstd.SpeedDefault},
		{"negative clamps", -1, zstd.SpeedDefault},
		{"above the range clamps", 99, zstd.SpeedDefault},
		{"between two levels clamps", 5, zstd.SpeedDefault},
	} {
		if got := encoderLevel(tc.level); got != tc.want {
			t.Fatalf("%s: encoderLevel(%d) = %v, want %v", tc.name, tc.level, got, tc.want)
		}
	}
}

// Test_checkLimit pins the refusal boundary directly: the smallest positive
// ceiling is accepted and everything at or below zero is refused with the
// typed code. It is the in-package half of the ADR 0031 refuse argument, so a
// future "just default it" edit fails a test rather than passing review.
func Test_checkLimit(t *testing.T) {
	t.Parallel()

	for _, limit := range []int64{1, 2, 1 << 20, 1 << 62} {
		if err := checkLimit(limit); err != nil {
			t.Fatalf("checkLimit(%d) refused a positive ceiling: %v", limit, err)
		}
	}
	for _, limit := range []int64{0, -1, -(1 << 62)} {
		err := checkLimit(limit)
		if err == nil {
			t.Fatalf("checkLimit(%d) accepted a non-positive ceiling", limit)
		}
		if !errs.HasCode(err, CodeLimitMisconfigured) {
			t.Fatalf("checkLimit(%d): wrong code: %v", limit, err)
		}
	}
}

// Test_isLimitError pins the classification input side: both library refusals
// count as the ceiling, and an unrelated library error does not. Getting this
// wrong is silent — a bomb would be logged as a corrupt stream and the alert
// built on the bomb code would never fire.
func Test_isLimitError(t *testing.T) {
	t.Parallel()

	if !isLimitError(zstd.ErrDecoderSizeExceeded) {
		t.Fatal("ErrDecoderSizeExceeded is not recognised as a ceiling refusal")
	}
	if !isLimitError(zstd.ErrWindowSizeExceeded) {
		t.Fatal("ErrWindowSizeExceeded is not recognised as a ceiling refusal")
	}
	if isLimitError(zstd.ErrMagicMismatch) {
		t.Fatal("ErrMagicMismatch must not be read as a ceiling refusal")
	}
	if isLimitError(nil) {
		t.Fatal("nil must not be read as a ceiling refusal")
	}
}
