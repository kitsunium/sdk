<!-- updated: 2026-11-03T22:00:00Z -->
# internal/service/data/codec/json/

## Purpose

JSON codec over `encoding/json/v2` under `encoding/json`'s v1 options (#893) — the engine and options Go 1.27 builds `encoding/json` with, so its bytes are `encoding/json`'s, pinned by golden files. Reference implementation for the registry: stateless, streaming, and Appender-capable. `NewV2()` is an unregistered sibling under json/v2's own semantics.

## Surface

| Item | Value |
|---|---|
| `Name()`         | `"json"` |
| `MIMETypes()`    | `application/json`, `text/json` |
| `Extensions()`   | `.json` |
| Constructors     | `New() codec.Codec` (the registered singleton, v1 semantics) · `NewV2() codec.Codec` (fresh, **not** registered: json/v2's defaults plus `Deterministic(true)`) |
| Streaming        | yes (`NewEncoder`, `NewDecoder`); bounded (`NewBoundedDecoder(r, maxBytes)`, `core/data/codec.BoundedStreamingCodec`, #891) |
| Appender         | yes (`Append(dst, v) ([]byte, error)`) |

## Error codes (range `0.3.2.*`)

Declared in `internal/core/data/codec/json` — `codes.go` and `errors.go` — and used
here as `corejson.<Var>` (ADR 0160 §2: a code lives in the core at the path that
mirrors the package emitting it). The values, reasons and texts are the ones this
package always emitted; only the declaration moved.

| Code         | Var               | Trigger |
|---|---|---|
| `0.3.2.1`    | `MarshalFailed`   | `encoding/json.Marshal` returned an error |
| `0.3.2.2`    | `UnmarshalFailed` | `encoding/json.Unmarshal` returned an error |
| `0.3.2.3`    | `SizeExceeded`    | `NewBoundedDecoder`'s decoder: a value longer than the ceiling — reason `SIZE_EXCEEDED`, `UNMARSHAL_FAILED` in the trail (#891, #894) |

**Position (#1125).** A decoding refusal carries the byte `offset` `encoding/json` gives — `SyntaxError.Offset` and `UnmarshalTypeError.Offset`, which json/v2 returns under the v1 options, or json/v2's own `ByteOffset` (`position.go`) —, the bytes read when the decoder stopped: `codec.PositionOf` reads it back.

## Conventions

- **`Sniff` (`sniff.go`, #909)** implements `core/data/codec.Sniffer`, which `codec.Detect` asks about an input's first 512 bytes: an object or an array whose bytes so far are JSON's, cut inside it or followed by whitespace only — a second value is ndjson's, a top-level scalar too common a head to claim. It reads only the prefix and allocates nothing.
- Stateless singleton.
- `Append` shares `Marshal`'s `json.RawMessage` fast-path, appended in place without Marshal's clone; any other value is written straight onto `dst` by json/v2's `MarshalWrite` under the v1 options through a pooled `scratch.AppendWriter` (#1124) — `Marshal`'s bytes, no newline to drop, no intermediate buffer. On failure the original `dst` is returned untouched and the wrapped error surfaces with `CodeJSONMarshalFailed`.
- Streaming encoder/decoder wrap the stdlib types only to bridge the `core/data/codec.Encoder.Close` and `core/data/codec.Decoder.More` shape. `encoding/json`'s `Encoder` and `Decoder` are json/v2's `MarshalEncode` / `UnmarshalDecode` under the v1 options in Go 1.27, with the v1 stream semantics (`More`, a newline per value); they are kept as they are.
- **`Marshal` / `Unmarshal` / `Append` name json/v2** (#893): `jsonv2.Marshal(v, stdjson.DefaultOptionsV1())`, `jsonv2.Unmarshal(…, stdjson.DefaultOptionsV1())`, `jsonv2.MarshalWrite` onto an `AppendWriter`. The bytes do not change — `golden_external_test.go` holds every value of `goldenValues` (and what `Unmarshal` reads from `goldenDecodes`) to `testdata/golden`, written by the base's codec on encoding/json v1. Regenerating them (`-update-golden`) is never how a failing gate is fixed.
- **One divergence observed, older than this codec's move**: with `GOEXPERIMENT=nojsonv2` (the legacy engine), invalid UTF-8 in a string is written as the six-character escape `\ufffd`; the Go 1.27 default engine, under the same v1 options, writes the U+FFFD character itself. The goldens hold what the codec wrote on the base — the 1.27 engine's spelling; the two read back the same. No other byte differed across the golden set.
- **`time.Duration` is the integer of its nanoseconds** (#1126), as `encoding/json` writes it: the registered codec's contract is the standard library's output, so it does not take the text formats' `String()` rule. Every format promoted through JSON (csv, form, pem, tlv, flatbuffers, ndjson, baseenc) inherits the integer.
- **`NewV2()`** (`v2.go`): json/v2's semantics for a wire contract that starts with them — case-sensitive member names, duplicate names and invalid UTF-8 refused, nil slices and maps as `[]` / `{}`, byte arrays as base64, `omitempty` meaning an empty JSON value, `omitzero`, a `time.Duration` refused without a `format` — plus `Deterministic(true)`, so map members are sorted as the registered codec sorts them. Appender and streaming (`jsontext.Encoder` / `Decoder`: a newline per value, `io.EOF` unwrapped at the end). Not registered, like `csv.NewWithEscape`; its errors carry this package's codes. `v2_external_test.go` pins each difference against the registered codec.
- **The bounded stream decoder** (`decoder.go`, #891): `NewBoundedDecoder(r, maxBytes)` puts a `window` reader between `r` and the stdlib decoder. Before each `Decode` the window opens at the decoder's `InputOffset` — the end of the previous value — and hands over at most `maxBytes+1` bytes past it, so `encoding/json` never buffers more than that of a hostile value, however much the stream holds; a value that needed a read past the window, or that ends exactly `maxBytes+1` bytes in, is refused with `SizeExceeded`, latched — the stream cannot be resynchronised — and `More` reports false. The refusal carries the family's `offset` (#1125): the stream's byte `end-1`, the first past the ceiling — `offsetField` places nothing past what an int holds, a 32-bit platform's long stream. `Decode` calls `core/data/codec.CheckTarget` before the window opens, so a nil or non-pointer target is `TARGET_INVALID` and reads nothing (#1101). The one extra byte lets a top-level number of exactly `maxBytes` bytes see the byte that ends it. A stream of small values, however long in all, is never refused. A ceiling of zero or below is refused with the core's `BOUND_INVALID` (ADR 0031). `NewDecoder`'s decoder is unchanged: it has no ceiling. `decoder_bounded_external_test.go` pins the edges: exactly `maxBytes`, `maxBytes+1`, two hundred small values, a 1 MiB one read to at most `maxBytes+1` bytes.

## Performance (lib-bound)

`encoding/json`'s reflect walk and its per-type field cache are internal to the
stdlib — unreachable from this layer. The realised wins are the
`json.RawMessage` pass-through fast-path (Marshal + Append skip the reflect
round-trip when the bytes already exist) and Append writing straight onto
`dst` through a pooled `scratch.AppendWriter` (#1124). Naming json/v2 (#893)
moved no allocation — `encoding/json` already is json/v2 under the v1 options
in Go 1.27 — and `BENCH.md` has the table. `codec.Unmarshal(codec.JSON, …)`
keeps its meaning; a caller wanting json/v2's takes `NewV2()`. Do not re-add a
local pool.

## Verification

```
bazel test --config=race //internal/service/data/codec/json:json_test
```

## Fuzz (#897)

`fuzz_external_test.go` — `FuzzUnmarshal`: any input decodes without a panic into an untyped and a typed target; the untyped value reads back equal, the typed one's second encode is the bytes of the first. The seed corpus runs with the package's tests; `scripts/ci/codec-fuzz.sh` (nightly, `codec-fuzz.yml`) explores it.
