// Package transform — declares the sentinel *errs.Error values returned by the
// vendor compressors. Each sentinel var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form. The zstdWrap / s2Wrap WrapParams mirror their sentinels
// so a wrapped library cause carries the same Code/Reason/Public on the wire as
// the bare sentinel (origin wins, root CLAUDE.md rule 6).
package transform

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitDataErr matches sysexits EX_DATAERR — a malformed or over-large frame is
// a data problem, not a generic internal software error (70). It mirrors the
// stdlib sibling in internal/core/transform.
const exitDataErr int = 65

var (
	// ZstdFailed wraps a failure from the zstd codec (Compress or Decompress).
	ZstdFailed = errs.Define(CodeZstdFailed, "ZSTD_FAILED",
		"zstd transform failed",
		"third-party/transform: the zstd codec returned an error",
		errs.WithExitCode(exitDataErr))

	// S2Failed wraps a failure from the s2 codec (Compress or Decompress).
	S2Failed = errs.Define(CodeS2Failed, "S2_FAILED",
		"s2 transform failed",
		"third-party/transform: the s2 codec returned an error",
		errs.WithExitCode(exitDataErr))

	// DecompressionLimitExceeded is returned when a decompression would produce
	// more plaintext than the compressor's configured ceiling allows. No output
	// is returned with it: the caller's dst is handed back at its original
	// length, so a refused bomb never leaves a partial payload behind.
	DecompressionLimitExceeded = errs.Define(CodeDecompressionLimitExceeded, "DECOMPRESSION_LIMIT_EXCEEDED",
		"Decompressed output would exceed the configured limit",
		"third-party/transform: the decompression-bomb ceiling refused this payload",
		errs.WithExitCode(exitDataErr))

	// LimitMisconfigured is returned by NewZstdCompressor / NewS2Compressor for
	// a non-positive maxDecompressedBytes. It is a construction-time refusal, so
	// a compressor that would not bound its output never exists to be called.
	LimitMisconfigured = errs.Define(CodeLimitMisconfigured, "LIMIT_MISCONFIGURED",
		"maxDecompressedBytes must be a positive byte count",
		"third-party/transform: a non-positive decompression ceiling is refused, never defaulted (ADR 0031)")

	// zstdWrap is the WrapParams the zstd scheme attaches to a library cause;
	// its fields mirror the ZstdFailed sentinel.
	zstdWrap = errs.WrapParams{
		Code:    CodeZstdFailed,
		Reason:  "ZSTD_FAILED",
		Public:  "zstd transform failed",
		Private: "third-party/transform: the zstd codec returned an error",
	}

	// s2Wrap is the WrapParams the s2 scheme attaches to a library cause,
	// mirroring the S2Failed sentinel.
	s2Wrap = errs.WrapParams{
		Code:    CodeS2Failed,
		Reason:  "S2_FAILED",
		Public:  "s2 transform failed",
		Private: "third-party/transform: the s2 codec returned an error",
	}
)
