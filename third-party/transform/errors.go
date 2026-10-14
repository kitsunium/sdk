package transform

import "github.com/kitsunium/sdk/internal/kernel/errs"

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
