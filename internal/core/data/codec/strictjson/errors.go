// Package strictjson — the sentinel *errs.Error values, one per code. Each
// Reason derives from its var name or from its Code constant (ADR 0020);
// the Public and Private texts are the ones the service package always
// emitted, so moving the declaration changed no rendering.
//
// Every Public and every Private below is a fixed sentence. None of them can
// carry a byte of the document, because the document is what a caller least
// controls and an error message is where input classically leaks back out —
// into a response, into a log someone else reads.
package strictjson

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitDataErr matches sysexits EX_DATAERR (65): the input was wrong.
const exitDataErr int = 65

// exitIOErr matches sysexits EX_IOERR (74): the input could not be read.
const exitIOErr int = 74

// exitSoftware matches sysexits EX_SOFTWARE (70): the caller's code is wrong.
const exitSoftware int = 70

// The HTTP statuses the refusals carry, as RFC 9110 numbers them. Literals,
// because the core does not import net/http for a number (ADR 0160); each
// is the value of the net/http constant its comment names.
const (
	// httpBadRequest is 400, http.StatusBadRequest: the document is wrong.
	httpBadRequest int = 400
	// httpContentTooLarge is 413, http.StatusRequestEntityTooLarge: the
	// document is longer than the bound.
	httpContentTooLarge int = 413
	// httpUnsupportedMediaType is 415, http.StatusUnsupportedMediaType: the
	// body does not declare itself JSON.
	httpUnsupportedMediaType int = 415
)

var (
	// DocumentTooLarge refuses a document longer than the caller's bound,
	// whatever the bytes beyond it would have said.
	DocumentTooLarge = errs.Define(CodeDocumentTooLarge, "DOCUMENT_TOO_LARGE",
		"The JSON document is larger than the limit",
		"service/data/codec/strictjson: more bytes arrived than the bound allows; the limit field is the bound",
		errs.WithHTTPStatus(httpContentTooLarge), errs.WithExitCode(exitDataErr))

	// DocumentEmpty reports a document of zero bytes. It is its own code so a
	// caller for whom a body is optional can tell "no body" from "a broken
	// one" without reading the input again.
	DocumentEmpty = errs.Define(CodeDocumentEmpty, "DOCUMENT_EMPTY",
		"The JSON document is empty",
		"service/data/codec/strictjson: the reader ended before the first byte",
		errs.WithHTTPStatus(httpBadRequest), errs.WithExitCode(exitDataErr))

	// DocumentMalformed refuses a document that is not exactly one
	// well-formed JSON value.
	DocumentMalformed = errs.Define(CodeDocumentMalformed, "DOCUMENT_MALFORMED",
		"The JSON document is not well-formed",
		"service/data/codec/strictjson: a syntax error, a truncation, trailing data, a duplicate name or invalid UTF-8; the offset field locates it",
		errs.WithHTTPStatus(httpBadRequest), errs.WithExitCode(exitDataErr))

	// MemberUnknown refuses an object member the target does not declare,
	// including one that differs from a declared name only by case.
	MemberUnknown = errs.Define(CodeMemberUnknown, "MEMBER_UNKNOWN",
		"The JSON document has a member the target does not accept",
		"service/data/codec/strictjson: an object member matches no field of the target; PointerOf locates it",
		errs.WithHTTPStatus(httpBadRequest), errs.WithExitCode(exitDataErr))

	// ValueMismatched refuses a well-formed value the target cannot hold.
	ValueMismatched = errs.Define(CodeValueMismatched, "VALUE_MISMATCHED",
		"The JSON document has a value of the wrong type or out of range",
		"service/data/codec/strictjson: a value does not fit the field it maps to; PointerOf locates it",
		errs.WithHTTPStatus(httpBadRequest), errs.WithExitCode(exitDataErr))

	// MediaTypeUnsupported refuses a request body that does not declare itself
	// JSON.
	MediaTypeUnsupported = errs.Define(CodeMediaTypeUnsupported, "MEDIA_TYPE_UNSUPPORTED",
		"The request body must be application/json or a +json media type",
		"service/data/codec/strictjson: the Content-Type is absent, unparsable, or names another media type",
		errs.WithHTTPStatus(httpUnsupportedMediaType), errs.WithExitCode(exitDataErr))

	// DocumentUnreadable wraps a reader that failed before the document
	// ended. The reader's own error stays in the chain.
	DocumentUnreadable = errs.Define(CodeDocumentUnreadable, "DOCUMENT_UNREADABLE",
		"The JSON document could not be read",
		"service/data/codec/strictjson: the reader returned an error other than end-of-input",
		errs.WithHTTPStatus(httpBadRequest), errs.WithExitCode(exitIOErr))

	// DecodeMisconfigured refuses a call the decoder can never honour: a
	// non-positive bound — whose two readings, "unbounded" and "nothing", are
	// opposites (ADR 0031) — or a target that is not a non-nil pointer.
	DecodeMisconfigured = errs.Define(CodeDecodeMisconfigured, "DECODE_MISCONFIGURED",
		"The JSON decoder was called with an argument it cannot use",
		"service/data/codec/strictjson: the field names the argument: a non-positive bound or a target that is not a non-nil pointer",
		errs.WithExitCode(exitSoftware))
)
