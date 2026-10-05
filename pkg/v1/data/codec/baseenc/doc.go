// Package baseenc registers the base-N codec with the SDK's codec registry —
// and no other codec — when it is imported (ADR 0134):
//
//	import _ "github.com/kitsunium/sdk/pkg/v1/data/codec/baseenc"
//
// It registers nine Formats, one per alphabet, all through one pipeline: a
// value is encoded as JSON and the JSON text as base-N, and decoding runs the
// two steps backwards — so every alphabet carries any Go value encoding/json
// can, and swapping one alphabet for another is a change of Format name.
// Every Format streams.
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
// Failures carry the range 0.3.24.*; match them with errs.HasCode, or the
// sentinels with errors.Is:
//
//	CodeMarshalFailed    0.3.24.1  the value has no JSON encoding
//	CodeUnmarshalFailed  0.3.24.2  the decoded text is not JSON the target can hold
//	CodeDecodeFailed     0.3.24.3  the input is not text of the Format's alphabet
//	CodeSizeExceeded     0.3.24.4  an input past its bound — 10 MiB, 4 KiB for base58 and base62
//
// No error message quotes the input.
package baseenc
