//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/data/codec/flatbuffers .

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

import (
	coreflatbuffers "github.com/kitsunium/sdk/internal/core/data/codec/flatbuffers"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	// The implementation registers itself with the codec registry as it is
	// initialised; importing it registers the format, and the names below
	// alias the types it owns.
	svcflatbuffers "github.com/kitsunium/sdk/internal/service/data/codec/flatbuffers"
)

// Format is the name FlatBuffers is registered under. It is an untyped constant, so
// it goes wherever a format name is taken — the codec package's Marshal,
// config.FSSource's string, i18n.LoadFS's codec.Format — without a
// conversion.
const Format = "flatbuffers"

// The error codes, range 0.3.23.*, declared in the core (ADR 0160).
const (
	// CodeBadType identifies Marshal was given neither a []byte nor a BytesProvider (0.3.23.1).
	CodeBadType errs.Code = coreflatbuffers.CodeFlatbuffersBadType
	// CodeBadTarget identifies Unmarshal was given neither a *[]byte nor a BytesAcceptor (0.3.23.2).
	CodeBadTarget errs.Code = coreflatbuffers.CodeFlatbuffersBadTarget
	// CodeTruncated identifies the buffer is shorter than its root offset, or past 64 MiB (0.3.23.3).
	CodeTruncated errs.Code = coreflatbuffers.CodeFlatbuffersTruncated
)

// The sentinels, for errors.Is: each carries the code of the same name.
var (
	// BadType is the sentinel of [CodeBadType].
	BadType = coreflatbuffers.FlatbuffersBadType
	// BadTarget is the sentinel of [CodeBadTarget].
	BadTarget = coreflatbuffers.FlatbuffersBadTarget
	// Truncated is the sentinel of [CodeTruncated].
	Truncated = coreflatbuffers.FlatbuffersTruncated
)

// BytesProvider is what a Marshal argument may implement to hand the codec
// its already-encoded FlatBuffer: the bytes are read, never copied, for the
// length of the call.
type BytesProvider = svcflatbuffers.BytesProvider

// BytesAcceptor is what an Unmarshal target may implement to receive the
// buffer. The slice is the caller's input, passed by reference: an
// implementation that keeps it copies it.
type BytesAcceptor = svcflatbuffers.BytesAcceptor
