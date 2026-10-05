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
	// The implementation registers itself with the codec registry as it is
	// initialised; importing it is what registers the format.
	_ "github.com/kitsunium/sdk/internal/service/data/codec/tlv"
)

// Format is the name TLV is registered under. It is an untyped constant, so it
// goes wherever a format name is taken — the codec package's Marshal,
// config.FSSource's string, i18n.LoadFS's codec.Format — without a conversion.
const Format = "tlv"
