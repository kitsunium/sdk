// Package json wraps encoding/json as a codec.Codec implementation
// registered under Format("json"). Blank-importing this package is enough
// to make JSON resolvable via the core/data/codec registry.
//
// Package json — adapts *encoding/json.Decoder to codec.Decoder.
//
// Package json — adapts *encoding/json.Encoder to codec.Encoder.
package json
