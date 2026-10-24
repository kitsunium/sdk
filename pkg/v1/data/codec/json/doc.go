// Package json registers the JSON codec with the SDK's codec registry — and
// no other codec — when it is imported (ADR 0134):
//
//	import _ "github.com/kitsunium/sdk/pkg/v1/data/codec/json"
//
// Everything that dispatches through the registry by format name then reads
// and writes JSON: config.FileSource and config.FSSource, i18n.LoadFS, and
// the codec package's Marshal and Unmarshal. Importing
// github.com/kitsunium/sdk/pkg/v1/data/codec instead registers every format the SDK
// ships — BSON, CBOR, MessagePack and the rest — which
// a program that only reads JSON does not need to link. This package links
// the JSON codec, the core package declaring its codes, the standard
// library's encoding/json and internal/kernel/jsonenc (the standard library
// again), and nothing else.
//
// Importing both packages is harmless: a format is registered by the package
// that implements it, which Go initialises once however many packages import
// it.
//
// # Appenders
//
// For a program that writes JSON by hand, the package re-exports the
// appenders the SDK's own emitters use (ADR 0186 §D10):
//
//	out := json.BeginObject(buf[:0]).
//		String("level", "info").
//		Int("n", 7).
//		End()
//	stringOut := json.AppendString(nil, "a \"quoted\" word")
//
// A string is escaped as encoding/json escapes it — U+2028 and U+2029
// included, an invalid UTF-8 byte written as U+FFFD — with one difference:
// '<', '>' and '&' are NOT escaped, so the literal is not safe to embed in
// HTML as it is. The appenders register no codec and read no value by
// reflection, and Marshal is unchanged.
//
// # Errors
//
// Failures carry the range 0.3.2.*; match them with errs.HasCode, or the
// sentinels with errors.Is:
//
//	CodeMarshalFailed    0.3.2.1  encoding/json could not encode the value
//	CodeUnmarshalFailed  0.3.2.2  the input is not JSON the target can hold
package json
