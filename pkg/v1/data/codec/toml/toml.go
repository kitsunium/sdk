//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/data/codec/toml .

// Package toml registers the TOML codec with the SDK's codec registry — and
// no other codec — when it is imported (ADR 0134):
//
//	import _ "github.com/kitsunium/sdk/pkg/v1/data/codec/toml"
//
// Everything that dispatches through the registry by format name then reads
// and writes TOML: config.FileSource and config.FSSource, i18n.LoadFS, and
// the codec package's Marshal and Unmarshal. Importing
// github.com/kitsunium/sdk/pkg/v1/data/codec instead registers every format the SDK
// ships — BSON, CBOR, MessagePack and the rest — which
// a program that only reads TOML does not need to link. This package links
// the TOML codec, which the SDK implements with the standard library alone,
// and no module outside the SDK.
//
// The codec reads TOML v1.0.0 (https://toml.io/en/v1.0.0), refusing what the
// specification refuses — a key or a table defined twice, an integer outside
// int64, bytes that are not UTF-8 — and accepts the four TOML v1.1.0
// relaxations: newlines, comments and a trailing comma in an inline table, the
// \e and \xHH escapes, and a time without seconds. It writes TOML v1.0.0.
// A refusal never quotes the document; it names the problem, the line and the
// column.
//
// A document decoded into map[string]any or any holds map[string]any, []any,
// string, int64, float64, bool, time.Time for an offset date-time, and
// [LocalDateTime], [LocalDate] and [LocalTime] for the three kinds of TOML
// value that carry no time zone. Decoded into a time.Time instead, a local
// value is placed in time.Local.
//
// Importing both packages is harmless: a format is registered by the package
// that implements it, which Go initialises once however many packages import
// it.
//
// # Errors
//
// Failures carry the range 0.3.5.*; match them with errs.HasCode, or the
// sentinels with errors.Is:
//
//	CodeMarshalFailed    0.3.5.1  a Go value TOML cannot represent, or a writer that failed
//	CodeUnmarshalFailed  0.3.5.2  a document that is not TOML, past a bound, or a value its target cannot hold
package toml

import (
// The implementation registers itself with the codec registry as it is
// initialised; importing it is most of this package's job.
)

// Format is the name TOML is registered under. It is an untyped constant, so
// it goes wherever a format name is taken — config.FSSource's string,
// i18n.LoadFS's codec.Format — without a conversion.
const Format = "toml"
