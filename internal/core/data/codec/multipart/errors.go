// Package multipart — the sentinel *errs.Error values, one per code. Each
// Reason derives from its var name or from its Code constant (ADR 0020);
// the Public and Private texts are the ones the service package always
// emitted, so moving the declaration changed no rendering.
package multipart

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// MarshalFailed wraps a failure raised while writing a part.
	MarshalFailed = errs.Define(CodeMultipartMarshalFailed, "MARSHAL_FAILED",
		"multipart encoding failed",
		"service/data/codec/multipart: mime/multipart returned an error while writing a part")

	// UnmarshalFailed wraps a failure raised while parsing a body.
	UnmarshalFailed = errs.Define(CodeMultipartUnmarshalFailed, "UNMARSHAL_FAILED",
		"multipart decoding failed",
		"service/data/codec/multipart: mime/multipart returned an error while reading a part")

	// ValueInvalid fires when the caller's argument shape cannot be honoured.
	ValueInvalid = errs.Define(CodeMultipartValueInvalid, "VALUE_INVALID",
		"multipart codec rejected the value shape",
		"service/data/codec/multipart: Marshal/Unmarshal called with an unusable argument")

	// BoundaryInvalid fires when no usable RFC 2046 boundary is available.
	BoundaryInvalid = errs.Define(CodeMultipartBoundaryInvalid, "BOUNDARY_INVALID",
		"multipart boundary is missing or invalid",
		"service/data/codec/multipart: boundary absent from the body or outside the RFC 2046 charset")

	// LimitExceeded fires when a payload crosses one of the configured bounds.
	LimitExceeded = errs.Define(CodeMultipartLimitExceeded, "LIMIT_EXCEEDED",
		"multipart payload exceeds a configured limit",
		"service/data/codec/multipart: part size, part count or aggregate size crossed its bound")

	// LimitsInvalid fires when a Limits value carries a negative bound.
	LimitsInvalid = errs.Define(CodeMultipartLimitsInvalid, "LIMITS_INVALID",
		"multipart limits are not usable",
		"service/data/codec/multipart: a negative bound is neither a limit nor a request for the default")
)
