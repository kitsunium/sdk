// Package json wraps encoding/json as a codec.Codec implementation
// registered under Format("json"). Blank-importing this package is enough
// to make JSON resolvable via the core/data/codec registry.
//
// It also re-exports the JSON appenders of internal/kernel/jsonenc —
// [AppendString], [AppendEscaped], [AppendInt], [AppendUint], [BeginObject]
// and the [Object] it returns — for the programs that write a JSON document
// by hand and want the escaping the SDK's own emitters use (ADR 0186 §D10).
// They register no codec and read no value by reflection; Marshal is still
// encoding/json, and does not change. A string is escaped as encoding/json
// escapes it, except that '<', '>' and '&' are NOT.
//
// Package json — adapts *encoding/json.Decoder to codec.Decoder.
//
// Package json — adapts *encoding/json.Encoder to codec.Encoder.
package json
