<!-- updated: 2026-10-03T00:00:00Z -->
# internal/service/codec/toml/

## Purpose

The TOML codec, written natively against TOML v1.0.0 with the standard library
alone — no third-party module, so that the public `pkg` module moves toward
depending on the standard library and the SDK only. It replaced a wrapper around
`github.com/pelletier/go-toml/v2` v2.4.3 and keeps its observable behaviour:
same package, API, `Format` name and error codes; the same decoded values
(differentially fuzzed against it, 19.7 M inputs, no divergence); the same
encoded bytes (identical on all 262 valid toml-test documents and on every
struct-tag option).

## Surface

| Item | Value |
|---|---|
| `Name()`         | `"toml"` |
| `MIMETypes()`    | `application/toml` |
| `Extensions()`   | `.toml` |
| Constructor      | `New() codec.Codec` (the registered singleton) |
| Streaming        | yes — `NewEncoder` writes one document per `Encode`; `NewDecoder` reads its reader whole, once, within the size cap |
| Appender         | yes — `Append` encodes straight onto `dst`, which keeps its length on failure |
| `LocalDate`, `LocalTime`, `LocalDateTime` | the three TOML kinds Go has no type for. Same fields and methods (`AsTime`, `String`, `MarshalText`, `UnmarshalText`) as the go-toml types they replace, so a type switch migrates by changing its import |

## Error codes (range `0.3.5.*`)

| Code | Var | Trigger |
|---|---|---|
| `0.3.5.1` | `MarshalFailed`   | a value TOML cannot represent: a non-table root, nil, chan/func/complex, an unsigned integer above `MaxInt64`, a string or key that is not UTF-8, two map keys written alike, a cycle; a writer or `MarshalText` failure (its error kept as the cause) |
| `0.3.5.2` | `UnmarshalFailed` | a document that is not TOML, past the size or depth cap, or a value its target cannot hold; a reader or `UnmarshalText` failure (cause kept) |

No other code: every refusal keeps the reason consumers already match
(`HasReason(err, "UNMARSHAL_FAILED")` — pinned by `pkg/v1/codec`'s V75 test).
The diagnosis is in fields: `problem` (a fixed sentence), `line` and `column`
(1-based, columns in characters), and for a typed decode `key` (the dotted
key), `toml` (the kind of value) and `type` (the Go type). **No field and no
message ever carries a byte of the document or of the value** — that is where
secrets live; `TestRefusalNeverQuotesTheDocument` pins it.

## Design

- **Two phases.** `parser` (`parser*.go`, `parse_*.go`) reads the whole
  document into an arena tree (`node`, linked by `int32` index, keys and
  strings as spans into the input or into one unescape buffer), refusing
  anything the grammar or the redefinition rules forbid; only then does
  `decoder` (`decode*.go`) write into the target. A refused document writes
  nothing.
- **The redefinition rules are an `origin` per table** — implicit (a longer
  header implied it), header, dotted (a dotted key created it), inline, element
  (of an array of tables). A `[header]` may define an implicit table once and
  pass through any table but an inline one; a dotted key may only extend a
  dotted table; nothing extends an inline table or a static array. These are
  exactly the rules of go-toml's seen-tracker, which the corpus and the
  differential fuzz both confirm.
- **Wide tables are hashed.** A table is scanned up to 16 children, then moves
  to a `maphash` index (`parser_tree.go`): a 100 000-key table costs 100 000
  lookups, not five billion comparisons (go-toml's tracker was quadratic).
- **Caps** (`const`, per the codec conventions): `maxDocumentBytes` 10 MiB
  (CWE-400, as YAML and MessagePack; the streaming decoder reads through a
  `LimitReader`) and `maxDepth` 128 levels of tables and arrays (CWE-674; the
  parse and the decode recurse once per level). The encoder applies the same
  depth cap, which is also what refuses a cyclic value instead of overflowing
  the stack.
- **Pools.** The parser (arena included) is pooled through
  `kernel/recycler.NewCappedPool`, dropped past 256 KiB; the encoder's entry
  slices likewise. `Marshal` and the streaming encoder write into the shared
  `core/codec/scratch` buffer; `Append` writes onto `dst` directly.
- **Plans per type.** `plan.go` caches one `typeInfo` per `reflect.Type`: the
  struct's fields under the `toml` tag (embedded structs flattened, the
  shallower field winning, then the first declared), an exact and a
  case-folded index (exact match first, as encoding/json), and the roles the
  type plays (TextMarshaler, TextUnmarshaler, IsZero, time/local type).

## Mapping

| TOML | untyped target (`map[string]any`, `any`) | typed targets |
|---|---|---|
| table | `map[string]any` (merged into one already there) | struct, `map[K]V` (`K` a string, an integer, a float or a TextUnmarshaler) |
| array, array of tables | `[]any` | slice (replaced), array (filled, rest zeroed, extra dropped) |
| string | `string` | string kinds; any TextUnmarshaler (`time.Time`, `net.IP`, the local types…) |
| integer | `int64` | signed, unsigned (range-checked), float kinds |
| float | `float64` | float kinds (`float32` range-checked) |
| boolean | `bool` | bool |
| offset date-time | `time.Time` (UTC for a zero offset, else a fixed zone) | `time.Time` |
| local date-time / date / time | `LocalDateTime` / `LocalDate` / `LocalTime` | the same type, or `time.Time` in `time.Local` |

Encoding: struct fields in declaration order, map keys sorted; key-values
first, then `[tables]` and `[[arrays of tables]]`, a blank line before each
header that does not follow another; strings literal unless they hold an
apostrophe or a control character; floats in the shortest `'f'` form with
`.0` when integral. Tag options: `omitempty`, `omitzero`, `inline`,
`multiline`, `commented`, plus the standalone `multiline:"true"`,
`inline:"true"`, `commented:"true"` and `comment:"…"` tags.

## What changed for consumers (versus go-toml v2.4.3)

- `LocalDate`, `LocalTime`, `LocalDateTime` in an untyped decode are this
  package's types (re-exported by `pkg/v1/codec/toml`), no longer go-toml's.
- A string or key that is not valid UTF-8 is **refused** at Marshal; go-toml
  rewrote each bad byte as the character of the same number — a value the
  caller did not hold, reported as success.
- A decimal integer outside int64 is refused in every target, as the
  specification requires; go-toml let one into a float field.
- Documents past 10 MiB or nested past 128 levels are refused (go-toml had no
  size cap and capped only arrays and inline tables, at 10 000).
- Accepted, as before: the four TOML v1.1.0 relaxations go-toml accepted —
  newlines, comments and a trailing comma in an inline table; `\e` and `\xHH`;
  a time without seconds. Refusing them would have broken documents that
  decode today. The encoder writes TOML v1.0.0 only.

## Conformance

`testdata/toml-test/` is toml-test v2.1.0 — the TOML project's suite, MIT
(`LICENSE` beside it) — copied byte for byte from the upstream tag's `tests/`
directory, the files its `files-toml-1.0.0` and `files-toml-1.1.0` lists name.
`.gitattributes` there marks every file `-text` so no checkout rewrites a line
ending the suite tests. `conformance_external_test.go` runs it: every valid
document decodes to its expected value and round-trips through the encoder;
every invalid one is refused, except the nine v1.0.0 files the v1.1.0
relaxations above make valid, which are pinned as accepted. To refresh:
download the new tag, copy the files both lists name, update the count
assertions.

## Performance

`BENCH.md` — go-toml and the native codec, the same benchmark file run in
alternation on one machine. Encoding is at parity (0.92–1.02×, one allocation
fewer); decoding into a struct is 0.73×; into `map[string]any` 1.06× on a
configuration and 1.16× on a thousand tables — the cost of checking the whole
tree before writing the map; a 10 000-key table 0.024× (go-toml's tracker was
quadratic). A document past ~4 000 nodes outgrows the 256 KiB pool ceiling, so
its arena is allocated per call — once, sized from the document's separators.

## Do NOT

- Add a third-party import. The point of this package is that it has none.
- Quote document bytes, keys aside, or values in an error.
- Add an error code for a decode failure: consumers match `UNMARSHAL_FAILED`.
- Reorder the encoder's output: it is byte-compatible with what consumers'
  files already contain, and `TestGoldenLayout` pins it.
- Edit `testdata/toml-test/` by hand.

## Verification

```
bazel test --config=race //internal/service/codec/toml:toml_test
cd internal/service && GOWORK=off go test -run='^$' -fuzz='^FuzzUnmarshal$' -fuzztime=60s ./codec/toml/
cd internal/service && GOWORK=off go test -run='^$' -fuzz='^FuzzUnmarshalTyped$' -fuzztime=60s ./codec/toml/
cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem ./codec/toml/
```

`TestAllocBudget` (`codec_integration_test.go`) is `//go:build !race` and runs
in the race-off alloc lane.
