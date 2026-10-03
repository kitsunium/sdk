<!-- updated: 2026-10-03T00:00:00Z -->
# internal/service/data/codec/msgpack/

## Purpose

MessagePack codec — a NATIVE implementation of the specification
(github.com/msgpack/msgpack/blob/master/spec.md) on the standard library alone.
It replaced `github.com/vmihailenco/msgpack/v5` (decision D6: the public module
becomes stdlib-only) and writes the bytes that library wrote:
`testdata/vendor-golden.txt` holds the vendor's encoding of 106 values covering
every family, and `TestWireGolden` holds the native encoder to it byte for byte,
reads every line back into the value's own type, and checks an untyped decode
yields the same Go types the vendor yielded. The file is never regenerated from
the native codec — that would make it prove nothing.

## Surface

| Item | Value |
|---|---|
| `Name()`         | `"msgpack"` |
| `MIMETypes()`    | `application/msgpack`, `application/x-msgpack` |
| `Extensions()`   | `.msgpack`, `.mpk` |
| Constructor      | `New() codec.Codec` (the registered singleton `Codec`) |
| Streaming        | yes (`NewEncoder`, `NewDecoder`) |
| Appender         | yes — encodes straight onto `dst`, no intermediate buffer, no copy |

## Error codes (range `0.3.7.*`)

| Code         | Var               | Trigger |
|---|---|---|
| `0.3.7.1`    | `MarshalFailed`   | any encode failure: unsupported kind (chan, func, complex, unsafe.Pointer), a length over 2³²−1, nesting past `maxDepth`, a marshal method's error, `MarshalMsgpack` bytes that are not exactly one value, a struct with two fields on one key, a writer error |
| `0.3.7.2`    | `UnmarshalFailed` | any decode failure: malformed or truncated input, input over `maxMsgPackBytes` (10 MiB), nesting past `maxDepth`, a declared length the input cannot hold, trailing bytes, a value that does not fit its target |

Two codes on purpose: callers route on the reason, and a new reason per cause
would have changed what `errs.HasReason(err, "UNMARSHAL_FAILED")` answers. The
cause is in `Private` (log-only) and in `Fields` — `offset`, `type`, `wire`,
`len`, `limit`, `field` — and never in a quote of the input. The over-cap
refusal keeps its exact pre-native shape (public "MessagePack input exceeds
size limit", fields `len`/`cap`).

## Wire choices (pinned by the golden file)

- Integers are written in the SHORTEST form whatever their Go type: a
  non-negative value is a positive fixint or uint 8/16/32/64, a negative one a
  negative fixint or int 8/16/32/64 (the vendor's `UseCompactInts`). Floats keep
  their width: float32 → float 32, float64 → float 64.
- str uses fixstr / str 8 / str 16 / str 32; bin 8/16/32; fixarray / array 16 /
  32; fixmap / map 16 / 32. A nil slice, map or pointer is nil; an empty one is
  an empty container.
- `time.Time` is the timestamp extension (type −1) in its shortest form: 32-bit
  seconds, 64-bit `nsec<<34|sec`, else 96-bit — every instant before 1970 is
  96-bit. `encoding.BinaryMarshaler` AND `encoding.TextMarshaler` are written as
  bin (the vendor's choice, kept so a reader in another language sees no type
  change). A field of static type `error` is its message.
- Map pairs follow Go's map iteration order — not deterministic, as before.

## Decode rules

| Target | Accepts | Notes |
|---|---|---|
| any | everything but unknown extensions | fixint/int8 → `int8`, int16/32/64 → `int16/32/64`, uint 8–64 → `uint8…uint64`, float32/64, `string`, `[]byte` (copy), `[]any`, `map[string]any` (keys must be str/bin), `time.Time` |
| intN / uintN | any integer form | refused when the value does not fit (the vendor wrapped 300 into an int8 as 44); a negative into an unsigned is refused; nil → 0 |
| floatN | floats and integers | float32 refuses a value beyond its range; nil → 0 |
| string / []byte | str and bin | `[]byte` owns a copy; nil → "" / nil slice |
| slice | array | REPLACED: exactly the decoded elements, backing array reused when large enough |
| [N]T, [N]byte | array, bin/str | at most N; the rest is zeroed; nil zeroes |
| map | map | pairs ADDED, as in encoding/json; an unhashable key into `map[any]T` is refused (the vendor panicked) |
| struct | map (by key) and array (by position) | unknown keys skipped; nil zeroes |
| time.Time | timestamp, RFC 3339 str, nil | always UTC |
| interface | — | a held non-nil pointer is decoded INTO; `error` gets the string as an error; another non-empty interface is refused |

`Unmarshal` decodes exactly ONE value: trailing bytes are refused (the vendor
ignored them). `UnmarshalMsgpack` / `UnmarshalBinary` / `UnmarshalText` receive
a COPY of their bytes.

## Struct mapping (struct.go)

`msgpack:"name,omitempty"`, `"-"`, `alias:other` (decode only), `inline` /
`noinline` on an embedded struct, and the `_msgpack` marker field carrying
`as_array`/`asArray` and a struct-wide `omitempty` for later fields. No json-tag
fallback (the vendor's default had none either). An embedded struct inlines
unless a name collides; one that cannot inline is a field named after its type,
and an inlined one's name still decodes as a nested map. Two fields on one key
refuse the type (the vendor wrote the key twice). Unsupported vendor-only tag
options: `intern` (a non-standard dictionary extension) is ignored.

## Bounds (wire.go)

- `maxMsgPackBytes` = 10 MiB per `Unmarshal` input and per STREAM (the whole
  stream, as before — `NewDecoder` reads through an `io.LimitedReader` of one
  byte past the cap).
- `maxDepth` = 1000 nested containers on decode; on encode every pointer,
  interface and container counts, so a cycle fails instead of overflowing the
  stack and anything `Marshal` writes, `Unmarshal` reads back.
- No allocation is sized by a declared length before the input proved it holds
  that many bytes: a str/bin/ext length is checked against what remains, an
  array/map count against one/two bytes per element, and a slice or map reserves
  at most `preallocBytes` (64 KiB) before growing with the elements that
  actually decode. The stream framer refuses a length beyond what the stream
  can still deliver before reading it, and reads payloads in `frameChunk` pieces.
- A stream value the 4 KiB read-ahead holds whole is decoded in place (after a
  non-allocating `valueExtent` scan); any other value — longer, straddling the
  read-ahead, or malformed — is framed into a scratch buffer first, so both
  paths refuse alike. `FuzzUnmarshal` checks both against `Unmarshal`.

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
| `fault.go`, `failed.go` | failure constructors; the two codes and sentinels |
| `codec_compliance.go` | compile-time interface conformance |

## Verification

```
go test -race -count=1 ./internal/service/data/codec/msgpack/...
go test -count=1 -run TestAllocBudget ./internal/service/data/codec/msgpack/   # race-off alloc gate
go test -run='^$' -fuzz=FuzzUnmarshal -fuzztime=60s ./internal/service/data/codec/msgpack/
bazel test --config=race //internal/service/data/codec/msgpack:msgpack_test
```

## Do NOT

- Regenerate `testdata/vendor-golden.txt` from this codec, or edit it by hand.
- Size an allocation from a length the input declares without checking it
  against the bytes that remain.
- Add a `sync.Pool` here: buffers come from `core/data/codec/scratch`.
- Add a reason or code per failure cause — the two reasons are the contract.
