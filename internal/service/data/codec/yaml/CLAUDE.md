<!-- updated: 2026-10-05T00:00:00Z -->
# internal/service/data/codec/yaml/

## Purpose

The SDK's `"yaml"` Format: a **native, standard-library-only** reader and
writer of a **named subset of YAML 1.2.2**, sized for configuration. It
replaced the `gopkg.in/yaml.v3` wrapper (decision D6 of the tree
reorganisation), so the SDK module — `internal/service` and the public
packages alike — links no third-party YAML library. The whole of YAML — every construct this
package refuses — stays available through the opt-in
`third-party/codec/yaml`, registered as `"yaml-full"` and claiming no MIME
type and no extension.

## Surface

| Item | Value |
|---|---|
| `Name()` | `"yaml"` |
| `MIMETypes()` | `application/yaml`, `text/yaml`, `application/x-yaml` |
| `Extensions()` | `.yaml`, `.yml` |
| Constructor | `New() codec.Codec` (the stateless registered singleton) |
| Streaming | yes — `NewEncoder` writes one document per `Encode`; `NewDecoder` reads a `---`-separated stream, one document per `Decode` |
| Appender | yes — `Append(dst, v)` writes into a pooled buffer, then appends onto `dst` |
| Code range | `0.3.4.*` |

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
line, a `...` closing nothing. **Refused by the source check**, before the
parse: a document that is not UTF-8 (a UTF-16/32 byte order mark, an invalid
byte), a raw control character other than tab and the line breaks, a raw
U+0085 / U+2028 / U+2029 — a line break to YAML 1.1 and text to YAML 1.2, so
a reader cannot tell which a raw one is; `\N`, `\L`, `\P` write them
unambiguously — and a raw U+FEFF anywhere but first, which libyaml skips
where a line starts and keeps as text elsewhere (`\uFEFF` writes it).

## Why-this-shape

- **Refused by name, never read some other way.** A configuration reader
  that silently skips a tag, expands an alias or takes `yes` for `true` reads
  a different file from the one the operator wrote. Every construct outside
  the subset is a typed refusal naming the construct and where it starts, so
  the fix — quote it, spell the value out, or opt into `"yaml-full"` — is in
  the message.
- **No anchors and no aliases** removes alias expansion (CWE-776, "billion
  laughs") at the root: the decoded value can never be larger than the
  document, so the byte cap is the whole budget.
- **A strict subset of what yaml.v3 reads, as yaml.v3 reads it.** Where the
  YAML 1.2.2 text and libyaml disagree, the subset either follows libyaml or
  refuses — it never accepts a document yaml.v3 refuses, nor reads one as a
  different value. `FuzzNativeAgreesWithYAMLv3` (in `third-party/codec/yaml`,
  the only module allowed to link yaml.v3) holds that line, and found every
  alignment below:
  - a `:` followed by a flow indicator stays inside a plain scalar, and a
    flow key's other `:` stay in the key;
  - a `?` that would open an explicit key in flow context is refused;
  - a `...` with no document before it is refused, not ignored;
  - a tab is refused wherever the line's indentation is, even on a blank or
    a comment line, and after a sequence entry's `-`;
  - the `\/` escape is refused (libyaml has no such escape);
  - a block scalar's indentation is found by libyaml's algorithm, leading
    empty lines and the indentation indicator relative to the parent
    included;
  - an implicit key is bounded at 1024 characters (YAML §7.4.2), counted in
    runes as the decoder counts them, on a flow key that never reaches its
    `:` too;
  - a `\U` escape past U+10FFFF or naming a surrogate is refused — checked on
    the unsigned value, since `\U80000000` would otherwise wrap to a negative
    rune that `utf8.AppendRune` writes as U+FFFD without a word;
  - a second byte order mark is refused (libyaml skips it, so yaml.v3 read
    `\ufeff\ufeff` as an empty document while a literal reading is text).
- **The writer quotes for every reader, not only for itself.** What it
  writes is read by YAML 1.1 tools too, so a string is plain only when the
  core schema, YAML 1.1 (`yes`, `0777`, `1_000`, base 60, `.Inf`), yaml.v3
  (which drops every underscore before reading a number, so `+_0` is 0) and
  a timestamp parser all read it back as the same string, and when it is not
  `<<` (a merge key to yaml.v3, refused as a key here); otherwise it is
  double-quoted. A float always carries its dot (`1.0`, `1.0e+21`), since
  YAML 1.1 needs it and the core schema reads a dotless decimal as an
  integer. `FuzzNativeOutputReadsTheSame` reads every output back with BOTH
  readers.
- **`Unmarshal` reads one document; the stream decoder reads many.**
  yaml.v3 silently ignored every document after the first — a second
  document is a refusal here. `NewDecoder` keeps the `StreamingCodec`
  contract: it splits the stream at `---` lines, reads each document whole
  (bounded at 10 MiB) and parses it exactly as `Unmarshal` does, with line
  numbers counted from the start of the stream.
- **Leading zeros**: `0644` is octal 420 to YAML 1.1 and decimal 644 to YAML
  1.2. Into a string it is the text `0644`; into a number or `any` it is
  refused by name, because either reading would be a guess.
- **A refusal never quotes the document.** It carries `line`, `column`, a
  constant `detail` and, for a value its Go target cannot hold, the Go `type`.
  A configuration file holds secrets, and an error is logged;
  `TestRefusalsNeverQuoteTheDocument` plants one and reads every rendering.
- **No panic.** Every input is bounded (below), and `FuzzUnmarshal` runs the
  decoder into an untyped and a typed target.

## Bounds (`node.go`)

| Bound | Value | Applies to |
|---|---|---|
| `maxYAMLBytes` | 10 MiB | `Unmarshal`, and each document of a `NewDecoder` stream |
| `maxNodes` | 1 048 576 | nodes in one document |
| `maxDepth` | 100 | collection nesting, reading AND writing; `UnmarshalYAML` re-entry |
| `maxKeyRunes` | 1024 | an implicit key, reading AND writing (YAML §7.4.2) |
| `maxPooledNodes` | 16 384 | the node arena a recycled parser may keep |

## Go values

- Struct fields follow the `yaml` tag as yaml.v3 does: the key is the tag's
  name or the field's own name in lower case, `-` leaves it out, and the
  flags are `omitempty` (asks `IsZero()` first), `flow` (write the
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

## What a caller of the old wrapper sees change

`yes`/`no`/`on`/`off` into a `bool` are refused (they were `true`/`false`);
`0644` is refused where its value matters (it was octal 420); `1_000`,
`0b101` and timestamps are text in `any` (they were numbers and
`time.Time`); a float into an int must be exact; a second document is
refused by `Unmarshal`; `UnmarshalYAML(*yaml.Node)` is refused; a mapping
with non-string keys reads as `map[string]any`; and the constructs in the
table above are refused by name. `third-party/codec/yaml` (`"yaml-full"`)
reads every one of them as before.

## Error codes (range `0.3.4.*`)

Declared in `internal/core/data/codec/yaml` — `codes.go` and `errors.go` — and used
here as `coreyaml.<Var>` (ADR 0160 §2: a code lives in the core at the path that
mirrors the package emitting it). The values, reasons and texts are the ones this
package always emitted; only the declaration moved.

| Code | Var | Reason | Trigger |
|---|---|---|---|
| `0.3.4.1` | `MarshalFailed` | `MARSHAL_FAILED` | an unsupported kind, a non-UTF-8 string, a non-scalar key, a key past 1024 characters, nesting past `maxDepth`, a failed `MarshalYAML`/`MarshalText`, a malformed tag, a failing stream writer |
| `0.3.4.2` | `UnmarshalFailed` | `UNMARSHAL_FAILED` | a syntax error (`detail` names it), input past a bound, a value its target cannot hold (`type` names the target); the trail code of every refusal below |
| `0.3.4.3` | `AnchorRefused` | `ANCHOR_REFUSED` | `&name` |
| `0.3.4.4` | `AliasRefused` | `ALIAS_REFUSED` | `*name` |
| `0.3.4.5` | `TagRefused` | `TAG_REFUSED` | `!`, `!!`, `!<…>` |
| `0.3.4.6` | `MergeKeyRefused` | `MERGE_KEY_REFUSED` | a plain `<<` key |
| `0.3.4.7` | `MultipleDocumentsRefused` | `MULTIPLE_DOCUMENTS_REFUSED` | a second document in `Unmarshal` |
| `0.3.4.8` | `ComplexKeyRefused` | `COMPLEX_KEY_REFUSED` | `?`, a flow collection as a key |
| `0.3.4.9` | `DirectiveRefused` | `DIRECTIVE_REFUSED` | `%` at a line's start: `%YAML`, `%TAG` |
| `0.3.4.10` | `DuplicateKey` | `DUPLICATE_KEY` | one key twice — by text in the document, by decoded value in a typed map |
| `0.3.4.11` | `LeadingZeroRefused` | `LEADING_ZERO_REFUSED` | `0644` into a number or `any` |

Codes `0.3.4.1` and `0.3.4.2` keep their values and reasons from the yaml.v3
wrapper. A refusal's origin is its own sentinel (`errs.HasReason`,
`errs.CodeOf`), and `errs.HasCode(err, CodeYAMLUnmarshalFailed)` matches every
decoding failure through the trail.

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

## Performance

`BENCH.md` holds the numbers, measured against the yaml.v3 wrapper on the same
fixtures: 3 to 10 times faster, with 1 to 33 allocations per call where
yaml.v3 made 28 to 381 on the small and medium fixtures. The parser builds one flat
node arena per document and recycles it through a pool; the input is copied
once into a string and a scalar is a slice of that copy unless an escape or a
fold rewrites it — so a decoded string keeps the document's text alive while
it is held; a struct's tag plan and a type's hooks are computed once; the
encoder writes straight into the shared `internal/core/data/codec/scratch` buffer
pool and copies the result out once.

## Do NOT

- Accept a construct the subset refuses "to be lenient": the subset's value
  is that a document reads the same everywhere. A caller who needs one uses
  `"yaml-full"`.
- Read a document yaml.v3 refuses, or read one differently — run
  `FuzzNativeAgreesWithYAMLv3` after any change to the scanner or the parser.
- Quote the input in an error: `detail` is a constant, the position is the
  locator.
- Add a dependency: this package is standard library + kernel + core only.
- Use `fmt.Errorf`/`errors.New`; wrap through `errs.Wrap`.

## Verification

```
bazel test --config=race //internal/service/data/codec/yaml:yaml_test
bazel test --config=alloc //internal/service/data/codec/yaml:yaml_test   # the !race allocation ceilings
go test -race ./internal/service/data/codec/yaml/
go test -run='^$' -fuzz='^FuzzUnmarshal$' -fuzztime=60s ./internal/service/data/codec/yaml/
go test -run='^$' -fuzz='^FuzzNativeAgreesWithYAMLv3$' -fuzztime=60s ./third-party/codec/yaml/
go test -run='^$' -fuzz='^FuzzNativeOutputReadsTheSame$' -fuzztime=60s ./third-party/codec/yaml/
```
