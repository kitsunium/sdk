// Package multipart registers the multipart/form-data codec with the SDK's
// codec registry — and no other codec — when it is imported (ADR 0134):
//
//	import _ "github.com/kitsunium/sdk/pkg/v1/data/codec/multipart"
//
// The codec is RFC 7578's multipart/form-data on the standard library's
// mime/multipart. Its native value is a [Form] of [Part]s — a file upload is a
// part with a FileName and a ContentType; any other value travels as one
// JSON-mediated part. The boundary belongs in the Content-Type header, which a
// codec has nowhere to return, so [ContentType] recovers the header value from
// the body Marshal wrote. Parts, part sizes and the whole body are bounded.
// It streams.
//
// Everything that dispatches through the registry by format name then
// reads and writes it: the codec package's Marshal and Unmarshal,
// config.FSSource and i18n.LoadFS. Importing
// github.com/kitsunium/sdk/pkg/v1/data/codec instead registers every
// format the SDK ships; this package links this one codec, the core
// package declaring its codes and the standard library, and nothing else.
// Importing both is harmless: a format is registered by the package that
// implements it, which Go initialises once however many packages import it.
//
// # Errors
//
// Failures carry the range 0.3.41.*; match them with errs.HasCode, or the
// sentinels with errors.Is:
//
//	CodeMarshalFailed    0.3.41.1  a part could not be written
//	CodeUnmarshalFailed  0.3.41.2  a malformed part, a part with no name, a missing closing delimiter
//	CodeValueInvalid     0.3.41.3  an unusable value — no name, or a CR, LF or NUL in a header field
//	CodeBoundaryInvalid  0.3.41.4  no usable RFC 2046 boundary
//	CodeLimitExceeded    0.3.41.5  a part, the part count or the body crossed its bound
//	CodeLimitsInvalid    0.3.41.6  a negative bound
//
// No error message quotes the input.
package multipart
