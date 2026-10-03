# internal/service/codec/bson/

## Purpose

BSON codec — a native implementation of the BSON 1.1 specification
(bsonspec.org) behind the universal `core/codec.Codec` dispatch, written with
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
every case except the deviations listed below, and all 718 valid cases of the
specification's corpus round-trip byte for byte while all 75 of its
decode-error cases are refused.

## Surface

| Aspect | Value |
|---|---|
| Format name | `"bson"` |
| MIME types | `application/bson` |
| Extensions | `.bson` |
| Streaming | **no** — a document is length-prefixed, so the writer must hold it whole |
| Appender | yes — the encoder writes straight into `dst`; a destination with room allocates nothing |
| Code range | `0.3.36.*` (ADR 0021) |
| Value types | `D`, `E`, `M`, `A`, `ObjectID` (+ `NilObjectID`, `ObjectIDFromHex`), `DateTime` (+ `NewDateTimeFromTime`), `Decimal128` (+ `NewDecimal128`, `ParseDecimal128`), `Binary` (+ the `Binary*` subtype constants), `Regex`, `Timestamp`, `DBPointer`, `JavaScript`, `Symbol`, `CodeWithScope`, `MinKey`, `MaxKey`, `Undefined`, `Null` |

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
codes.go, errors.go  the 0.3.36.* codes and sentinels, and the detail constructors
```

## Mapping

Encode — exact types first, then a type's own `MarshalBSON`, then its kind:

| Go | BSON |
|---|---|
| `bool` | boolean |
| `int8`, `int16`, `int32`, `uint8`, `uint16` | int32 |
| `int` | int32 when it fits, int64 otherwise |
| `int64` | int64; int32 under `minsize` when it fits |
| `uint`, `uint32`, `uint64` | int64; int32 under `minsize` when ≤ MaxInt32; past MaxInt64 refused |
| `float32`, `float64` | double |
| `string` | string (must be UTF-8) |
| `[]byte`, a `[]byte`-element slice, `[N]byte` | binary subtype 0x00; nil → null |
| other slices, arrays | array; nil slice → null |
| `D`, `[]E`, `[N]E` | document, in order; nil → null |
| maps (`string`, integer, `TextMarshaler` keys) | document, **keys sorted**; nil → null, except at the top level, where it is `{}` |
| structs | document of their fields, declaration order, `",inline"` flattened, `",inline"` map last |
| pointers, interfaces | their target; nil → null |
| `time.Time` | datetime, UTC milliseconds (truncated) |
| `url.URL` | string; `json.Number` → int64 when it parses as one, else double |
| the value types | their BSON type; `Regex` options written sorted |

Struct tags: `bson:"name,omitempty,minsize,truncate,inline"`, `bson:"-"` skips.
No tag → the field name lower-cased. A tag with no key and no colon (`` `name` ``)
is read as a bson tag. Every comma part, the first included, is matched as an
option. An embedded struct WITHOUT `",inline"` is a sub-document named after
its type. Two fields claiming one name: the shallower wins, equal depth refuses
the type. `omitempty`: `IsZero()` when the type has it, length 0, never a struct,
an interface only when nil.

Decode — numbers convert into any numeric target they fit (a double only when
whole, unless `truncate`), booleans and numbers into each other, null and
undefined into every zero value; a string target also takes a symbol, an
ObjectID (hex) and a generic binary; `[]byte` takes a generic binary, a string
or a symbol; `time.Time` takes a datetime, an int64 (ms), a timestamp (s) or a
`2006-01-02T15:04:05.999Z07:00` string. Fields match exactly, then by the
element name lower-cased. Unknown elements go to the `",inline"` map or are
skipped. A slice is decoded into its existing backing array when it has room; a
map is added to; a pointer is allocated, and a null sets it back to nil.

Interface targets: double → `float64`, int32 → `int32`, int64 → `int64`, string
→ `string`, boolean → `bool`, null → `nil`, array → `A`, datetime → `DateTime`,
binary → `Binary`, and each other BSON type to its value type. A document
becomes the **ancestor** type — the nearest enclosing `map[string]any`, `M` or
slice of `E` — or `D` when there is none: `*any` gives D all the way down,
`map[string]any` gives maps all the way down; a struct field resets it.

Hooks: `MarshalBSON() ([]byte, error)` (the bytes must be one well-formed
document) and `UnmarshalBSON([]byte) error` (a copy of the value's bytes; the
whole document at the top level). A pointer-receiver hook is reached only
through an addressable value, as the driver reached it.

## Hardening

- **Size cap**: 10 MiB (`maxBSONBytes`) on Unmarshal, checked first.
- **Validate, then decode**: `validateRoot` checks the whole input — every
  declared length against the bytes remaining, terminators, type bytes,
  booleans, subtype 0x02 inner lengths, code-with-scope part lengths, UTF-8 of
  strings, keys and regexes — so a malformed document is refused whole and the
  target is never half-written. The decoder still bounds-checks every slice.
- **Depth**: 100 levels, the top level counting as the first — refused with
  `BSON_DEPTH_EXCEEDED` on decode before recursing, and on encode, where it is
  also what a cyclic value runs into; a pointer chain that opens no container
  is bounded by `maxIndirections`.
- **No panic** on any input: `FuzzUnmarshal` (validity is target-independent;
  re-encoding is a fixed point).
- **Amplification** is bounded by the input cap, not removed: a typed slice
  decodes n × sizeof(T) for n elements of at least two bytes each.

## Differences from the MongoDB driver

Intentional, each refusing what the driver accepted or wrote silently:

- invalid UTF-8 in a string, key, regex or namespace is refused both ways (the
  specification's corpus requires it; the driver accepted it);
- a malformed element the target does not keep is refused (the driver skipped
  it unread);
- nesting past 100 levels is refused (the driver recursed until the stack
  ran out);
- map keys are written sorted (the driver wrote Go's random iteration order);
- a nil pointer whose type has `MarshalBSON` encodes as null (the driver called
  the method on nil); a double past the int64 range, NaN included, is refused
  for an integer target even under `truncate` (the driver's result was
  platform-dependent); NaN decodes into a `float32` (the driver refused it);
- a subtype 0x02 binary must carry a consistent inner length;
- not ported: `bsoncodec` registries and options, `Raw`/`RawValue`,
  `ValueMarshaler` and `Proxy` (their signatures name driver types), extended
  JSON, `KeyMarshaler`, ObjectID generation (an `id` scheme's job).

## Error codes (range `0.3.36.*`)

| Code | Var | Trigger |
|---|---|---|
| `0.3.36.1` | `MarshalFailed` | a value with no BSON form, a non-document top level, invalid UTF-8, a NUL in a key, a failing `MarshalBSON` |
| `0.3.36.2` | `UnmarshalFailed` | malformed input, a value the target cannot hold, a bad target |
| `0.3.36.3` | `SizeExceeded` | `len(data)` exceeds `maxBSONBytes` (10 MiB) |
| `0.3.36.4` | `DepthExceeded` | nesting past `maxBSONNestedLevels`, either direction |
| `0.3.36.5` | `ValueInvalid` | `ObjectIDFromHex`, `ParseDecimal128`, the value types' `Unmarshal*` methods |

`Private` names offsets, BSON types, Go types and declared field names — never
a key or a value read from the input.

## Do NOT

- Add an `init()` — registration is the package-level `var Codec = codec.Register(...)`.
- Use `fmt.Errorf`/`errors.New`; failures go through `marshalError`/`unmarshalError`/`valueError`.
- Decode before `validateRoot` has accepted the whole input, or slice by a length
  the validator has not compared with the bytes remaining.
- Put a value read from the input into an error message.
- Import a BSON library — the point of this package is that there is none.

## Verification

```
bazel test --config=race //internal/service/codec/bson:bson_test
go test -race ./internal/service/codec/bson/
go test -count=1 -run TestAllocBudget ./internal/service/codec/bson/   # race off: the alloc lane
go test -run '^$' -fuzz FuzzUnmarshal -fuzztime 60s ./internal/service/codec/bson/
```

Benchmarks and the comparison with the driver: `BENCH.md`.
