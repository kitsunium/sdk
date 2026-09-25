//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/codec/strictjson .

// Package strictjson decodes exactly one JSON document into a Go value,
// within a byte bound, refusing what a permissive decoder lets through — and
// no refusal ever repeats a byte of the document.
//
//	var in CreateItem
//	if err := strictjson.DecodeRequest(w, r, &in, 1<<20); err != nil {
//		http.Error(w, errs.PublicOf(err), errs.HTTPStatusOf(err)) // 400, 413 or 415
//		return
//	}
//
// # Why a second JSON decoder
//
// The codec package's json format is encoding/json with its defaults, which
// is the right thing for data this program wrote. For a document somebody
// else wrote it is five ways for one document to be read two ways, none of
// which fails: no size limit, unknown members silently dropped, a member
// matched to a field case-insensitively, a duplicated name won by whichever
// came last, and data after the value ignored. This package refuses all five,
// plus invalid UTF-8 and a number out of its field's range. It is built on
// encoding/json/v2, whose defaults already refuse most of them.
//
// # The refusals
//
// Each is a typed error with a fixed sentence and an HTTP status, matched with
// errs.HasCode:
//
//	CodeDocumentTooLarge      413  longer than the bound — the part read is not decoded as if it were whole
//	CodeDocumentEmpty         400  zero bytes: "no body", which a caller may accept
//	CodeDocumentMalformed     400  syntax, truncation, trailing data, a duplicate name, invalid UTF-8
//	CodeMemberUnknown         400  a member the target does not declare, including a case-only variant
//	CodeValueMismatched       400  the wrong kind, a number out of range, a field's own unmarshaler refused
//	CodeMediaTypeUnsupported  415  a request body that does not declare application/json or +json
//	CodeDocumentUnreadable    400  the reader failed before the document ended
//	CodeDecodeMisconfigured   500  a non-positive bound, or a target that is not a non-nil pointer
//
// A zero bound is refused rather than read as "unlimited": the two readings of
// zero are opposites, and the unlimited one is a memory-exhaustion bug.
//
// # Never the input
//
// No Public, no Private and no field of a refusal carries a value from the
// document, and the decoder's own error — whose text quotes the offending
// member name and, for a field's own unmarshaler, the offending value — is not
// kept in the chain. Where the document failed is available separately, from
// PointerOf, as a JSON Pointer ("/items/1/price") built from the document's own
// member names and indices and bounded to MaxPointerBytes: a location, never
// a value, and still the document's text, so render it as untrusted.
//
// # Request bodies
//
// DecodeRequest adds what only an HTTP body has. The body is read through
// http.MaxBytesReader, so a body past the bound also tells net/http to close
// the connection instead of draining what the client keeps sending. An empty
// body is CodeDocumentEmpty before its Content-Type is looked at — treat that
// code as "no body" where the body is optional. A non-empty body must declare
// application/json or a structured-syntax +json type (RFC 6839) or it is not
// parsed at all.
package strictjson

import (
	"io"
	"net/http"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcstrict "github.com/kitsunium/sdk/internal/service/codec/strictjson"
)

// MaxPointerBytes bounds the JSON Pointer PointerOf returns; a longer one is
// cut back to the deepest ancestor that fits.
const MaxPointerBytes int = svcstrict.MaxPointerBytes

// CodeDocumentTooLarge identifies a document longer than the bound (413).
const CodeDocumentTooLarge errs.Code = svcstrict.CodeDocumentTooLarge

// CodeDocumentEmpty identifies a document of zero bytes (400).
const CodeDocumentEmpty errs.Code = svcstrict.CodeDocumentEmpty

// CodeDocumentMalformed identifies a document that is not exactly one
// well-formed JSON value (400).
const CodeDocumentMalformed errs.Code = svcstrict.CodeDocumentMalformed

// CodeMemberUnknown identifies a member the target does not declare (400).
const CodeMemberUnknown errs.Code = svcstrict.CodeMemberUnknown

// CodeValueMismatched identifies a value the target cannot hold (400).
const CodeValueMismatched errs.Code = svcstrict.CodeValueMismatched

// CodeMediaTypeUnsupported identifies a request body that does not declare
// JSON (415).
const CodeMediaTypeUnsupported errs.Code = svcstrict.CodeMediaTypeUnsupported

// CodeDocumentUnreadable identifies a reader that failed before the document
// ended (400).
const CodeDocumentUnreadable errs.Code = svcstrict.CodeDocumentUnreadable

// CodeDecodeMisconfigured identifies a call no input can satisfy: a
// non-positive bound, or a target that is not a non-nil pointer.
const CodeDecodeMisconfigured errs.Code = svcstrict.CodeDecodeMisconfigured

var (
	// DocumentTooLarge refuses a document longer than the bound.
	DocumentTooLarge = svcstrict.DocumentTooLarge
	// DocumentEmpty reports a document of zero bytes.
	DocumentEmpty = svcstrict.DocumentEmpty
	// DocumentMalformed refuses a document that is not one well-formed value.
	DocumentMalformed = svcstrict.DocumentMalformed
	// MemberUnknown refuses a member the target does not declare.
	MemberUnknown = svcstrict.MemberUnknown
	// ValueMismatched refuses a value the target cannot hold.
	ValueMismatched = svcstrict.ValueMismatched
	// MediaTypeUnsupported refuses a request body that does not declare JSON.
	MediaTypeUnsupported = svcstrict.MediaTypeUnsupported
	// DocumentUnreadable wraps a reader that failed before the document ended.
	DocumentUnreadable = svcstrict.DocumentUnreadable
	// DecodeMisconfigured refuses a call no input can satisfy.
	DecodeMisconfigured = svcstrict.DecodeMisconfigured
)

// Decode reads exactly one JSON value from r into v, a non-nil pointer,
// reading at most maxBytes bytes of it — the bound plus the one byte that
// proves it was exceeded, never more. On a refusal v may be partly written.
func Decode(r io.Reader, v any, maxBytes int64) error {
	//: delegate verbatim to the service implementation.
	return svcstrict.Decode(r, v, maxBytes)
}

// DecodeRequest decodes the JSON body of req into v as Decode does, through
// http.MaxBytesReader, after refusing an empty body (CodeDocumentEmpty) and a
// body that does not declare JSON (CodeMediaTypeUnsupported). w is used only
// to have net/http close the connection after an oversized body; nil is
// accepted.
func DecodeRequest(w http.ResponseWriter, req *http.Request, v any, maxBytes int64) error {
	//: delegate verbatim to the service implementation.
	return svcstrict.DecodeRequest(w, req, v, maxBytes)
}

// PointerOf returns where a refused document went wrong, as a JSON Pointer
// ("" is the document itself), and whether err carries one. It is the
// document's own member names and indices, bounded to MaxPointerBytes — render
// it as untrusted text.
func PointerOf(err error) (pointer string, ok bool) {
	//: delegate verbatim to the service implementation.
	return svcstrict.PointerOf(err)
}
