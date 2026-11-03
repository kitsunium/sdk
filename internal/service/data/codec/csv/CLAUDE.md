<!-- updated: 2026-11-03T22:00:00Z -->
# internal/service/data/codec/csv/

## Purpose

CSV codec wrapping stdlib `encoding/csv`. Models the wire format as a `[][]string` matrix: `Marshal` accepts `[][]string` (or `*[][]string`), `Unmarshal` writes into `*[][]string`. A slice of structs is a table too (#902): a header from the fields' `csv` tags, one record per row, each cell converted to its field.

## Surface

| Item | Value |
|---|---|
| `Name()`         | `"csv"` |
| `MIMETypes()`    | `text/csv` |
| `Extensions()`   | `.csv` |
| Constructors     | `New() codec.Codec` (registered, `escapeFormulas=false`) · `NewWithEscape(escape bool) codec.Codec` (fresh, **not** registered) · `NewWithOptions(Options) (codec.Codec, error)` (fresh, **not** registered: `Escape`, `Comma`, `NoHeader`; an invalid delimiter is `OptionsInvalid`) — none surfaced in `pkg/v1`, like `multipart.NewWithLimits` |
| Streaming        | yes (#902) — `NewDecoder` hands one record per `Decode`, into a `*[]string` as it is or into a pointer to a struct through the header (`decoder.go`); `NewEncoder` writes one record per `Encode`, a `[]string` or a struct, the header before the first struct (`encoder.go`) |
| Appender         | yes (`Append(dst, v) ([]byte, error)`) — the records are written into `dst` itself, grown once to the exact size when it lacks the room; `dst` returned as given on a refusal (#1124) |

## Error codes (range `0.3.8.*`)

Declared in `internal/core/data/codec/csv` — `codes.go` and `errors.go` — and used
here as `corecsv.<Var>` (ADR 0160 §2: a code lives in the core at the path that
mirrors the package emitting it). The values, reasons and texts are the ones this
package always emitted; only the declaration moved.

| Code         | Var               | Trigger |
|---|---|---|
| `0.3.8.1`    | `MarshalFailed`   | not emitted by `Marshal` / `Append` since #1124: the package's own encoder writes every `[][]string` (`encoding/csv`'s only refusal was an invalid delimiter) |
| `0.3.8.2`    | `UnmarshalFailed` | `encoding/csv.Reader.Read` returned an error (`readAllPreSized` reads record by record) |
| `0.3.8.3`    | `ValueInvalid`    | argument is neither `[][]string` nor a slice of structs the typed mapping can write; target is neither `*[][]string` nor a pointer to such a slice; a stream target is neither `*[]string` nor a pointer to a struct. Also a table that is exactly the facade's promotion container read into a struct mapping no `_json` column — so `pkg/v1/data/codec` unwraps it as it wrote it |
| `0.3.8.4`    | `HeaderMismatch`  | the header lacks a column the struct requires, or repeats one it maps — the fields name the column as the struct names it, under `name` |
| `0.3.8.5`    | `CellInvalid`     | a cell does not convert to its field's type — the fields name the record and the column (`name`) and place the cell (`line`, `column`), never quote it |
| `0.3.8.6`    | `OptionsInvalid`  | `NewWithOptions` was given a delimiter encoding/csv cannot use |

**Position (#1125).** A parse error carries the 1-based `line` and byte `column` `encoding/csv`'s `ParseError` gives (`parsePosition`), and `CELL_INVALID` the cell's, from the reader's `FieldPos`, beside its `record` number: `codec.PositionOf` reads them back. `CELL_INVALID` and `HEADER_MISMATCH` name the column under `name` — `column` is a coordinate's key in every codec.

## Conventions

- **Typed tables (#902)** — `schema.go`, `typed.go`. A struct's columns are its exported fields in declaration order: the `csv:"name"` tag, else the Go name; `csv:"-"` skips a field; `,optional` lets a file lack the column (every other mapped column is required: `HeaderMismatch`); an exported embedded struct lends its fields in its place (an unexported one is skipped). A cell converts to a string, a signed or unsigned integer (base 10, the field's width), a float (`'g'`, shortest), a bool (`strconv.ParseBool`), or any type that reads and writes itself as text — `encoding.TextUnmarshaler` / `TextMarshaler`, `time.Time` in RFC 3339 among them, checked before the kind so a named string with its own `MarshalText` writes through it. A pointer field writes `nil` as an empty cell and reads an empty cell as `nil`; an empty cell for a number or a bool is `CellInvalid`. A `time.Duration` is an `int64`: nanoseconds (#1126 owns the family's rule). A field type no cell holds (a map, a slice, a struct that is not text), or two fields on one column, makes the struct `ValueInvalid` — which is what `pkg/v1/data/codec` reads as "promote the value", so a struct with nested data still travels as one JSON cell. Unknown columns are ignored, a UTF-8 BOM before the first header cell is dropped, and with `NoHeader` the columns are the fields in order. Schemas are built once per type (`sync.Map`).
- **The facade's promotion keeps reading old tables.** Before #902 `codec.Marshal(codec.CSV, []T)` wrapped a slice of flat structs in one `_json` cell; it now writes a typed table. A table in the old container read into such a slice is refused `ValueInvalid` (`isPromotionHeader`) unless the struct maps a `_json` column, so the facade's promotion still unwraps it.
- **OWASP CSV-Injection mitigation (CWE-1236)** is opt-in via `NewWithEscape(true)`. Cells whose first byte is one of `= + - @ \t \r` get prefixed with `'` so spreadsheet apps render them as text. Default `New()` preserves lossless wire-format fidelity.
- **The encoder is the package's own, and it writes `encoding/csv`'s bytes** (`encode.go`): `appendRecords` writes each record as `encoding/csv.Writer.Write` writes it with `UseCRLF` false — a field quoted only when `fieldNeedsQuotes` says so, a quote doubled — straight into the destination, and `recordsLen` sizes it exactly, so `Marshal` allocates its result once and `Append` nothing when `dst` has the room (#1124, `BENCH.md`). `TestAppendRecordsIsEncodingCSV` and `FuzzAppendRecordsIsEncodingCSV` pin it to `encoding/csv` byte for byte, with and without the mitigation.
- **The table is reserved from the input's non-empty lines, at most 4 096** (`recordsHint`, `maxReservedRecords`, #1098) — encoding/csv skips empty lines, so a newline count over-reserved without bound (1 MiB of `'\n'`: 24 MiB of slice headers). `append` grows it past the cap. `sizing_integration_test.go` pins it.
- The mitigation is applied cell by cell as the cell is written (`specFor`: the `'` prefix inside the quotes when the cell needs them), never on a copy of the records — the caller's slice is read, not changed.
- `NewWithEscape` returns a fresh instance — it is **not** added to the core registry; resolution-by-name returns the default singleton.

## Verification

```
bazel test --config=race //internal/service/data/codec/csv:csv_test
```

## Fuzz (#897)

`fuzz_external_test.go` — `FuzzUnmarshalRoundTrip`: any input decodes without a panic; decoded records re-encode and read back equal, and `NewWithEscape(true)` never writes a cell opening on a formula character. A record of one empty field is left out: `encoding/csv` writes it as an empty line every reader skips, and this encoder keeps `encoding/csv`'s bytes (`FuzzAppendRecordsIsEncodingCSV`) — a standard-library limit, not a codec defect; so is the `\r` its reader drops before `\n`, quoted or not. The seed corpus runs with the package's tests; `scripts/ci/codec-fuzz.sh` (nightly, `codec-fuzz.yml`) explores it.
