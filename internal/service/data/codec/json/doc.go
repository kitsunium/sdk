// Package json wraps encoding/json as a codec.Codec implementation
// registered under Format("json"). Blank-importing this package is enough
// to make JSON resolvable via the core/data/codec registry.
//
// The registered codec encodes and decodes with encoding/json/v2 under
// encoding/json's v1 options — the engine and the options Go 1.27 builds
// encoding/json with, named so the codec states the semantics it keeps: its
// bytes are encoding/json's, pinned by golden files. NewV2 returns an
// unregistered codec under json/v2's own semantics, for a wire contract that
// starts with them.
//
// It also re-exports the JSON appenders of internal/kernel/jsonenc —
// [AppendString], [AppendEscaped], [AppendInt], [AppendUint], [AppendFloat],
// [BeginObject] and the [Object] it returns — for the programs that write a
// JSON document by hand and want the escaping the SDK's own emitters use
// (ADR 0186 §D10).
// They register no codec and read no value by reflection; Marshal keeps
// encoding/json's semantics. A string is escaped as encoding/json escapes
// it, except that '<', '>' and '&' are NOT.
//
// Package json — adapts *encoding/json.Decoder to codec.Decoder.
//
// Package json — adapts *encoding/json.Encoder to codec.Encoder.
package json
