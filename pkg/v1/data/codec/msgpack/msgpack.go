//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/data/codec/msgpack .

// Package msgpack registers the MessagePack codec with the SDK's codec
// registry — and no other codec — when it is imported (ADR 0134):
//
//	import _ "github.com/kitsunium/sdk/pkg/v1/data/codec/msgpack"
//
// The codec is the msgpack.org specification written on the standard
// library: maps, arrays, the integer and float families, strings, binary,
// the timestamp extension, struct fields by their msgpack tag, and types
// that marshal themselves. Input is bounded in size and depth, and read
// without allocating what a declared length claims before the bytes are
// there. It streams.
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
// Failures carry the range 0.3.7.*; match them with errs.HasCode, or the
// sentinels with errors.Is:
//
//	CodeMarshalFailed    0.3.7.1  a value MessagePack cannot represent, nesting past the bound, a writer's failure
//	CodeUnmarshalFailed  0.3.7.2  malformed or truncated input, past a bound, or a value the target cannot hold
//
// No error message quotes the input.
package msgpack

import (
	// The implementation registers itself with the codec registry as it is
	// initialised; importing it is what registers the format.
	_ "github.com/kitsunium/sdk/internal/service/data/codec/msgpack"
)

// Format is the name MessagePack is registered under. It is an untyped
// constant, so it goes wherever a format name is taken — the codec package's
// Marshal, config.FSSource's string, i18n.LoadFS's codec.Format — without a
// conversion.
const Format = "msgpack"
