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

## Reference in NOTES.md

Moved unchanged to `internal/service/data/codec/cbor/NOTES.md`, under the same titles, and read only by a change that needs its mechanics: `What is written`; `What is read`; `Performance`.
