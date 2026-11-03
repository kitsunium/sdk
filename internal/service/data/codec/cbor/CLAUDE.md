<!-- updated: 2026-11-03T22:00:00Z -->
# internal/service/data/codec/cbor/

## Purpose

The SDK's CBOR codec (RFC 8949), on the standard library alone — the native
replacement for the `github.com/fxamacker/cbor/v2` wrapper it used to be
(decision D6: the public module is standard-library-only). It keeps the
package path, the `Codec` singleton, the `"cbor"` registration, both error
codes, and the bytes fxamacker wrote with the options the codec used: every
golden vector in `encode_external_test.go` is fxamacker's output, byte for
byte, except that map pairs are now sorted.

## Surface

| Item | Value |
|---|---|
| `Name()`         | `"cbor"` |
| `MIMETypes()`    | `application/cbor` |
| `Extensions()`   | `.cbor` |
| Constructor      | `New() codec.Codec` (the registered singleton); `NewStrict()` and `NewDeterministic()`, fresh and unregistered |
| Value type       | `TagValue{Number uint64; Content any}` (`Tag` in `pkg/v1`) — an application's own tag (#896) |
| Streaming        | yes (`NewEncoder`, `NewDecoder`) — one data item per `Encode` / `Decode`; bounded (`NewBoundedDecoder(r, maxBytes)`, #891) on the registered, strict and deterministic instances |
| Appender         | yes — encodes straight into `dst`, no intermediate buffer; 0 allocations into a sized `dst`, `dst` returned at its original length on failure |

## Error codes (range `0.3.6.*`)

Declared in `internal/core/data/codec/cbor` — `codes.go` and `errors.go` — and used
here as `corecbor.<Var>` (ADR 0160 §2: a code lives in the core at the path that
mirrors the package emitting it). The values, reasons and texts are the ones this
package always emitted; only the declaration moved.

| Code      | Var               | Trigger |
|---|---|---|
| `0.3.6.1` | `MarshalFailed`   | a type CBOR cannot carry, a string that is not UTF-8, two map keys that encode alike, nesting past 32, a cycle, a failing writer / `MarshalBinary` / `MarshalCBOR` (a cause that is an SDK error keeps its own code: origin wins) |
| `0.3.6.2` | `UnmarshalFailed` | input that is not one well-formed, valid item; a bound exceeded; an item the target cannot hold; a failing reader / `UnmarshalBinary` / `UnmarshalCBOR` |
| `0.3.6.3` | `DuplicateKey`    | `NewStrict`: a map repeats a key |
| `0.3.6.4` | `UnknownMember`   | `NewStrict`: a member no field of the struct claims |
| `0.3.6.5` | `NotDeterministic` | `NewDeterministic`: a document not in RFC 8949 §4.2.1's core deterministic encoding |
| `0.3.6.6` | `SizeExceeded`    | an array, map or chunked string past its 1 048 576-entry bound, or (#891) a data item longer than `NewBoundedDecoder`'s ceiling — reason `SIZE_EXCEEDED`, `UNMARSHAL_FAILED` in the trail (#894) |
| `0.3.6.7` | `DepthExceeded`   | arrays, maps and tags nested past 32 levels — reason `DEPTH_EXCEEDED`, `UNMARSHAL_FAILED` in the trail (#894) |

The Private line says what was refused — a type, a bound — and a malformed
input carries an `offset` field; no message ever quotes a value read from the
input. The registered codec fails with the first two only; the three others
belong to the opt-in instances.

**Position (#1125).** A malformed input's refusal (`malformed`, `exceeded`, the strict and deterministic instances') carries its byte `offset` under `core/data/codec.FieldOffset`, an offset of zero meaning the first byte: `codec.PositionOf` reads it back.

## Contents

| File | What it holds |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `codec.go` | the `Codec` singleton, `Marshal` (pooled `scratch` buffer, one exact-size copy), `Unmarshal`, `Append`, the stream constructors |
| `errors.go` | the four failure constructors; the two codes and the two sentinels are `internal/core/data/codec/cbor`'s (ADR 0160) |
| `wire.go` | major types, additional information, simple values, tags 0–3, `appendHead`/`readHead`, the binary16 decoder |
| `validate.go` | the validator: one resumable, explicit-stack pass accepting exactly one well-formed and valid item within the bounds |
| `fields.go` | struct field resolution: `cbor` tag, `json` fallback, encoding/json embedding and dominance, options, `keyasint` canonicalisation, `toarray` |
| `encode.go` | `walkDepth`, the untyped fast path, scalar appenders |
| `encode_plan.go` | `kindEncoder`, `encodePlan` and its planner / cache |
| `encode_scalar.go`, `encode_special.go`, `encode_kinds.go` | the kind encoders (scalars; time, `MarshalCBOR`, `BinaryMarshaler`; pointers, interfaces, sequences, refusals) |
| `encode_struct.go` | structs as maps or arrays, omitempty, omitzero |
| `encode_map.go` | maps in deterministic order, `big.Int` |
| `decode.go` | the decode walk: state, primitives, skipping, sequences |
| `decode_any.go` | untyped decoding, no reflection |
| `decode_plan.go` | `decoder`, `decodePlan` and its planner / cache, pointers, interfaces, the self-decoding types |
| `decode_scalar.go`, `decode_container.go`, `decode_struct.go` | typed decoding by kind |
| `encoder.go`, `decoder.go` | the stream encoder and the stream decoder |
| `kinds_compliance.go` | compile-time assertions for every kind encoder and decoder |
| `strict.go` | `NewStrict` (#1106): the repeated-key walk and the unknown-member refusal |
| `tag.go` | `Tag` and its encoder and decoder; the `time=tag1` / `time=tag0` field options (#896) |
| `deterministic.go` | `NewDeterministic` (#896): the shortest float, and the walk refusing what core deterministic encoding forbids |

## What is written

- Integers, lengths and tag numbers in their **shortest head** (RFC 8949 §4.1).
- A `float64` as a double and a `float32` as a single — **no shortest-float**
  conversion, as before; NaN as `0xf97e00`, ±Inf as `0xf97c00` / `0xf9fc00`.
- A `time.Time` as its **integer Unix seconds, untagged** — the fraction of a
  second is dropped, as fxamacker's default `TimeUnix` dropped it — and the
  zero time as null. This is kept on purpose: changing it changes the bytes.
  A field tagged `cbor:",time=tag1"` is written as **tag 1**: integer seconds
  when it has no fraction, the shortest float when a double carries the
  instant to the nanosecond as `floatTime` reads it back, and otherwise **tag
  0** with its RFC 3339 text to the nanosecond — RFC 8949 §3.4.2 gives tag 1
  nothing finer than a double, and the instant is never rounded.
  `cbor:",time=tag0"` always writes tag 0. The option on a field that is not a
  `time.Time` refuses the struct.
- A `time.Duration` as an integer of **int64 nanoseconds** (#1126) — the rule
  of every binary format: no standard tag carries a duration, and the integer
  is what the `reflect.Int64` path writes. The text formats (yaml, toml) write
  its `String()`; `pkg/v1/data/codec`'s `TestDurationAgreesAcrossFormats`
  pins all three sides.
- A `Tag` as its head and its content, encoded like any value; a tag 0–3 whose
  content is not its RFC type is refused (`tagAdmits`, the validator's rule).
- A `big.Int` as an integer when it fits one, a bignum (tag 2 / 3) otherwise.
- `encoding.BinaryMarshaler` → byte string; a type with `MarshalCBOR()
  ([]byte, error)` writes its own item, which is checked to be exactly one
  valid item first. `encoding.TextMarshaler` is **not** consulted (as before).
- **Map pairs sorted** by the bytes of their encoded keys (RFC 8949 §4.2.1):
  equal values encode to equal bytes. Text keys sort shorter-first, then
  bytewise — which is the same order.
- Nothing the decoder would refuse: no invalid UTF-8, no two keys encoding
  alike, nothing nested past 32 (`walkDepth.enter`), no unbounded chain of
  pointers and interfaces (`maxIndirections`, a cycle), no pointer type that
  points to itself.

## What is read

- **Validation first** (`validate.go`): exactly one item, well-formed (no
  reserved additional information, no indefinite integer or tag, chunks of the
  string's own type and definite, no stray break, two-byte simple values ≥ 32)
  and valid (UTF-8 text **wherever it sits**, tags 0–3 enclosing their RFC
  types), within the bounds `maxCBORArrayElements`, `maxCBORMapPairs`,
  `maxCBORStringChunks` (1 << 20 each) and `maxCBORNestedLevels` (32 levels of
  arrays, maps **and tags**). A refused document never touches the target, and
  no count is acted on before it passed.
- Untyped (`any`): `uint64`, `int64` (or `big.Int` below `math.MinInt64`),
  `[]byte`, `string`, `[]any`, **`map[string]any` when every key is text,
  `map[any]any` otherwise**, `bool`, `nil`, `float64`, `time.Time` (tags 0/1),
  `big.Int` (tags 2/3); any other tag is transparent. A `Tag` target takes any
  tag, its content decoded as into `any`; an untagged item is a mismatch.
- Typed: struct keys match the field exactly, then case-insensitively; integer
  keys match `keyasint`; an unknown key is skipped; a repeated key keeps its
  first value; a map keeps its entries and the last of equal keys wins. An item
  the target cannot hold is recorded, skipped, and decoding goes on — the first
  failure is returned (encoding/json's and fxamacker's rule).
- `encoding.BinaryUnmarshaler` takes a byte string; a type with
  `UnmarshalCBOR([]byte) error` gets the whole item (aliasing the input: it
  must copy what it keeps).
- The stream decoder buffers one item at a time and validates each byte once
  however the item is split across reads (the validator resumes). For an
  untrusted peer, `NewBoundedDecoder(r, maxBytes)` (#891) bounds each item:
  its buffer never holds more than `maxBytes` bytes (reads are cut to the room
  left, growth capped), and an unfinished item that fills it is refused with
  `SizeExceeded` before another byte is read — so at most `maxBytes` bytes of
  a hostile item are ever read. The refusal is placed at `offset` `maxBytes`,
  the first byte past the ceiling within the item, as the stream's other
  refusals are placed (#1125); `Decode` refuses a nil or non-pointer target
  with `TARGET_INVALID` before reading (#1101), bounded or not. The validator knows where an item ends, so no
  look-ahead byte is needed, unlike json and xml. A ceiling of zero or below is
  the core's `BOUND_INVALID`. The unbounded path pays one `limit > 0` test per
  read: `StreamDecode` unchanged (`BENCH.md`, #891).

## Deviations from fxamacker/cbor (consumer-visible)

| Before | Now | Why |
|---|---|---|
| map pairs in Go's random order | sorted by encoded key | deterministic output; no decoder can tell |
| an untyped map was `map[interface{}]interface{}` | `map[string]any` when every key is text | the shape every other codec and `encoding/json` use; config's JSON bridge could not marshal the old one |
| an unrecognised tag in `any` was `cbor.Tag` | its content | no SDK type replaces `cbor.Tag`; tags were already transparent for typed targets |
| an unassigned simple value was `cbor.SimpleValue` (and filled an int) | refused | RFC 8949 §5.4 allows it; no Go value means it |
| a byte-string key in an untyped map was `cbor.ByteString` | refused | no hashable Go value distinguishes it from text |
| invalid UTF-8 / a wrong tag 0–3 content in a **skipped** value passed | refused | validity is checked over the whole item |
| a string with invalid UTF-8 was written | refused at `Marshal` | it wrote what its own decoder refused |
| two keys encoding alike were written | refused at `Marshal` | an invalid map |
| no nesting bound on encoding (stack overflow on a cycle) | 32, as decoding | writes nothing it cannot read back |
| null into a non-nil `any` left it | sets it to nil | as encoding/json |
| a tagged null into a pointer allocated the pointee | sets it to nil | tags are transparent |
| a reused slice kept stale element fields | elements zeroed first | no stale data |
| a chain of 33 tags passed (the first was not counted) | refused: each tag is a level | one rule for every level |

`Unmarshal`'s `UNMARSHAL_FAILED` and `Marshal`'s `MARSHAL_FAILED` reasons, the
codes, and the streaming contract (`io.EOF` unwrapped at a clean end, `More`
false after any failure) are unchanged.

## Performance

Plans are resolved once per `reflect.Type` (cached in a `sync.Map`, recursive
types handled by building in a private map and publishing complete graphs);
`map[string]any`, `[]any`, `map[string]string` and the common scalars bypass
reflection. `Marshal` allocates exactly its result; `Append` into a sized
buffer allocates nothing; the decode state and map holders are recycled through
`kernel/concur/recycler`.

Against fxamacker, measured side by side in CPU time (`BENCH.md` says how, and
why not wall time): at parity or faster on every benchmark but two, with
allocations down or equal everywhere — a thousand records encode in one
allocation instead of a thousand. The two exceptions are explained there:
`MarshalMap16` (1.55×) is the price of sorting, which fxamacker charges just
the same when asked to sort, and `UnmarshalSmall` (1.23×) is the validation
pass's fixed cost on a 19-byte document. `codec_bench_test.go` holds the
thirteen benchmarks; `BENCH.md` is regenerated by hand from them.

## Verification

```
bazel test --config=race //internal/service/data/codec/cbor:cbor_test
cd internal/service && GOWORK=off go test -race ./data/codec/cbor/
cd internal/service && GOWORK=off go test ./data/codec/cbor/        # the !race alloc budgets
cd internal/service && GOWORK=off go test -run='^$' -fuzz='^FuzzUnmarshal$' -fuzztime=60s ./data/codec/cbor/
cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem ./data/codec/cbor/   # the BENCH.md numbers
```

`FuzzUnmarshal` asserts that nothing panics, that what decodes untyped
re-encodes to a fixed point after one round, that the stream decoder agrees
with `Unmarshal`, and that a typed target fails only with `UNMARSHAL_FAILED`.

A coverage build does not walk the million-item documents of
`Test_validateItem_indefiniteBounds`: under the race detector, which
`bazel coverage` keeps on, every coverage counter is an instrumented atomic,
and the walks took 26 to 29 s each against the target's 60 s.
`Test_validator_countChild` pins the same three bounds there, at the constants
themselves; every other build walks them.

## Strict instance (#1106, `strict.go`)

`NewStrict()` returns a fresh, unregistered codec — the `csv.NewWithEscape`
shape — reading a document somebody else wrote one way or not at all (ADR
0102, amended). Before anything is decoded, a walk over the validated bytes
refuses a map repeating a key with `DUPLICATE_KEY`
(`0.3.6.3`), two keys counting as one when they decode to the same Go key whatever form each was written in (a text in one chunk and in two, an integer with two heads); the
decode then refuses a member no field of the target struct claims with
`UNKNOWN_MEMBER` (`0.3.6.4`). Up to 32 keys
of one map are compared in place, on the stack; past that an indexed set
takes over. The stream `Decoder` of the strict instance applies both to
every item. The registered codec is unchanged: last-wins, unknown members
skipped. `TestNewStrict` holds each refusal and the exact match.

## Deterministic instance (#896, `deterministic.go`)

`NewDeterministic()` returns a fresh, unregistered codec for bytes that are
signed or hashed — COSE (RFC 9052), CWT (RFC 8392). It writes what the
registered codec writes with one difference: every float, `float32` included,
in the shortest of half, single and double that holds it exactly (RFC 8949
§4.1, required by §4.2.1; `appendShortestFloat`, checked against all 65 536
halves). Keys were already sorted and heads already shortest, and nothing
indefinite is ever written. Its `Unmarshal` and stream `Decoder` validate as
the registered codec does, then walk the item once and refuse with
`NOT_DETERMINISTIC` (`0.3.6.5`, with the `offset`) a head longer than it
needs, a float in a longer form than holds it, a NaN other than `0xf97e00`,
an indefinite length, and map keys not strictly ascending in the bytewise
order of their encodings — a repeat included. Into `any` it keeps every tag
but a bignum as a `Tag`, so a document decodes and encodes back to its own
bytes: `TestNewDeterministic_appendixA` holds RFC 8949 Appendix A's float rows
and tags 0, 1, 23, 24 and 32 both ways. The registered codec's bytes, and its
transparent tags in `any`, are unchanged (`TestNewDeterministic_registeredUnchanged`).
`FuzzNewDeterministic` checks it refuses only with its two reasons and reads
back whatever it writes.
