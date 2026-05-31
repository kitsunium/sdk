// Package transform — declares the sentinels returned by the Compressor
// facade. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE
// form. CodeUnknownCompressor is also surfaced via panic at boot on a duplicate
// registration (see registry.go), not only as an *Error sentinel.
package transform

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitDataErr matches sysexits EX_DATAERR — a malformed or undecompressable
// frame is a data problem, not a generic internal software error (70).
const exitDataErr int = 65

var (
	// UnknownCompressor is returned when no Compressor is registered under the
	// requested Algorithm — typically a missing blank-import of the scheme.
	UnknownCompressor = errs.Define(CodeUnknownCompressor, "UNKNOWN_COMPRESSOR",
		"No compressor is registered under that algorithm",
		"core/transform.Lookup: algorithm absent from registry; blank-import the scheme's package to register it")

	// CompressionFailed wraps a failure from a compressor's underlying writer
	// (e.g. compress/gzip or compress/flate) while encoding the payload.
	CompressionFailed = errs.Define(CodeCompressionFailed, "COMPRESSION_FAILED",
		"Compression failed",
		"core/transform.Compress: the underlying compressor returned an error",
		errs.WithExitCode(exitDataErr))

	// DecompressionFailed wraps a failure from a decompressor's underlying
	// reader while decoding the payload — malformed or truncated input.
	DecompressionFailed = errs.Define(CodeDecompressionFailed, "DECOMPRESSION_FAILED",
		"Decompression failed",
		"core/transform.Decompress: the underlying decompressor returned an error",
		errs.WithExitCode(exitDataErr))

	// CompressedFrameInvalid is returned for a malformed compressed frame or a
	// tripped decompression-bomb guard. The pkg/v1/codec frame layer (a later
	// commit) is its emitter; defined here so the 0.2.5.* block lands in one place.
	CompressedFrameInvalid = errs.Define(CodeCompressedFrameInvalid, "COMPRESSED_FRAME_INVALID",
		"Compressed frame is malformed or exceeds the safety bound",
		"core/transform: compressed-frame header is malformed or the decompression-bomb guard tripped",
		errs.WithExitCode(exitDataErr))

	// DuplicateRegistration is the boot-time panic sentinel for the Compressor
	// registry: a nil scheme or a distinct scheme claiming a taken Algorithm. Its
	// reason matches the bracket-header word so code 0.2.5.5 resolves to
	// DUPLICATE_REGISTRATION — never the unrelated UNKNOWN_COMPRESSOR (0.2.5.1).
	DuplicateRegistration = errs.Define(CodeDuplicateRegistration, "DUPLICATE_REGISTRATION",
		"A compressor is already registered under that algorithm",
		"core/transform.Register: a distinct scheme already claims this algorithm, or a nil scheme was supplied")
)
