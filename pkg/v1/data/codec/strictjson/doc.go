// Package strictjson decodes exactly one JSON document into a Go value,
// within a byte bound, refusing what a permissive decoder lets through — and
// no refusal ever repeats a byte of the document.
//
//	var in CreateItem
//	if err := strictjson.Decode(file, &in, 1<<20); err != nil {
//		return err // DOCUMENT_TOO_LARGE, MEMBER_UNKNOWN, VALUE_MISMATCHED…
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
//	CodeMediaTypeUnsupported  415  a request body (httpbody) that does not declare application/json or +json
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
// The JSON body of an HTTP request is decoded by the package beneath this one,
// github.com/kitsunium/sdk/pkg/v1/data/codec/strictjson/httpbody, whose
// DecodeRequest adds what only an HTTP body has — a bound net/http enforces
// too, an empty body settled first, a media type that must declare JSON — and
// refuses with this package's codes. It is a package of its own so that this
// one links no net/http: a program decoding documents from files, queues or
// sockets does not carry an HTTP stack it never serves.
package strictjson
