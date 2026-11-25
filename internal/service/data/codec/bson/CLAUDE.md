# internal/service/data/codec/bson/

## Purpose

BSON codec — a native implementation of the BSON 1.1 specification
(bsonspec.org) behind the universal `core/data/codec.Codec` dispatch, written with
the standard library alone. Blank-importing this package self-registers the
`"bson"` Format (no `init()`). ADR 0021 added the codec over
`go.mongodb.org/mongo-driver/bson`; the driver is gone, so a program linking
this package links no MongoDB code, and the module graph of `pkg` no longer
carries the driver's own requirements (snappy, klauspost/compress, scram,
pkcs8, x/crypto, x/sync, x/text among them), which took part in every
consumer's version selection although only the driver's bson packages were
linked.

The mapping between Go values and BSON is the driver's v1 default registry's,
checked against it before it left the module graph: a differential test and
two differential fuzzers (encode and decode, ~10 M executions) agreed on
every case except the deviations listed below. The specification's own
corpus runs in the ordinary test lane (#1107, see Conformance below): all 731
valid cases round-trip byte for byte and all 75 decode-error cases are
refused.

## Surface

| Aspect | Value |
|---|---|
| Format name | `"bson"` |
| MIME types | `application/bson` |
| Extensions | `.bson` |
| Streaming | **yes** (#1100, `stream.go`) — one document per `Encode` (one `Write`, nothing written on a refusal) and per `Decode` (the int32 prefix read first, a length past `maxBSONBytes` refused with `SIZE_EXCEEDED` before anything is reserved, the body read in a reservation that grows from 64 KiB with what the reader delivers, then validated whole); the sticky-EOF latch of the other decoders. StreamEncode costs 0 allocations, StreamDecode at most 7.4 % more than UnmarshalTyped (`BENCH.md` §#1100). ADR 0021 §3, amended. |
| Appender | yes — the encoder writes straight into `dst`; a destination with room allocates nothing |
| Code range | `0.3.36.*` (ADR 0021) |
| Value types | `D`, `E`, `M`, `A`, `ObjectID` (+ `NilObjectID`, `ObjectIDFromHex`), `DateTime` (+ `NewDateTimeFromTime`), `Decimal128` (+ `NewDecimal128`, `ParseDecimal128`), `Binary` (+ the `Binary*` subtype constants), `Regex`, `Timestamp`, `DBPointer`, `JavaScript`, `Symbol`, `CodeWithScope`, `MinKey`, `MaxKey`, `Undefined`, `Null` |

`pkg/v1/data/codec/bson` aliases the value types; nothing else is public.

## Contents

```
codec.go           Codec singleton, Name/MIMETypes/Extensions, Marshal (pooled scratch
                   buffer, one exact-size copy out), Unmarshal (size cap, validate, decode), Append
wire.go            type bytes, Binary* subtypes, bounds (maxBSONNestedLevels = 100), value sizes
validate.go        the structural validator Unmarshal runs before touching the target
plan.go            per-reflect.Type plans (sync.Map, built under one mutex), struct tags,
                   ",inline" expansion, duplicate-name dominance, hook detection
encode*.go         dispatch, containers (map keys sorted), primitives, value types
decode*.go         dispatch and element walk, containers, scalars, value types, interface defaults
types.go           D/E/M/A, Binary, Regex, DBPointer, Timestamp, CodeWithScope, DateTime, markers
objectid.go        ObjectID and its hex / text / JSON forms
decimal.go         Decimal128: BID layout, String and ParseDecimal128 (exact or refused)
errors.go          the detail constructors; the 0.3.36.* codes and sentinels are
                   internal/core/data/codec/bson's (ADR 0160)
```

## Hardening

- **Size cap**: 10 MiB (`maxBSONBytes`) on Unmarshal, checked first.
- **Validate, then decode**: `validateRoot` checks the whole input — every
  declared length against the bytes remaining, terminators, type bytes,
  booleans, subtype 0x02 inner lengths, code-with-scope part lengths, UTF-8 of
  strings, keys and regexes — so a malformed document is refused whole and the
  target is never half-written. The decoder still bounds-checks every slice.
- **Depth**: 100 levels, the top level counting as the first — refused with
  `DEPTH_EXCEEDED` on decode before recursing, and on encode, where it is
  also what a cyclic value runs into; a pointer chain that opens no container
  is bounded by `maxIndirections`.
- **No panic** on any input: `FuzzUnmarshal` (validity is target-independent;
  re-encoding is a fixed point).
- **Amplification** is bounded by the input cap, not removed: a typed slice
  decodes n × sizeof(T) for n elements of at least two bytes each.

## Detection (#909, `sniff.go`)

`Sniff` implements `core/data/codec.Sniffer`, which `codec.Detect` asks about
an input's first 512 bytes: an int32 length between 5 and `maxBSONBytes`, the
closing NUL where it says when the document fits the prefix, a known first
element type and a NUL-terminated UTF-8 name without control bytes; the empty
document is its five bytes. It reads only the prefix and allocates nothing.
The facade's compressed frame can pass those checks on a flate payload, which
is why `codec.Detect` refuses the frame before asking any sniffer.

## Error codes (range `0.3.36.*`)

Declared in `internal/core/data/codec/bson` — `codes.go` and `errors.go` — and used
here as `corebson.<Var>` (ADR 0160 §2: a code lives in the core at the path that
mirrors the package emitting it). The values, reasons and texts are the ones this
package always emitted; only the declaration moved.

| Code | Var | Trigger |
|---|---|---|
| `0.3.36.1` | `MarshalFailed` | a value with no BSON form, a non-document top level, invalid UTF-8, a NUL in a key, a failing `MarshalBSON` |
| `0.3.36.2` | `UnmarshalFailed` | malformed input, a value the target cannot hold, a bad target |
| `0.3.36.3` | `SizeExceeded` | `len(data)` exceeds `maxBSONBytes` (10 MiB) |
| `0.3.36.4` | `DepthExceeded` | nesting past `maxBSONNestedLevels`, either direction |
| `0.3.36.5` | `ValueInvalid` | `ObjectIDFromHex`, `ParseDecimal128`, the value types' `Unmarshal*` methods |

`Private` names offsets, BSON types, Go types and declared field names — never
a key or a value read from the input.

**Position (#1125).** A malformed document's refusal carries the validator's byte `offset` as the family's field (`malformedAt`), no longer inside its Private sentence, which keeps only the rule broken; the strict instance's refusals carry theirs too. In a stream, the offset is in the document refused. `codec.PositionOf` reads it back; an offset of zero is the first byte.

## Do NOT

- Add an `init()` — registration is the package-level `var Codec = codec.Register(...)`.
- Use `fmt.Errorf`/`errors.New`; failures go through `marshalError`/`unmarshalError`/`valueError`.
- Decode before `validateRoot` has accepted the whole input, or slice by a length
  the validator has not compared with the bytes remaining.
- Put a value read from the input into an error message.
- Import a BSON library — the point of this package is that there is none.

## Verification

```
bazel test --config=race //internal/service/data/codec/bson:bson_test
go test -race ./internal/service/data/codec/bson/
go test -count=1 -run TestAllocBudget ./internal/service/data/codec/bson/   # race off: the alloc lane
go test -run '^$' -fuzz FuzzUnmarshal -fuzztime 60s ./internal/service/data/codec/bson/
```

Benchmarks and the comparison with the driver: `BENCH.md`.

## Conformance (#1107)

`testdata/bson-corpus/` is the MongoDB specifications' BSON corpus — the 31
per-type `*.json` files of `source/bson-corpus/tests/` at commit `775c4bce`,
copied byte for byte — under **CC BY-NC-SA 3.0 US**, not Apache-2.0 as the
issue assumed: `LICENSE` beside the files is the upstream licence and
`README.md` there the attribution (source, commit, licensor, licence, no
change made). The licence covers those files only; the owner accepted it for
`testdata/` (the SDK is not sold). `conformance_external_test.go` runs them
with no network: every `valid` case's `canonical_bson` decodes into a `D` and
encodes back to the same bytes, and its `degenerate_bson`, when it has one, to
the canonical bytes (4 cases); a decimal128 decodes to the `Decimal128` whose
`String` is the canonical extended JSON's `$numberDecimal` (unless `lossy`);
every `decodeErrors` document is refused with a `0.3.36.*` code; every
decimal128 `parseErrors` string is refused by `ParseDecimal128` (131). Groups
it sets aside, by name: the other `parseErrors` (49 — extended JSON, not
ported), the extended-JSON forms, and `converted_bson`. The relaxations map
is empty and must stay so unless this file names a case; the test asserts the
counts 731 / 75 / 131, so a refresh that loses a case fails. To refresh: copy
the same files from a newer commit, update `README.md` there and the counts.

## Strict instance (#1106, `strict.go`)

`NewStrict()` returns a fresh, unregistered codec — the `csv.NewWithEscape`
shape — reading a document somebody else wrote one way or not at all (ADR
0102, amended). Before anything is decoded, a walk over the validated bytes
refuses a BSON document, at any depth, repeating a key with `DUPLICATE_KEY`
(`0.3.36.6`); the
decode then refuses a member no field of the target struct claims with
`UNKNOWN_MEMBER` (`0.3.36.7`), unless an `,inline` map collects it. Up to 32 keys
of one map are compared in place, on the stack; past that an indexed set
takes over. The stream `Decoder` of the strict instance applies both to
every item. The registered codec is unchanged: last-wins, unknown members
skipped. `TestNewStrict` holds each refusal and the exact match.

## Reference in NOTES.md

Moved unchanged to `internal/service/data/codec/bson/NOTES.md`, under the same titles, and read only by a change that needs its mechanics: `Mapping`; `Differences from the MongoDB driver`.
