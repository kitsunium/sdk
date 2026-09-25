// Package strictjson decodes exactly one JSON document into a Go value,
// refusing everything the permissive decoder lets through, within a byte
// bound, with errors that never quote the input.
//
// The universal codec's json format is encoding/json with its defaults: no
// size limit, unknown members silently dropped, a member matched to a field
// case-insensitively, a duplicated name resolved by whichever came last, and
// trailing data after the value ignored. Each of those is a way for one
// document to be read two ways — by this program and by whatever else parsed
// it first — and none of them fails. This package is the other decoder, built
// on encoding/json/v2 whose defaults already refuse most of them:
//
//   - a document longer than the bound is refused before it is buffered past
//     the bound;
//   - an object member the target does not declare is refused, and so is one
//     that differs from a declared name only by case;
//   - a duplicated member name, invalid UTF-8, and anything after the one
//     value are refused;
//   - a number out of the field's range is refused rather than wrapped.
//
// Every refusal is a typed error whose text is a fixed sentence: the
// document's bytes appear in no Public, no Private and no wrapped cause.
// Where the document failed is available through PointerOf, as a JSON
// Pointer built from the document's own member names — a location, never a
// value.
package strictjson

import (
	"encoding/json/jsontext"
	"errors"
	"io"
	"math"
	"reflect"

	jsonv2 "encoding/json/v2"

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
	if invalid := checkArguments(v, maxBytes); invalid != nil {
		//: DecodeMisconfigured, naming the argument.
		return invalid
	}
	//: nothing to read from is the caller's bug, not an empty document.
	if r == nil {
		//: DecodeMisconfigured, naming the argument.
		return misconfigured("r")
	}
	//: a plain reader has no size refusal of its own to recognise.
	return decode(r, v, maxBytes, nil)
}

// checkArguments refuses the two calls no input can make succeed.
func checkArguments(v any, maxBytes int64) error {
	//: the two readings of a non-positive bound are opposites.
	if maxBytes <= 0 {
		//: DecodeMisconfigured, naming the argument.
		return misconfigured("maxBytes")
	}
	//: a value decode cannot write into is the caller's bug, found before
	//: reading a byte of somebody else's input.
	if target := reflect.ValueOf(v); target.Kind() != reflect.Pointer || target.IsNil() {
		//: DecodeMisconfigured, naming the argument.
		return misconfigured("v")
	}
	//: usable.
	return nil
}

// decode reads and decodes one bounded document. oversize recognises a read
// error that is itself a size refusal — http.MaxBytesError, for a request
// body — so it is reported as DocumentTooLarge rather than as a failed read;
// nil recognises none.
func decode(r io.Reader, v any, maxBytes int64, oversize func(error) bool) error {
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

// PointerOf returns where a document Decode or DecodeRequest refused went
// wrong, as a JSON Pointer (RFC 6901) — "" is the document itself — and
// reports whether err carries one.
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
		sentinel := ValueMismatched
		//: the one semantic refusal with a code of its own.
		if errors.Is(decodeErr, jsonv2.ErrUnknownName) {
			sentinel = MemberUnknown
		}
		//: located, and nothing more.
		return located(sentinel, semantic.JSONPointer, semantic.ByteOffset)
	}
	//: a syntactic refusal: not one well-formed value.
	if syntactic, isSyntactic := errors.AsType[*jsontext.SyntacticError](decodeErr); isSyntactic {
		//: located, and nothing more.
		return located(DocumentMalformed, syntactic.JSONPointer, syntactic.ByteOffset)
	}
	//: an input that ended inside the value, reported without a location.
	return errs.Wrap(DocumentMalformed, errs.WrapParams{})
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

// misconfigured refuses a call before anything is read.
func misconfigured(argument string) error {
	//: the argument's name, never its value.
	return errs.Wrap(DecodeMisconfigured, errs.WrapParams{}, errs.String(fieldArgument, argument))
}
