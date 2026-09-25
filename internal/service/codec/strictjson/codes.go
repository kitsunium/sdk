// Package strictjson — range 0.3.72.* (ADR 0102 service/codec/strictjson block).
package strictjson

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.72.0 - 0.3.72.255

// CodeDocumentTooLarge identifies a JSON document longer than the bound the
// caller gave.
const CodeDocumentTooLarge errs.Code = 0x00_03_48_01 // 0.3.72.1

// CodeDocumentEmpty identifies a JSON document of zero bytes: no value at all,
// which a caller may read as "no body" rather than as a malformed one.
const CodeDocumentEmpty errs.Code = 0x00_03_48_02 // 0.3.72.2

// CodeDocumentMalformed identifies a document that is not exactly one
// well-formed JSON value: a syntax error, a truncation, trailing data, a
// duplicated member name, or invalid UTF-8.
const CodeDocumentMalformed errs.Code = 0x00_03_48_03 // 0.3.72.3

// CodeMemberUnknown identifies an object member the target type does not
// declare.
const CodeMemberUnknown errs.Code = 0x00_03_48_04 // 0.3.72.4

// CodeValueMismatched identifies a well-formed value the target cannot hold:
// the wrong JSON kind, a number out of the field's range, a value a field's
// own unmarshaler refused.
const CodeValueMismatched errs.Code = 0x00_03_48_05 // 0.3.72.5

// CodeMediaTypeUnsupported identifies a request body whose Content-Type is not
// application/json or a +json structured-syntax type.
const CodeMediaTypeUnsupported errs.Code = 0x00_03_48_06 // 0.3.72.6

// CodeDocumentUnreadable identifies a reader that failed before the document
// ended — a connection reset, a cancelled request.
const CodeDocumentUnreadable errs.Code = 0x00_03_48_07 // 0.3.72.7

// CodeDecodeMisconfigured identifies a call the decoder refuses before
// reading anything: a non-positive bound, or a target that is not a non-nil
// pointer.
const CodeDecodeMisconfigured errs.Code = 0x00_03_48_08 // 0.3.72.8
