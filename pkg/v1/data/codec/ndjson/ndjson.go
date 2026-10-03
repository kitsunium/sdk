//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/data/codec/ndjson .

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

import (
	corendjson "github.com/kitsunium/sdk/internal/core/data/codec/ndjson"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	// The implementation registers itself with the codec registry as it is
	// initialised; importing it is what registers the format.
	_ "github.com/kitsunium/sdk/internal/service/data/codec/ndjson"
)

// Format is the name newline-delimited JSON is registered under. It is an
// untyped constant, so it goes wherever a format name is taken — the codec
// package's Marshal, config.FSSource's string, i18n.LoadFS's codec.Format —
// without a conversion.
const Format = "ndjson"

// The error codes, range 0.3.11.*, declared in the core (ADR 0160).
const (
	// CodeMarshalFailed is 0.3.11.1: a record has no JSON encoding.
	CodeMarshalFailed errs.Code = corendjson.CodeNDJSONMarshalFailed
	// CodeUnmarshalFailed is 0.3.11.2: a line is not JSON its element can
	// hold.
	CodeUnmarshalFailed errs.Code = corendjson.CodeNDJSONUnmarshalFailed
	// CodeValueInvalid is 0.3.11.3: the value or the target is not a slice.
	CodeValueInvalid errs.Code = corendjson.CodeNDJSONValueInvalid
)

// The sentinels, for errors.Is: each carries the code of the same name.
var (
	// MarshalFailed is the sentinel of [CodeMarshalFailed].
	MarshalFailed = corendjson.MarshalFailed
	// UnmarshalFailed is the sentinel of [CodeUnmarshalFailed].
	UnmarshalFailed = corendjson.UnmarshalFailed
	// ValueInvalid is the sentinel of [CodeValueInvalid].
	ValueInvalid = corendjson.ValueInvalid
)
