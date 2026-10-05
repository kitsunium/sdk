// Package toml is the TOML codec, written against TOML v1.0.0
// (https://toml.io/en/v1.0.0) with the standard library alone. It implements
// codec.Codec, codec.StreamingCodec and codec.Appender.
//
// Decoding refuses what the specification refuses: a key or a table defined
// twice, a table extended from a place the specification does not allow, an
// integer outside int64, a control character in a string or a comment, bytes
// that are not UTF-8. It also accepts the four TOML v1.1.0 relaxations the
// library it replaced accepted — newlines, comments and a trailing comma in an
// inline table, the \e and \xHH escapes, and a time without seconds — so that
// no document that decoded before decodes no longer. Encoding writes TOML
// v1.0.0 only.
//
// A document decodes into a map or a struct; an untyped target receives
// map[string]any, []any, string, int64, float64, bool, time.Time for an offset
// date-time, and LocalDateTime, LocalDate and LocalTime for the three local
// kinds. A struct field is keyed by its toml tag, or by its name, matched
// exactly first and then without regard to case.
//
// Package toml — the decode: a parsed tree written into a Go value. Untyped
// targets (map[string]any, any, []any) are built natively; typed ones through
// reflection, guided by the cached typeInfo of each type.
//
// Package toml — scalars into Go values: strings, integers, floats, booleans
// and the four date and time kinds, each into the types that can hold it
// without losing it, or a refusal.
//
// Package toml — the typed half of the decode: tables into structs and maps,
// arrays into slices and arrays, through reflection.
//
// Package toml — the streaming codec.Decoder: the reader is one TOML
// document, read whole on the first Decode, within maxDocumentBytes.
//
// Package toml — the encode: a Go map or struct written as a TOML document.
// The layout is the one the previous library wrote, byte for byte on the
// values it accepted: a table's plain keys first, then its sub-tables and
// arrays of tables, each under its header; struct fields in declaration order
// and map keys sorted; strings literal when they can be, basic otherwise.
//
// Package toml — values written after a key: scalars, strings in the spelling
// that needs no escape when there is one, arrays and inline tables.
//
// Package toml — the streaming codec.Encoder: one TOML document per Encode
// call, written to the writer in one Write.
//
// Package toml — the private messages of the refusals that wrap a cause.
//
// Every refusal is one of the two sentinels internal/core/data/codec/toml
// declares for this package (ADR 0160). What went wrong travels in fields —
// problem, a fixed sentence; line and column; the dotted key and the Go type
// for a decode that does not fit its target — and never as a byte of the
// document or of the value, which may be a secret.
//
// Package toml — the three TOML values that have no Go type of their own: a
// date, a time of day and a date-time, each in no time zone. A decode into an
// untyped target (map[string]any, any) returns them; a decode into time.Time
// places them in time.Local, the only zone a local value can be read in.
//
// The three types keep the field layout and the methods of the
// github.com/pelletier/go-toml/v2 types this codec returned before it was
// written natively, so a type switch moves from one to the other by changing
// its import and nothing else.
//
// Package toml — dates and times: offset date-time, local date-time, local
// date and local time, read as RFC 3339 spells them with the two liberties
// TOML adds — a space for the T, and a fraction of any length, truncated past
// the nanosecond (§Offset Date-Time).
//
// Package toml — integers and floats: the token is checked against the
// grammar (§Integer, §Float) before any conversion, because strconv accepts
// spellings TOML refuses — a leading zero, a hexadecimal float, "Inf".
//
// Package toml — strings: basic and literal, on one line or several, the
// escape sequences of a basic string and the backslash that ends a line of a
// multi-line one (TOML v1.0.0 §String).
//
// Package toml — table headers, key-value pairs and keys: the half of the
// parser that decides where a value goes and whether TOML allows it there.
//
// Package toml — values: the dispatch on a value's first byte, arrays, inline
// tables and booleans.
//
// Package toml — the parser: one pass over a TOML v1.0.0 document that
// builds its tree and refuses, as the specification requires, a key or a
// table defined twice, a table extended from a place the specification does
// not allow, and every byte the grammar has no place for.
//
// Package toml — the document tree a parse builds: every table, array and
// scalar of one document in a single arena, linked by index, and the lookup
// that finds a key in a table in constant time however wide the table is.
//
// Package toml — what the codec knows about a Go type, computed once per type
// and cached: how a struct's fields map to keys under the toml struct tag,
// and which special roles a type plays.
//
// Package toml — the lexical tables the parser reads bytes through, and the
// fixed sentences a refusal is made of.
package toml
