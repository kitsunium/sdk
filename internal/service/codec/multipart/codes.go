// Package multipart — range 0.3.41.* (ADR 0005 service/codec/multipart block).
package multipart

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.41.0 - 0.3.41.255

// CodeMultipartMarshalFailed identifies a failure while writing a
// multipart/form-data body — a part header, a part body, or the JSON
// intermediate produced for the JSON-mediated single-part shape.
const CodeMultipartMarshalFailed errs.Code = 0x00_03_29_01 // 0.3.41.1

// CodeMultipartUnmarshalFailed identifies a failure while parsing a
// multipart/form-data body: a malformed part header, a missing closing
// delimiter, or a JSON-mediated part whose payload is not valid JSON.
const CodeMultipartUnmarshalFailed errs.Code = 0x00_03_29_02 // 0.3.41.2

// CodeMultipartValueInvalid identifies a Marshal / Unmarshal / Encode call
// whose argument shape the codec cannot honour — an Unmarshal target that is
// not a non-nil pointer, a Part carrying no field name, or a Part whose Name,
// FileName or ContentType carries a CR, LF or NUL (field "field" names which).
const CodeMultipartValueInvalid errs.Code = 0x00_03_29_03 // 0.3.41.3

// CodeMultipartBoundaryInvalid identifies a boundary the codec cannot use:
// caller-supplied but outside the RFC 2046 charset / length, or absent from a
// body whose first delimiter line could not be recovered.
const CodeMultipartBoundaryInvalid errs.Code = 0x00_03_29_04 // 0.3.41.4

// CodeMultipartLimitExceeded identifies a payload that crossed one of the
// configured bounds — per-part bytes, part count, or aggregate bytes.
const CodeMultipartLimitExceeded errs.Code = 0x00_03_29_05 // 0.3.41.5

// CodeMultipartLimitsInvalid identifies a Limits value the codec refuses to
// interpret: a negative bound is neither a bound nor a documented default,
// and guessing which one the caller meant is exactly what ADR 0031 forbids.
const CodeMultipartLimitsInvalid errs.Code = 0x00_03_29_06 // 0.3.41.6
