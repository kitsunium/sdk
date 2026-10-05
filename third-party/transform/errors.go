package transform

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitDataErr matches sysexits EX_DATAERR — a malformed or over-large frame is
// a data problem, not a generic internal software error (70). It mirrors the
// stdlib sibling in internal/core/data/transform.
const exitDataErr int = 65

var (
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
