// Package ndjson registers the NDJSON codec with the SDK's codec registry —
// and no other codec — when it is imported (ADR 0134):
//
//	import _ "github.com/kitsunium/sdk/pkg/v1/data/codec/ndjson"
//
// The codec writes and reads newline-delimited JSON: one encoding/json value
// per line, a stream of records. Its native value is a slice, one element per
// line; through the codec package's Marshal any other value is carried as a
// one-record stream. Its extensions are .ndjson and .jsonl.
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
// Failures carry the range 0.3.11.*; match them with errs.HasCode, or the
// sentinels with errors.Is:
//
//	CodeMarshalFailed    0.3.11.1  a record has no JSON encoding
//	CodeUnmarshalFailed  0.3.11.2  a line is not JSON its element can hold
//	CodeValueInvalid     0.3.11.3  the value or the target is not a slice
//
// No error message quotes the input.
package ndjson
