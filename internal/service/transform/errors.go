// Package transform — declares the sentinel *errs.Error values used to wrap
// failures from compress/gzip and compress/flate. Each sentinel var's name
// equals its errs.Define Reason in SCREAMING_SNAKE form. The gzipWrap /
// flateWrap WrapParams mirror their sentinels so a wrapped stdlib cause carries
// the same Code/Reason/Public on the wire as the bare sentinel.
package transform

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// GzipFailed wraps a failure from compress/gzip (Compress or Decompress).
	GzipFailed = errs.Define(CodeGzipFailed, "GZIP_FAILED",
		"gzip transform failed",
		"service/transform: compress/gzip returned an error")

	// FlateFailed wraps a failure from compress/flate (Compress or Decompress).
	FlateFailed = errs.Define(CodeFlateFailed, "FLATE_FAILED",
		"flate transform failed",
		"service/transform: compress/flate returned an error")

	// gzipWrap is the WrapParams the gzip scheme attaches to a stdlib cause; its
	// fields mirror the GzipFailed sentinel.
	gzipWrap = errs.WrapParams{
		Code:    CodeGzipFailed,
		Reason:  "GZIP_FAILED",
		Public:  "gzip transform failed",
		Private: "service/transform: compress/gzip returned an error",
	}

	// flateWrap is the WrapParams the flate scheme attaches to a stdlib cause,
	// mirroring the FlateFailed sentinel.
	flateWrap = errs.WrapParams{
		Code:    CodeFlateFailed,
		Reason:  "FLATE_FAILED",
		Public:  "flate transform failed",
		Private: "service/transform: compress/flate returned an error",
	}
)
