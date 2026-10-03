<!-- updated: 2026-09-28T19:19:15Z -->
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
| Streaming        | not implemented (line-oriented format is consumed via a hand-rolled `'\n'`-indexed splitter using `bytes.IndexByte` — no `bufio.Scanner`) |
| Appender         | yes (`Append(dst, v) ([]byte, error)`) — slice-only, prior contents preserved on mid-record failure |

## Error codes (range `0.3.11.*`)

| Code         | Var               | Trigger |
|---|---|---|
| `0.3.11.1`   | `MarshalFailed`   | `encoding/json.Marshal` failed on a single record |
| `0.3.11.2`   | `UnmarshalFailed` | `encoding/json.Unmarshal` failed on a single record OR a line exceeded `scannerMaxCapacity` |
| `0.3.11.3`   | `ValueInvalid`    | argument is not a slice or an array (Marshal/Append) or target is not a pointer to a slice (Unmarshal) |

## Conventions

- **Slices and arrays only**: NDJSON models a stream of records, so `Marshal` / `Append` take a slice or an array (or a non-nil pointer to one) and `Unmarshal` a non-nil pointer to a slice; anything else is a programming error caught by the `asSlice` / `asSlicePointer` reflect helpers.
- **Line-length cap**: max single-line length 10 MiB (`scannerMaxCapacity`) enforced by the hand-rolled splitter — bigger records likely indicate a consumer bug or corrupt stream.
- **Append rollback**: on per-record failure `Append` returns `dst[:origLen]` so callers never see a torn buffer (Appender contract).
- Empty / whitespace-only lines are skipped silently to match the de-facto NDJSON dialect.
- **Per-record encoding passes the element's ADDRESS**, not a copy (`elemForMarshal`), whenever the element is addressable — i.e. always for a `[]T`, never for a bare `[N]T` array value, and deliberately never for an interface element. Two measured reasons:
  - *Parity.* `reflect.Value.Interface()` strips addressability, so a `MarshalJSON` / `MarshalText` declared on `*T` used to be skipped: ndjson emitted `{"v":1}` where `encoding/json.Marshal` of the whole `[]T` emits `"PTR-JSON"`. Pinned by `TestStdlibSliceParity`, which asserts line *i* equals element *i* of the stdlib's whole-slice encoding.
  - *Allocation.* Since Go 1.27 `encoding/json` is the json/v2 implementation (`GOEXPERIMENT=jsonv2` joined the baseline in `internal/buildcfg/exp.go`), and json/v2's `marshalEncode` shallow-copies every non-pointer argument through `reflect.New` (`encoding/json/v2/arshal.go`). Passing the address skips it. Per record: **3 → 1** allocation for `[]T`, `[]*T` was already at 1, `[]any` stays at 2 (an interface's dynamic value is not addressable, so that copy is irreducible). Pinned by `TestAllocPerRecord`.

## Verification

```
bazel test --config=race //internal/service/data/codec/ndjson:ndjson_test
```
