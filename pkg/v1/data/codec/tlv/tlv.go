//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/data/codec/tlv .

// Package tlv registers the TLV codec with the SDK's codec registry — and no
// other codec — when it is imported (ADR 0134):
//
//	import _ "github.com/kitsunium/sdk/pkg/v1/data/codec/tlv"
//
// The codec is the SDK's self-describing Type-Length-Value binary: every
// value carries its kind and its length, so a document decodes without a
// schema, into a typed target or into an interface. Structs, maps, slices
// and the scalar kinds are written by reflection. Input is bounded in size
// and depth. It streams.
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
// Failures carry the range 0.3.22.*; match them with errs.HasCode, or the
// sentinels with errors.Is:
//
//	CodeMarshalFailed    0.3.22.1  the value could not be encoded, or the writer failed
//	CodeUnmarshalFailed  0.3.22.2  a malformed record, a type mismatch, or the reader failed
//	CodeUnsupportedType  0.3.22.3  a channel, a function, a complex number, an unsafe pointer
//	CodeDepthExceeded    0.3.22.4  nesting past 32 levels
//	CodeSizeExceeded     0.3.22.5  an input past 10 MiB
//	CodeTruncated        0.3.22.6  the input ends inside a record
//
// No error message quotes the input.
package tlv

import (
	coretlv "github.com/kitsunium/sdk/internal/core/data/codec/tlv"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	// The implementation registers itself with the codec registry as it is
	// initialised; importing it is what registers the format.
	_ "github.com/kitsunium/sdk/internal/service/data/codec/tlv"
)

// Format is the name TLV is registered under. It is an untyped constant, so it
// goes wherever a format name is taken — the codec package's Marshal,
// config.FSSource's string, i18n.LoadFS's codec.Format — without a conversion.
const Format = "tlv"

// The error codes, range 0.3.22.*, declared in the core (ADR 0160).
const (
	// CodeMarshalFailed is 0.3.22.1: the value could not be encoded, or the
	// writer failed.
	CodeMarshalFailed errs.Code = coretlv.CodeTLVMarshalFailed
	// CodeUnmarshalFailed is 0.3.22.2: a malformed record, a type mismatch, or
	// the reader failed.
	CodeUnmarshalFailed errs.Code = coretlv.CodeTLVUnmarshalFailed
	// CodeUnsupportedType is 0.3.22.3: a channel, a function, a complex
	// number, an unsafe pointer.
	CodeUnsupportedType errs.Code = coretlv.CodeTLVUnsupportedType
	// CodeDepthExceeded is 0.3.22.4: nesting past 32 levels.
	CodeDepthExceeded errs.Code = coretlv.CodeTLVDepthExceeded
	// CodeSizeExceeded is 0.3.22.5: an input past 10 MiB.
	CodeSizeExceeded errs.Code = coretlv.CodeTLVSizeExceeded
	// CodeTruncated is 0.3.22.6: the input ends inside a record.
	CodeTruncated errs.Code = coretlv.CodeTLVTruncated
)

// The sentinels, for errors.Is: each carries the code of the same name.
var (
	// MarshalFailed is the sentinel of [CodeMarshalFailed].
	MarshalFailed = coretlv.MarshalFailed
	// UnmarshalFailed is the sentinel of [CodeUnmarshalFailed].
	UnmarshalFailed = coretlv.UnmarshalFailed
	// UnsupportedType is the sentinel of [CodeUnsupportedType].
	UnsupportedType = coretlv.UnsupportedType
	// DepthExceeded is the sentinel of [CodeDepthExceeded].
	DepthExceeded = coretlv.DepthExceeded
	// SizeExceeded is the sentinel of [CodeSizeExceeded].
	SizeExceeded = coretlv.SizeExceeded
	// Truncated is the sentinel of [CodeTruncated].
	Truncated = coretlv.Truncated
)
