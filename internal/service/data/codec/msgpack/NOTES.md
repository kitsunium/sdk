# internal/service/data/codec/msgpack — notes

Reference moved out of `CLAUDE.md` (ADR 0253), unchanged and under the same section titles.

## Behaviour changes from the vendor-backed codec (consumer-visible)

1. Decoded times are UTC (were `time.Local`); the instant is unchanged.
2. Integer targets refuse values they cannot hold (were silently wrapped); a
   uint 64 above MaxInt64 decoded into a float64 is now positive (was wrapped
   negative).
3. Trailing bytes after the value are refused by `Unmarshal`.
4. A timestamp with more than 999 999 999 nanoseconds is refused (spec MUST).
5. Nesting is bounded at 1000; a declared length larger than the input is
   refused before any allocation. The vendor sized a slice from the declared
   count (its decode_slice.go computes `noLimit` as always true), so five bytes
   — `dd ff ff ff ff` — could ask for a 4-billion-element slice, and it
   recursed without a depth bound.
6. The streaming encoder writes Marshal's bytes (the vendor's stream writer,
   unlike its Marshal, wrote sized ints at full width) in ONE Write per value,
   and writes nothing for a value that fails.
7. A pointer-receiver marshal method is called on an addressable COPY of a
   non-addressable value (the vendor refused "non-addressable").
8. `float64` into a `float32` target is accepted within range (was refused); a
   float32 signalling NaN held in an interface keeps its bits.
9. Methods bound to vendor types — `EncodeMsgpack(*msgpack.Encoder)`,
   `DecodeMsgpack(*msgpack.Decoder)`, `msgpack.RawMessage`, `RegisterExt` — are
   gone with the vendor; the structural `MarshalMsgpack() ([]byte, error)` /
   `UnmarshalMsgpack([]byte) error` pair is still honoured.
10. Decoding nil into a Go array zeroes it (was left untouched); decoding into
    a slice replaces its elements (was merged into existing elements).

## Performance

See `BENCH.md`. Reflection runs once per Go type: `encoderFor` / `decoderFor`
cache a closure per type (a placeholder covers recursive types, as in
encoding/json); a struct appends each key pre-encoded; the common
`map[string]V` shapes (any, string, int, int64, float64, bool) and `[]any` are
ranged directly. `Marshal` encodes into a `core/data/codec/scratch` buffer and
returns one exact-size copy; `Append` encodes straight onto `dst`.

## Files

| File | Holds |
|---|---|
| `codec.go` | the Codec, Marshal / Unmarshal / Append / NewEncoder / NewDecoder |
| `wire.go` | format bytes, widths, bounds, the 256-entry header table |
| `encode.go`, `timestamp.go` | append primitives, the timestamp extension |
| `encode_plan.go`, `encode_map.go`, `encode_struct.go`, `encode_hook.go` | per-type encoders, map fast paths, struct + omitempty, marshal methods and the untyped entry |
| `decode.go`, `decode_any.go` | the bounded cursor, skip, the non-allocating extent scan, untyped decode |
| `decode_plan.go`, `decode_list.go`, `decode_map.go`, `decode_struct.go`, `decode_hook.go` | per-type decoders, slices/arrays, maps, structs, pointers/interfaces/unmarshal methods and the entry |
| `struct.go` | struct layout and tag parsing, shared by both directions |
| `encoder.go`, `decoder.go` | streaming encoder; streaming decoder — in place from the read-ahead, or framed |
| `fault.go` | failure constructors; the two codes and sentinels are `internal/core/data/codec/msgpack`'s (ADR 0160) |
| `codec_compliance.go` | compile-time interface conformance |
