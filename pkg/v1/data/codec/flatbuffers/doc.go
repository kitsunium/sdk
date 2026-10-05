// Package flatbuffers registers the FlatBuffers codec with the SDK's codec
// registry — and no other codec — when it is imported (ADR 0134):
//
//	import _ "github.com/kitsunium/sdk/pkg/v1/data/codec/flatbuffers"
//
// The codec is a passthrough for payloads flatc-generated code already
// encoded: Marshal takes a []byte or a [BytesProvider] and hands the bytes
// back, Unmarshal fills a *[]byte or a [BytesAcceptor]. Reading the fields of
// a buffer stays in the generated accessors; the codec checks only that the
// buffer is long enough to hold a root offset and short enough to be read.
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
// Failures carry the range 0.3.23.*; match them with errs.HasCode, or the
// sentinels with errors.Is:
//
//	CodeBadType    0.3.23.1  Marshal was given neither a []byte nor a BytesProvider
//	CodeBadTarget  0.3.23.2  Unmarshal was given neither a *[]byte nor a BytesAcceptor
//	CodeTruncated  0.3.23.3  the buffer is shorter than its root offset, or past 64 MiB
//
// No error message quotes the input.
package flatbuffers
