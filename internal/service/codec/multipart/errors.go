// Package multipart — declares the sentinel *errs.Error values for the
// multipart/form-data codec.
package multipart

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// MarshalFailed wraps a failure raised while writing a part.
	MarshalFailed = errs.Define(CodeMultipartMarshalFailed, "MARSHAL_FAILED",
		"multipart encoding failed",
		"service/codec/multipart: mime/multipart returned an error while writing a part")

	// UnmarshalFailed wraps a failure raised while parsing a body.
	UnmarshalFailed = errs.Define(CodeMultipartUnmarshalFailed, "UNMARSHAL_FAILED",
		"multipart decoding failed",
		"service/codec/multipart: mime/multipart returned an error while reading a part")

	// ValueInvalid fires when the caller's argument shape cannot be honoured.
	ValueInvalid = errs.Define(CodeMultipartValueInvalid, "VALUE_INVALID",
		"multipart codec rejected the value shape",
		"service/codec/multipart: Marshal/Unmarshal called with an unusable argument")

	// BoundaryInvalid fires when no usable RFC 2046 boundary is available.
	BoundaryInvalid = errs.Define(CodeMultipartBoundaryInvalid, "BOUNDARY_INVALID",
		"multipart boundary is missing or invalid",
		"service/codec/multipart: boundary absent from the body or outside the RFC 2046 charset")

	// LimitExceeded fires when a payload crosses one of the configured bounds.
	LimitExceeded = errs.Define(CodeMultipartLimitExceeded, "LIMIT_EXCEEDED",
		"multipart payload exceeds a configured limit",
		"service/codec/multipart: part size, part count or aggregate size crossed its bound")

	// LimitsInvalid fires when a Limits value carries a negative bound.
	LimitsInvalid = errs.Define(CodeMultipartLimitsInvalid, "LIMITS_INVALID",
		"multipart limits are not usable",
		"service/codec/multipart: a negative bound is neither a limit nor a request for the default")
)
