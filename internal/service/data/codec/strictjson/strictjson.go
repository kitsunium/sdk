package strictjson

import (
	"encoding/json/jsontext"
	"errors"
	"io"
	"math"
	"reflect"

	jsonv2 "encoding/json/v2"

	corestrictjson "github.com/kitsunium/sdk/internal/core/data/codec/strictjson"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Field names the refusals carry.
const (
	// fieldPointer carries where the document failed, as a JSON Pointer.
	fieldPointer string = "pointer"
	// fieldOffset carries the byte offset at which the decoder stopped.
	fieldOffset string = "offset"
	// fieldLimit carries the bound a too-large document exceeded.
	fieldLimit string = "limit"
	// fieldArgument names the argument a misconfigured call got wrong.
	fieldArgument string = "argument"
	// fieldCause carries the message of a reader that failed.
	fieldCause string = "cause"
)

// MaxPointerBytes bounds the JSON Pointer a refusal carries. A pointer is
// built from the document's member names, so its length is the document's
// to choose; past the bound it is cut back to the deepest ancestor that fits,
// which still points at the right place, just less precisely.
const MaxPointerBytes int = 256

// Decode reads exactly one JSON value from r into v, which must be a non-nil
// pointer, reading at most maxBytes bytes of it.
//
// It refuses — with DocumentTooLarge, DocumentEmpty, DocumentMalformed,
// MemberUnknown, ValueMismatched or DocumentUnreadable — every document the
// package comment lists, and DecodeMisconfigured when maxBytes is not
// positive, v is not a non-nil pointer, or r is nil. On a refusal v may have
// been partly written; a caller that keeps v after an error keeps a
// half-decoded value.
//
// A document longer than maxBytes is refused whatever the decoder made of the
// part it read: the truncation, not the prefix, is the finding. The reader is
// never read past maxBytes + 1 bytes.
func Decode(r io.Reader, v any, maxBytes int64) error {
	//: a call that can never succeed is refused before reading a byte.
	if invalid := CheckArguments(v, maxBytes); invalid != nil {
		//: DecodeMisconfigured, naming the argument.
		return invalid
	}
	//: nothing to read from is the caller's bug, not an empty document.
	if r == nil {
		//: DecodeMisconfigured, naming the argument.
		return Misconfigured("r")
	}
	//: a plain reader has no size refusal of its own to recognise.
	return DecodeChecked(r, v, maxBytes, nil)
}

// CheckArguments refuses the two calls no input can make succeed — a
// non-positive bound, and a v that is not a non-nil pointer — with
// DecodeMisconfigured naming the argument, and returns nil otherwise. Decode
// runs it first; a caller reading from something other than a plain reader
// (the HTTP form in the httpbody package) runs it before touching its input,
// so the same call is refused the same way through every door.
func CheckArguments(v any, maxBytes int64) error {
	//: the two readings of a non-positive bound are opposites.
	if maxBytes <= 0 {
		//: DecodeMisconfigured, naming the argument.
		return Misconfigured("maxBytes")
	}
	//: a value decode cannot write into is the caller's bug, found before
	//: reading a byte of somebody else's input.
	if target := reflect.ValueOf(v); target.Kind() != reflect.Pointer || target.IsNil() {
		//: DecodeMisconfigured, naming the argument.
		return Misconfigured("v")
	}
	//: usable.
	return nil
}

// DecodeChecked reads and decodes one bounded document from r into v, for a
// caller that has already refused what CheckArguments refuses and holds a
// non-nil reader. oversize recognises a read error that is itself a size
// refusal — http.MaxBytesError, for a request body — so it is reported as
// DocumentTooLarge rather than as a failed read; nil recognises none.
//
// It is how the HTTP form reuses this decoder without this package naming
// net/http: the recogniser is the caller's, and so is the import.
func DecodeChecked(r io.Reader, v any, maxBytes int64, oversize func(error) bool) error {
	//: one byte past the bound shows a document that does not fit — unless
	//: the bound is already the largest there is.
	budget := maxBytes
	if budget < math.MaxInt64 {
		budget++
	}
	counted := &boundedReader{source: r, remaining: budget}
	decodeErr := jsonv2.UnmarshalRead(counted, v, jsonv2.RejectUnknownMembers(true))
	//: the verdict of the reader first: a document that did not fit, or did
	//: not arrive, makes whatever the decoder concluded an artefact.
	if readErr := counted.verdict(maxBytes, oversize); readErr != nil {
		//: DocumentTooLarge, DocumentEmpty or DocumentUnreadable.
		return readErr
	}
	//: decoded exactly one value, with nothing after it.
	if decodeErr == nil {
		//: v holds the document.
		return nil
	}
	//: the decoder's refusal, reduced to a code, an offset and a pointer.
	return classify(decodeErr)
}

// PointerOf returns where a document Decode refused went wrong — or one the
// httpbody package's DecodeRequest refused — as a JSON Pointer (RFC 6901),
// "" being the document itself, and reports whether err carries one.
//
// The pointer is built from the document's own member names and array
// indices, so it is the input's text, bounded to MaxPointerBytes: a location,
// never a value. Render it as untrusted — escape it in HTML, quote it in a log.
func PointerOf(err error) (pointer string, ok bool) {
	//: the refusal carries it as a field; nothing else does.
	for _, field := range errs.FieldsOf(err) {
		//: the one field this package writes it under.
		if field.Key() == fieldPointer {
			//: as recorded.
			return field.StringValue(), true
		}
	}
	//: not a located refusal from this package.
	return "", false
}

// classify turns a decoder error into this package's refusal. The decoder's
// own error is NOT kept as the cause: its text quotes the offending member
// name and, for a field's own unmarshaler, the offending value.
func classify(decodeErr error) error {
	//: a semantic refusal: well-formed, but not what the target holds.
	if semantic, isSemantic := errors.AsType[*jsonv2.SemanticError](decodeErr); isSemantic {
		sentinel := corestrictjson.ValueMismatched
		//: the one semantic refusal with a code of its own.
		if errors.Is(decodeErr, jsonv2.ErrUnknownName) {
			sentinel = corestrictjson.MemberUnknown
		}
		//: located, and nothing more.
		return located(sentinel, semantic.JSONPointer, semantic.ByteOffset)
	}
	//: a syntactic refusal: not one well-formed value.
	if syntactic, isSyntactic := errors.AsType[*jsontext.SyntacticError](decodeErr); isSyntactic {
		//: located, and nothing more.
		return located(corestrictjson.DocumentMalformed, syntactic.JSONPointer, syntactic.ByteOffset)
	}
	//: an input that ended inside the value, reported without a location.
	return errs.Wrap(corestrictjson.DocumentMalformed, errs.WrapParams{})
}

// located builds a refusal carrying where the document failed.
func located(sentinel *errs.Error, pointer jsontext.Pointer, offset int64) error {
	//: the sentinel is the origin, so its code and its fixed text win.
	return errs.Wrap(sentinel, errs.WrapParams{},
		errs.String(fieldPointer, boundPointer(string(pointer))), errs.Int64(fieldOffset, offset))
}

// boundPointer cuts a pointer longer than MaxPointerBytes back to the deepest
// ancestor that fits, so what is kept still names a real location.
func boundPointer(pointer string) string {
	//: the ordinary case.
	if len(pointer) <= MaxPointerBytes {
		//: unchanged.
		return pointer
	}
	cut := pointer[:MaxPointerBytes]
	//: a reference token is cut at its separator, never in its middle.
	for index := len(cut) - 1; index >= 0; index-- {
		//: the start of the last whole token that fits.
		if cut[index] == '/' {
			//: the ancestor it names.
			return cut[:index]
		}
	}
	//: one token longer than the bound: the document itself is the location.
	return ""
}

// Misconfigured refuses a call before anything is read: DecodeMisconfigured,
// with the argument's name — never its value — as a field.
func Misconfigured(argument string) error {
	//: the argument's name, never its value.
	return errs.Wrap(corestrictjson.DecodeMisconfigured, errs.WrapParams{}, errs.String(fieldArgument, argument))
}
