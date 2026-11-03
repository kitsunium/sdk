<!-- updated: 2026-11-03T22:00:00Z -->
# internal/service/data/codec/ndjson/

## Purpose

Newline-delimited JSON: one JSON value per line, `\n`-separated. `Marshal` emits one record per slice element; `Unmarshal` decodes each non-empty line into a fresh slice element.

## Surface

| Item | Value |
|---|---|
| `Name()`         | `"ndjson"` |
| `MIMETypes()`    | `application/x-ndjson`, `application/jsonl` |
| `Extensions()`   | `.ndjson`, `.jsonl` |
| Constructor      | `New() codec.Codec` |
| Streaming        | yes (#901) — `NewDecoder` hands one record per `Decode`, reading one line at a time through a `bufio.Reader` (`decoder.go`); `NewEncoder` writes one line per `Encode` (`encoder.go`). `Unmarshal` keeps its hand-rolled `'\n'`-indexed splitter over the whole input |
| Appender         | yes (`Append(dst, v) ([]byte, error)`) — slice-only, prior contents preserved on mid-record failure |

## Error codes (range `0.3.11.*`)

Declared in `internal/core/data/codec/ndjson` — `codes.go` and `errors.go` — and used
here as `corendjson.<Var>` (ADR 0160 §2: a code lives in the core at the path that
mirrors the package emitting it). The values, reasons and texts are the ones this
package always emitted; only the declaration moved.

| Code         | Var               | Trigger |
|---|---|---|
| `0.3.11.1`   | `MarshalFailed`   | `encoding/json` failed to encode a single record (`Encoder.Encode`) |
| `0.3.11.2`   | `UnmarshalFailed` | `encoding/json.Unmarshal` failed on a single record OR a line exceeded `scannerMaxCapacity` |
| `0.3.11.3`   | `ValueInvalid`    | argument is not a slice or an array (Marshal/Append) or target is not a pointer to a slice (Unmarshal) |
| `0.3.11.4`   | `SizeExceeded`    | a STREAM line longer than `scannerMaxCapacity` (10 MiB): the stream decoder refuses it once it has read that far, and the stream ends. `Unmarshal` still reports an oversized line as `UnmarshalFailed` — #894 unifies the size vocabulary across the codecs |

## Conventions

- **`Sniff` (`sniff.go`, #909)** implements `core/data/codec.Sniffer`, which `codec.Detect` asks about an input's first 512 bytes: a whole object or array, the end of its line, and a second record opening on the next — two records at least, so a single record stays json's and the two never claim the same bytes; the scanner duplicates json's, codecs importing no sibling. It reads only the prefix and allocates nothing.
- **Slices and arrays only**: NDJSON models a stream of records, so `Marshal` / `Append` take a slice or an array (or a non-nil pointer to one) and `Unmarshal` a non-nil pointer to a slice; anything else is a programming error caught by the `asSlice` / `asSlicePointer` reflect helpers.
- **Line-length cap**: max single-line length 10 MiB (`scannerMaxCapacity`) enforced by the hand-rolled splitter — bigger records likely indicate a consumer bug or corrupt stream.
- **Pre-encoded rows stay JSON** (`[]json.RawMessage`, `[][]byte`, and a non-nil `*[][]byte`): written as they are, **unvalidated**, when no row carries a `'\n'` — the caller vouches for them; otherwise they take the encoder path, `[][]byte` re-typed as `[]json.RawMessage` first (`asRawMessages`, zero-copy), so every row is validated and compacted onto one line — never written as a base64 string, which `encoding/json` does for a `[]byte` (#462). On that path only, a row that is not JSON is `MarshalFailed`.
- **Append rollback**: on per-record failure `Append` returns `dst[:origLen]` so callers never see a torn buffer (Appender contract).
- Empty / whitespace-only lines are skipped silently to match the de-facto NDJSON dialect.
- **The output is sized by records, not newlines** (#1098): `countNDJSONRecords` counts the lines `decodeLines` will decode — non-blank once `bytes.TrimSpace` has run — so a blank line reserves nothing, and at most `maxReservedRecords` (4 096) slots are reserved before the first record is decoded, the slice doubling past that (`growSlots`). 1 MiB of `'\n'` into ten-string records reserved 160 MiB before; a refusal at the first line of a huge input reserved a slot per line. `sizing_integration_test.go` pins both; `FuzzUnmarshalCountsRecords` holds the count exact on success.
- **Per-record encoding passes the element's ADDRESS**, not a copy (`elemForMarshal`), whenever the element is addressable — i.e. always for a `[]T`, never for a bare `[N]T` array value, and deliberately never for an interface element. Two measured reasons:
  - *Parity.* `reflect.Value.Interface()` strips addressability, so a `MarshalJSON` / `MarshalText` declared on `*T` used to be skipped: ndjson emitted `{"v":1}` where `encoding/json.Marshal` of the whole `[]T` emits `"PTR-JSON"`. Pinned by `TestStdlibSliceParity`, which asserts line *i* equals element *i* of the stdlib's whole-slice encoding.
  - *Allocation.* Since Go 1.27 `encoding/json` is the json/v2 implementation (`GOEXPERIMENT=jsonv2` joined the baseline in `internal/buildcfg/exp.go`), and json/v2's `marshalEncode` shallow-copies every non-pointer argument through `reflect.New` (`encoding/json/v2/arshal.go`). Passing the address skips it. Per record: **3 → 1** allocation for `[]T`, `[]*T` was already at 1, `[]any` stays at 2 (an interface's dynamic value is not addressable, so that copy is irreducible). Pinned by `TestAllocPerRecord`.
- **json/v2 under the v1 options, by name** (#893): each record is written by `jsonv2.MarshalWrite(buf, rec, stdjson.DefaultOptionsV1())` then `'\n'` (`encodeRecordTo`), and read by `jsonv2.Unmarshal(line, slot, stdjson.DefaultOptionsV1())` (`decodeRecord`) — the calls `encoding/json`'s `Encoder.Encode` and `Unmarshal` make in Go 1.27. `golden_external_test.go` holds `Marshal`, `Append` and a decode round trip to `testdata/golden`, written by the base's codec; the per-record allocations below are unchanged (`TestAllocPerRecord`).
- **One encoder per record, no slice per record** (`encodeSliceInto`, shared by `Marshal` and `Append`). `MarshalWrite` writes `Marshal`'s bytes — compact, HTML-escaped by default, the same marshalers — through json/v2's pooled encoder straight into the output buffer, then the `'\n'` a record needs. Before #893 it was one `json.Encoder` per call, whose `Encode` makes the same json/v2 call. A `json.Marshal` per record allocated a fresh slice per record only to copy it out. Per record: **1 → 0** allocation for `[]T` and `[]*T`, **2 → 1** for `[]any`; a 1 000-record `Append` leaves 74 B for the collector instead of 78 KiB (`BENCH.md`). Line parity with the stdlib's whole-slice encoding is still `TestStdlibSliceParity`'s, and `TestAllocBudget` / `TestAllocPerRecord` hold the new figures (3 per call, 0 per record).

- **The stream decoder** (#901) skips blank lines as `Unmarshal` does and decodes each line with the same call; `More` reads ahead to the next non-blank line, so a stream's last record is followed by `false`, and reports `true` for a failure `Decode` has not returned yet. `Decode` returns `io.EOF` unwrapped once drained. A line that does not fit the target fails its own `Decode` (`UnmarshalFailed`) and the next `Decode` reads the line after it — the line-delimited format resynchronises; a failing reader or an oversized line ends the stream. A line that fits the `bufio.Reader`'s buffer is decoded in place; a longer one is gathered, at most 10 MiB plus one buffer read. The encoder is one `json.Encoder` over the writer — `Marshal`'s line per record.

## Benchmarks

`codec_bench_test.go` — `MarshalBatch` and `AppendBatch` over 10 and 1 000 small records; `stream_bench_test.go` — `Unmarshal64` against `StreamDecode64`; numbers and machine in `BENCH.md`.

## Verification

```
bazel test --config=race //internal/service/data/codec/ndjson:ndjson_test
```
