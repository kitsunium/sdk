<!-- updated: 2026-05-18T14:30:00Z -->
# internal/service/codec/csv/

## Purpose

CSV codec wrapping stdlib `encoding/csv`. Models the wire format as a `[][]string` matrix: `Marshal` accepts `[][]string` (or `*[][]string`), `Unmarshal` writes into `*[][]string`.

## Surface

| Item | Value |
|---|---|
| `Name()`         | `"csv"` |
| `MIMETypes()`    | `text/csv` |
| `Extensions()`   | `.csv` |
| Constructors     | `New() codec.Codec` (registered, `escapeFormulas=false`) · `NewWithEscape(escape bool) codec.Codec` (fresh, **not** registered) |
| Streaming        | not implemented |
| Appender         | yes (`Append(dst, v) ([]byte, error)`) — delegates to Marshal (single source for the type-gate + wrap contract) then appends onto dst |

## Error codes (range `0.3.8.*`)

| Code         | Var               | Trigger |
|---|---|---|
| `0.3.8.1`    | `MarshalFailed`   | `encoding/csv.Writer.WriteAll` returned an error |
| `0.3.8.2`    | `UnmarshalFailed` | `encoding/csv.Reader.ReadAll` returned an error |
| `0.3.8.3`    | `ValueInvalid`    | argument is not `[][]string` / target is not `*[][]string` |

## Conventions

- **OWASP CSV-Injection mitigation (CWE-1236)** is opt-in via `NewWithEscape(true)`. Cells whose first byte is one of `= + - @ \t \r` get prefixed with `'` so spreadsheet apps render them as text. Default `New()` preserves lossless wire-format fidelity.
- Mitigation operates on a defensive copy (`escapeFormulaCells` → `escapeFormulaRow` → `escapeIfFormulaCell`), keeping the caller's slice intact and per-row allocations attributable to the helper (not flagged as inner-loop allocation by ktn HOTLOOP).
- `NewWithEscape` returns a fresh instance — it is **not** added to the core registry; resolution-by-name returns the default singleton.

## Verification

```
bazel test --config=race //internal/service/codec/csv:csv_test
```
