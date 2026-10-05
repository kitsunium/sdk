package transform

import (
	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

var (
	// gzipWrap is the WrapParams the gzip scheme attaches to a stdlib cause; its
	// fields mirror the GzipFailed sentinel.
	gzipWrap = errs.WrapParams{
		Code:    coretransform.CodeGzipFailed,
		Reason:  "GZIP_FAILED",
		Public:  "gzip transform failed",
		Private: "service/data/transform: compress/gzip returned an error",
	}

	// flateWrap is the WrapParams the flate scheme attaches to a stdlib cause,
	// mirroring the FlateFailed sentinel.
	flateWrap = errs.WrapParams{
		Code:    coretransform.CodeFlateFailed,
		Reason:  "FLATE_FAILED",
		Public:  "flate transform failed",
		Private: "service/data/transform: compress/flate returned an error",
	}

	// zlibWrap is the WrapParams the zlib scheme attaches to a stdlib cause,
	// mirroring the ZlibFailed sentinel.
	zlibWrap = errs.WrapParams{
		Code:    coretransform.CodeZlibFailed,
		Reason:  "ZLIB_FAILED",
		Public:  "zlib transform failed",
		Private: "service/data/transform: compress/zlib returned an error",
	}
)
