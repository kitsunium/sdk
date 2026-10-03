//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/codec/toml .

// Package toml registers the TOML codec with the SDK's codec registry — and
// no other codec — when it is imported (ADR 0134):
//
//	import _ "github.com/kitsunium/sdk/pkg/v1/codec/toml"
//
// Everything that dispatches through the registry by format name then reads
// and writes TOML: config.FileSource and config.FSSource, i18n.LoadFS, and
// the codec package's Marshal and Unmarshal. Importing
// github.com/kitsunium/sdk/pkg/v1/codec instead registers every format the SDK
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
package toml

import (
	// The implementation registers itself with the codec registry as it is
	// initialised; importing it is most of this package's job.
	"github.com/kitsunium/sdk/internal/service/codec/toml"
)

// Format is the name TOML is registered under. It is an untyped constant, so
// it goes wherever a format name is taken — config.FSSource's string,
// i18n.LoadFS's codec.Format — without a conversion.
const Format = "toml"

// LocalDate is a calendar day in no time zone: what a TOML local date such as
// 1979-05-27 decodes to in an untyped target. It writes and reads itself as
// YYYY-MM-DD, through String, MarshalText and UnmarshalText, and AsTime places
// it at midnight in a zone.
type LocalDate = toml.LocalDate

// LocalTime is a time of day in no time zone: what a TOML local time such as
// 07:32:00.999 decodes to in an untyped target. Precision is the number of
// fractional digits the document wrote, which String writes back.
type LocalTime = toml.LocalTime

// LocalDateTime is a date and a time of day in no time zone: what a TOML local
// date-time such as 1979-05-27T07:32:00 decodes to in an untyped target. AsTime
// places it in a zone.
type LocalDateTime = toml.LocalDateTime
