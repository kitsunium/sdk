<!-- updated: 2026-09-28T23:59:00Z -->
# internal/service/data/codec/jsonpatch/

## Purpose

The structural difference between two JSON documents as RFC 6902 operations
— `add`, `remove`, `replace`, each at an RFC 6901 JSON Pointer — with the
value each operation writes AND the value it replaces or removes (ADR 0143
§D9). It is what a record's history needs to say what changed between two of
its versions: `docstore` keeps versions as JSON, and a framework's `Diff` of
two of them is this. Public facade: `pkg/v1/data/codec/jsonpatch`.

Stdlib only; not a codec — it registers no Format. It reads documents with
`encoding/json/jsontext`, strictly, and owns `0.3.90.*`.

## Contents

| File | Role |
|---|---|
| `jsonpatch.go` | package doc, `Op` (`Add`, `Remove`, `Replace`), `EditValue`, `Diff`; the `differ` — objects member by member in name order, arrays aligned (`align` → `lcsLengths`, bounded by `maxAlignCells`) then paired in runs (`changeRun`), anything else replaced; `escapeToken` |
| `tree.go` | the `node` a document is read into (`parse` → `readValue` / `readObject` / `readArray` / `readMember`), its `digest` (FNV-1a over what `equal` compares, members summed so their order does not count), `equal` (the digests first, then RFC 6902 §4.6), `sameNumber` / `canonical` (exact decimal equality, linear in the text), `encode` (compact, members in the document's order, a number as written), `notJSON` |
| — | `0.3.90.1` `NOT_JSON` is declared in `internal/core/data/codec/jsonpatch` (ADR 0160 §2) and used here as `corejsonpatch.NotJSON`; its 400 is a literal there, so neither package links `net/http` for a number |

## Why-this-shape

- **Equality is the RFC's, not the bytes'.** Two documents a store wrote at
  different times may spell a number or escape a string differently; a diff
  that reported that would report nothing a reader cares about. Objects compare
  whatever their member order.
- **Numbers compare exactly, never through a float.** A number is reduced to a
  sign, its significant digits and a power of ten — the exponent a `big.Int`,
  because jsontext bounds nothing there — so `1e400` is compared, not
  overflowed, and `1e999999999999999999999` costs its length, not ten to that.
- **Every value carries a digest of what `equal` compares**, built once as
  the document is read: two values that differ are told apart in one
  comparison, however large they are, so aligning two arrays of large
  elements costs one comparison a pair of cells, not a walk of both. Equal
  values always share it — members summed rather than chained, every zero
  alike, a number by its sign, digits and power of ten — and a shared digest
  is only a reason to compare in full, never a verdict.
- **Arrays are aligned, then paired.** The equal elements at both ends are
  trimmed; the longest common subsequence of the middles says which elements
  both keep; between two kept ones, the removed and the added are paired in
  order and diffed in turn — so a block edited in place is a nested `replace`,
  an insertion one `add`. The alignment table is bounded (`maxAlignCells`,
  2^18 cells): past it, the middles are paired by position, a longer patch that
  is still correct.
- **Indices are the applied ones.** Operations are emitted left to right, and
  each index is that of the array as the operations before it left it, so the
  list applies in order, as RFC 6902 applies a patch — the property the suite
  checks on four thousand generated pairs with an applier of its own.
- **Both values travel.** `Old` is what a reader of a history needs beside the
  new `Value`; it is encoded under `old`, which RFC 6902 does not define for
  these operations and an applier ignores (§4), so the encoded list is still a
  JSON Patch document.
- **Deterministic.** Members in byte order of their names; the same two
  documents give the same operations, byte for byte.
- **Nothing from a document reaches an error.** jsontext quotes the character
  it stopped at; `NOT_JSON` names the document and the offset only.

## Do NOT

- Compare numbers as floats, or scalars as their raw bytes.
- Emit `move` or `copy`: an element moved is a removal and an insertion, which
  a reader of a history reads without a second pointer.
- Keep a `jsontext.Token` across a decoder call: it is void after the next one.

## Verification

```
bazel test --config=race //internal/service/data/codec/jsonpatch:jsonpatch_test
cd internal/service && GOWORK=off go test -race ./data/codec/jsonpatch/
```

`TestEveryPatchTurnsTheFirstDocumentIntoTheSecond` is the oracle: 4 000 pairs
of generated documents — nested objects and arrays, names needing escapes,
the second a random rewrite of the first — each patch applied in order by the
suite's own RFC 6902 applier, every `old` checked against what the path held.

## Reference

- ADR 0143 §D9; RFC 6902 (JSON Patch), RFC 6901 (JSON Pointer), RFC 8259
