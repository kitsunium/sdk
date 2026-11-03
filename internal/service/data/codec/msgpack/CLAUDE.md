<!-- updated: 2026-11-03T22:00:00Z -->
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

Declared in `internal/core/data/codec/msgpack` — `codes.go` and `errors.go` — and used
here as `coremsgpack.<Var>` (ADR 0160 §2: a code lives in the core at the path that
mirrors the package emitting it). The values, reasons and texts are the ones this
package always emitted; only the declaration moved.

| Code         | Var               | Trigger |
|---|---|---|
| `0.3.7.1`    | `MarshalFailed`   | any encode failure: unsupported kind (chan, func, complex, unsafe.Pointer), a length over 2³²−1, nesting past `maxDepth`, a marshal method's error, `MarshalMsgpack` bytes that are not exactly one value, a struct with two fields on one key, a writer error |
| `0.3.7.2`    | `UnmarshalFailed` | any decode failure: malformed or truncated input, a declared length the input cannot hold, trailing bytes, a value that does not fit its target |
| `0.3.7.5`    | `SizeExceeded`    | input over `maxMsgPackBytes` (10 MiB) — reason `SIZE_EXCEEDED`, `UNMARSHAL_FAILED` in the trail (#894) |
| `0.3.7.6`    | `DepthExceeded`   | nesting past `maxDepth` — reason `DEPTH_EXCEEDED`, `UNMARSHAL_FAILED` in the trail (#894) |

Two codes on purpose: callers route on the reason, and a new reason per cause
would have changed what `errs.HasReason(err, "UNMARSHAL_FAILED")` answers. The
cause is in `Private` (log-only) and in `Fields` — `offset`, `type`, `wire`,
`len`, `limit`, `field` — and never in a quote of the input. The over-cap
refusal keeps its exact pre-native shape (public "MessagePack input exceeds
size limit", fields `len`/`cap`).

**Position (#1125).** A decode refusal carries its byte `offset` — `fieldOffset` is `core/data/codec.FieldOffset` —, an offset of zero meaning the first byte: `codec.PositionOf` reads it back.

## Wire choices (pinned by the golden file)

- Integers are written in the SHORTEST form whatever their Go type: a
  non-negative value is a positive fixint or uint 8/16/32/64, a negative one a
  negative fixint or int 8/16/32/64 (the vendor's `UseCompactInts`). Floats keep
  their width: float32 → float 32, float64 → float 64.
- str uses fixstr / str 8 / str 16 / str 32; bin 8/16/32; fixarray / array 16 /
  32; fixmap / map 16 / 32. A nil slice, map or pointer is nil; an empty one is
  an empty container.
- `Ext{Type, Data}` (#903, `ext.go`) is an application extension written in its
  shortest form: fixext 1/2/4/8/16 for those payload sizes, else ext 8/16/32. The
  vendor this codec replaced exposed `RawExt`; until `Ext` a document carrying an
  application extension (Redis/Fluentd/Pulsar record types, UUIDs, decimals of
  other stacks) failed as a whole on decode into `any`. The method pair
  `MarshalMsgpackExt` / `UnmarshalMsgpackExt` the issue also sketches is not
  implemented: a type that writes its own extension implements `MarshalMsgpack`
  and returns the `Ext` bytes, or holds an `Ext` field.
- `time.Time` is the timestamp extension (type −1) in its shortest form: 32-bit
  seconds, 64-bit `nsec<<34|sec`, else 96-bit — every instant before 1970 is
  96-bit. `encoding.BinaryMarshaler` AND `encoding.TextMarshaler` are written as
  bin (the vendor's choice, kept so a reader in another language sees no type
  change). A field of static type `error` is its message.
- `time.Duration` is an integer of **int64 nanoseconds** (#1126), the rule of
  every binary format — the text formats (yaml, toml) write its `String()`;
  `pkg/v1/data/codec`'s `TestDurationAgreesAcrossFormats` pins both.
- Map pairs are written in **key order** (#1104), so equal values encode to
  equal bytes — what a content hash, a dedup key or a golden file needs: string
  keys byte-wise (encoding/json's order), integer keys by value whatever width
  each is written in, any other key — or two keys of different kinds — by its
  encoded bytes, and two keys that encode alike (an `int8(1)` and an `int64(1)`
  in one `map[any]any`, two NaN keys) by their values. The direct
  `map[string]V` paths sort the keys before writing (on the stack up to sixteen,
  one reserved slice beyond); the reflective path writes the pairs, then
  reorders them in place through a pooled scratch buffer (`reorderPairs`),
  comparing two string keys by an 8-byte prefix first. Cost: `BENCH.md`
  §Map order. `TestMarshalWritesMapKeysInOrder` pins the bytes.

## Decode rules

| Target | Accepts | Notes |
|---|---|---|
| any | everything | fixint/int8 → `int8`, int16/32/64 → `int16/32/64`, uint 8–64 → `uint8…uint64`, float32/64, `string`, `[]byte` (copy), `[]any`, `map[string]any` (keys must be str/bin), `time.Time`, `Ext` for any extension but the timestamp (#903) |
| `Ext` | any extension, nil | the type and a copy of the payload — the timestamp (−1) included, raw; any other family is refused |
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

`msgpack:"name,omitempty"`, `msgpack:"name,omitzero"` (#1103: encoding/json's
rule — the type's `IsZero()` when it declares one, through the field's address
when only the pointer does, else `reflect.Value.IsZero`; decided once per field
in `omitzero.go`; with `omitempty` too, either one leaves the field out), `"-"`,
`alias:other` (decode only), `inline` /
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
| `fault.go` | failure constructors; the two codes and sentinels are `internal/core/data/codec/msgpack`'s (ADR 0160) |
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

## Strict instance (#1106, `strict.go`)

`NewStrict()` returns a fresh, unregistered codec — the `csv.NewWithEscape`
shape — reading a document somebody else wrote one way or not at all (ADR
0102, amended). Before anything is decoded, a walk over the validated bytes
refuses a map repeating a key with `DUPLICATE_KEY`
(`0.3.7.3`), two keys counting as one when they decode to the same Go key whatever form each was written in (a str and a bin, an integer in two widths); the
decode then refuses a member no field of the target struct claims with
`UNKNOWN_MEMBER` (`0.3.7.4`). Up to 32 keys
of one map are compared in place, on the stack; past that an indexed set
takes over. The stream `Decoder` of the strict instance applies both to
every item. The registered codec is unchanged: last-wins, unknown members
skipped. `TestNewStrict` holds each refusal and the exact match.
