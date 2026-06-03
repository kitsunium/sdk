<!-- updated: 2026-05-18T14:30:00Z -->
# internal/service/codec/ndjson/

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
| `0.3.11.3`   | `ValueInvalid`    | argument is not a slice (Marshal/Append) or target is not a pointer to a slice (Unmarshal) |

## Conventions

- **Slice-only**: NDJSON models a stream of records, so a non-slice argument is a programming error caught by the `asSlice` / `asSlicePointer` reflect helpers.
- **Line-length cap**: max single-line length 10 MiB (`scannerMaxCapacity`) enforced by the hand-rolled splitter — bigger records likely indicate a consumer bug or corrupt stream.
- **Append rollback**: on per-record failure `Append` returns `dst[:origLen]` so callers never see a torn buffer (Appender contract).
- Empty / whitespace-only lines are skipped silently to match the de-facto NDJSON dialect.

## Verification

```
bazel test --config=race //internal/service/codec/ndjson:ndjson_test
```

## Accepted audit findings

- Deferred/accepted low+info audit findings (V53) are recorded in `.claude/contexts/sdk-audit-2026-06-03-accepted.yaml` (2026-06-03 close-out). Each is a deliberate decision or deferred change, not an open bug.
