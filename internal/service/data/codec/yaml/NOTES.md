# internal/service/data/codec/yaml — notes

Reference moved out of `CLAUDE.md` (ADR 0253), unchanged and under the same section titles.

## The subset

**Read**: block mappings and sequences by indentation (a sequence at its
key's own indentation, the compact `- key: value` entry, `- - nested`), flow
mappings and sequences across lines (`[a, b]`, `{k: v}`, a single-pair
mapping `[a: 1]` inside a flow sequence, a key with no value `{a, b: 2}`),
plain scalars across lines, single-quoted (`''`) and double-quoted scalars
with every YAML escape except `\/`, literal `|` and folded `>` block scalars
with chomping (`-` / `+`) and indentation (`1`-`9`) indicators, comments, and
ONE document with an optional `---` start and `...` end. A UTF-8 byte order
mark may open the document, and nowhere else; CRLF and a lone CR read as one
line break.

**Core schema** (YAML 1.2.2 §10.3.2), for a plain scalar only — a quoted or
block scalar is always a string: `null`/`Null`/`NULL`/`~`/nothing;
`true`/`false` in three cases — never `yes`, `no`, `on`, `off`; decimal,
`0o` octal and `0x` hexadecimal integers (an `int64`, a `uint64` past it);
floats with `.inf`, `-.inf`, `.nan`. Everything else is a string.

**Refused BY NAME** — each with its own sentinel, the line and the column it
starts at, and `UNMARSHAL_FAILED`'s code in its trail:

| Construct | Example | Sentinel | Code |
|---|---|---|---|
| anchor | `&base` | `AnchorRefused` | `0.3.4.3` |
| alias | `*base` | `AliasRefused` | `0.3.4.4` |
| tag | `!!str`, `!local`, `!<uri>` | `TagRefused` | `0.3.4.5` |
| merge key | `<<: {a: 1}` | `MergeKeyRefused` | `0.3.4.6` |
| a second document | `a: 1\n---\nb: 2` | `MultipleDocumentsRefused` | `0.3.4.7` |
| complex key | `? key`, `[a, b]: v`, `{[a]: b}` | `ComplexKeyRefused` | `0.3.4.8` |
| directive | `%YAML 1.2`, `%TAG` | `DirectiveRefused` | `0.3.4.9` |
| duplicate key | `a: 1\na: 2`, `a: 1\n"a": 2` | `DuplicateKey` | `0.3.4.10` |
| leading zero where the value matters | `mode: 0644` into an int or `any` | `LeadingZeroRefused` | `0.3.4.11` |

Everything else outside the subset is `UnmarshalFailed` with a constant
`detail` naming it: a multi-line implicit key, a reserved indicator (`@`,
`` ` ``), a tab where indentation is, an unknown escape, content on the `---`
line, a `...` closing nothing. So is what YAML itself refuses that libyaml
let through (#1107, the yaml-test-suite's 9C9N, QB6E, DK95/01, Y79Y/003): a
line of a flow collection or of a quoted scalar in block context must be
indented deeper than the block holding it, with spaces — a tab is not
indentation; a line opening with the collection's closing `]` or `}` is not
judged, so `a: [1,\n  2,\n]` still reads. **Refused by the source check**, before the
parse: a document that is not UTF-8 (a UTF-16/32 byte order mark, an invalid
byte), a raw control character other than tab and the line breaks, a raw
U+0085 / U+2028 / U+2029 — a line break to YAML 1.1 and text to YAML 1.2, so
a reader cannot tell which a raw one is; `\N`, `\L`, `\P` write them
unambiguously — and a raw U+FEFF anywhere but first, which libyaml skips
where a line starts and keeps as text elsewhere (`\uFEFF` writes it).

## Go values

- Struct fields follow the `yaml` tag as yaml.v3 does: the key is the tag's
  name or the field's own name in lower case, `-` leaves it out, and the
  flags are `omitempty` (asks `IsZero()` first), `omitzero` (#1103, which
  yaml.v3 refuses: encoding/json's rule — the type's `IsZero()` when it
  declares one, through the field's address when only the pointer does, else
  `reflect.Value.IsZero`, decided once per field in `omitzero.go`; with
  `omitempty` too either one leaves the field out, so a type tagged for
  encoding/json marshals as YAML), `flow` (write the
  collection inline) and `inline` (merge an embedded struct's fields, or
  collect the remaining keys into a `map[string]T`). An unknown flag, an
  `inline` on anything else, inlined structs forming a cycle and two fields
  declaring one key are refused. An unknown key is ignored.
- Hooks: `MarshalYAML() (any, error)` (yaml.v3's `Marshaler`, method for
  method), `UnmarshalYAML(func(any) error) error` (the yaml.v2 form yaml.v3
  still calls), `encoding.TextMarshaler`/`TextUnmarshaler`. yaml.v3's
  `UnmarshalYAML(*yaml.Node)` names a yaml.v3 type and is refused. Hook sets
  are read once per type (`hooks.go`), the tag plan once per type
  (`structinfo.go`).
- `time.Time` is written through its `MarshalText` (RFC 3339) and read from
  the forms yaml.v3 read; `time.Duration` as its `String()`, read by
  `time.ParseDuration` — an integer is refused, its unit would be a guess.
  This is the rule of the hand-edited formats, which `toml/` follows too
  (#1126); the binary formats write int64 nanoseconds.
- An untyped target receives `nil`, `bool`, `int` (`int64` where an `int`
  is narrower, `uint64` past `int64`), `float64`, `string`, `[]any` and
  `map[string]any` — a mapping keyed by each key's TEXT, whatever the key's
  type (yaml.v3 built a `map[any]any` for a non-string key). A timestamp
  stays a string. The `int64` reading is reached only where an `int` is 32
  bits wide, so CI's `test-386` lane is what checks it: a test spells such an
  expectation through `untypedInt`, never `math.MaxInt`/`math.MinInt`, which
  are 32-bit values there.
- A float decodes into an integer target only when it holds a whole number
  exactly; `[]byte` is written as a flow sequence of integers.
- The encoder writes block style indented by two spaces, struct fields in
  declaration order, map keys sorted (numbers by value), a multi-line string
  as a literal block (with its indentation indicator when the first line
  starts with a blank), a nil map or slice as an empty collection.

## Contents

| File | Role |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `codec.go` | the registered singleton, `Marshal`/`Unmarshal`/`Append`, the byte cap |
| `failed.go` | `at`, `syntaxError`, `refused`, `marshalError`; the eleven codes and sentinels are `internal/core/data/codec/yaml`'s (ADR 0160) |
| `node.go` | the node arena (one slice per document, children by index) and the bounds |
| `parse.go` | the parser's state and cursor, document markers, directives, duplicate keys |
| `block.go` | block mappings and sequences, implicit keys, the refusals at a node's start |
| `flow.go` | flow mappings and sequences across lines |
| `scan.go` | plain, single-quoted, double-quoted (escapes) and block scalars |
| `resolve.go` | the core schema, leading zeros, and what YAML 1.1 reads differently (for the writer) |
| `unmarshal.go` | the source check, the untyped decode, the parser pool |
| `unmarshal_typed.go` | decoding into typed targets by reflection, hooks, time, duration |
| `structinfo.go` | the per-type plan of a struct's `yaml` tags |
| `hooks.go`, `hooks_interface.go` | the hooks a type implements, read once per type |
| `marshal.go` | the encoder: values resolved through hooks, pointers and interfaces |
| `marshal_block.go` | block layout of mappings, sequences, keys and literal blocks |
| `marshal_flow.go` | flow layout, for the `flow` flag |
| `marshal_scalar.go` | when a scalar may be plain, double-quoting, float spelling |
| `encoder.go`, `decoder.go` | the streaming encoder and the `---`-splitting stream decoder |

Tests: `subset_external_test.go` (what is read), `refusal_external_test.go`
(every refusal by code, line and column; no refusal quotes the input),
`limits_external_test.go` (every bound; its two documents of 2^20 nodes are
left out of a coverage build, where the race detector makes each counter an
instrumented atomic and one of them took 55 s of the 60 s budget),
`parse_internal_test.go` (the node bound at `maxNodes` itself, through the
arena — what keeps that bound inside the coverage run),
`marshal_external_test.go` and
`unmarshal_external_test.go` (the Go mapping), `stream_external_test.go`,
`fuzz_external_test.go` (`FuzzUnmarshal`), `codec_integration_test.go`
(`//go:build !race` allocation ceilings — the alloc lane runs it),
`codec_bench_test.go` (fixtures shared verbatim with
`third-party/codec/yaml`). The differential fuzzers and their regression
inputs live in `third-party/codec/yaml`, because they link yaml.v3.

## Conformance (#1107)

`testdata/yaml-test-suite/` is the YAML project's suite, tag `data-2022-01-17`
— MIT, `LICENSE` beside it, `README.md` there naming the source and the four
files copied of each case (`===`, `in.yaml`, `in.json`, `error`), `-text` in
`.gitattributes` so no checkout rewrites a byte. `conformance_external_test.go`
runs every one of its **402** cases in the ordinary lane, no network:

| Outcome | Cases |
|---|---|
| valid, decoded equal to `in.json` | **143** |
| valid, refused by a named subset sentinel (anchor, tag, directive, complex key, second document…) | **102** |
| invalid, refused | **94** |
| invalid, accepted | **0** |
| skipped, in a named group with its reason | **63** |

The skipped groups: 60 valid cases refused with `UNMARSHAL_FAILED` and one of
ten constant details, each a construct this file lists outside the subset
(`subsetLimits` in the test — a valid case refused with any other detail
fails the run); 2 block scalars the input ends inside on a line of blanks
(JEF9/02, L24T/01), read as yaml.v3 reads them — without the line break the
suite's reference parser adds at the end of the input —, because the subset
replaced yaml.v3 and `FuzzNativeAgreesWithYAMLv3` holds it to its values; and
1 case the suite gives no `in.json` (4ABK). The test asserts the five counts,
so a change of the reader that moves one is seen. To refresh: copy the same
four files of every case from a newer data tag, update `README.md` there, the
counts and this table.
