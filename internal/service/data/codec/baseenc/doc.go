// Package baseenc — Base45 (RFC 9285) alphabet codec. Unlike base16/32/64
// and ascii85, the stdlib ships no base45 encoder, so the encode/decode
// transforms live here. Base45 is a block encoding (base45PairBytes input
// bytes → base45GroupChars output chars; a trailing byte → base45TailChars
// chars), hence O(n) — safe to share the 10 MiB maxBaseEncBytes cap with
// the stdlib-backed variants.
//
// Package baseenc — Base58 (Bitcoin alphabet) base-conversion codec.
//
// Package baseenc — Base62 (0-9A-Za-z) base-conversion codec.
//
// Package baseenc — big-endian base-conversion arithmetic shared by the
// Base58 and Base62 variants. Unlike the block encodings, these treat the
// whole input as one integer and divide it down by the radix, so the
// transforms are O(n²) — callers MUST cap input at maxConvBytes first.
//
// Package baseenc — buffered writer used by variants whose stdlib has no
// streaming encoder (base16 uppercase). Held in its own file so the
// KTN-STRUCT-ONEFILE rule sees exactly one exported-shape struct per file.
//
// Package baseenc implements a family of codec.Codec wrappers around the
// stdlib byte encodings (base16 / base32 / base64 / ascii85). Each variant
// registers itself with the core/data/codec registry under a distinct Name —
// the variant discriminator is the registered Format, not a tagged enum.
//
// Universal Marshal flow: encode v with encoding/json, then base-N encode
// the resulting bytes. Unmarshal reverses the pipeline. The JSON layer is
// intentional — base-N alphabets are structureless, so flattening through
// JSON gives consumers a single Marshal/Unmarshal contract over any value.
//
// Blank-importing this package registers all six variants at process start
// via package-level var initialisers; no init() function involved.
//
// Package baseenc — buffered decode reader for variants whose stdlib has
// no streaming decoder (Base45). Held in its own file so KTN-STRUCT-ONEFILE
// sees exactly one struct per file.
//
// Package baseenc — adapts the base-N stream-reader pipeline to
// codec.Decoder. Each Decode call drives a stdlib json.Decoder over the
// base-N decoded byte stream wrapped around the caller's io.Reader.
//
// Package baseenc — adapts the base-N stream-writer pipeline to
// codec.Encoder. Each Encode call serialises one JSON value into the
// underlying base-N WriteCloser, which writes the encoded bytes onto the
// caller's io.Writer.
//
// Package baseenc — IO adapters used by the streaming pipeline.
//
// Package baseenc — per-variant identity table.
package baseenc
