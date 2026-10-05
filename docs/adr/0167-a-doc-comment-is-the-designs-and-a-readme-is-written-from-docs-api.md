# ADR 0167 — a doc comment is the design's, and a README is written from docs/api

- **Status**: Accepted
- **Date**: 2026-10-05
- **Deciders**: SDK maintainers (the owner's decision: stage 4 of "kit regenerates the Go SDK from its design" — "that kit be the tool that can generate the documentation and the (theoretical) code from which the most efficient code possible can be generated"; question 2, option b: gomarkdoc retired)
- **Amends**: [ADR 0163](0163-the-sdk-is-designed-by-its-diagram-and-kit-writes-only-data-and-test-pins.md) §1 (doc text recorded for generated declarations only), §2 (what kit writes), §3 (what CI checks), §4 ("a doc edit needs no kit") and §12 (the READMEs stay gomarkdoc's); [ADR 0008](0008-readme-from-code-generation.md) (READMEs from Go doc comments through gomarkdoc); [ADR 0154](0154-the-sdks-principles-are-one-charter-and-an-incidents-rule-lives-with-its-code.md) principle 28 (where a README comes from); [ADR 0166](0166-a-facade-is-the-designs-and-a-forwarders-form-is-measured.md) §Consequences (a doc edit on a hand-written declaration or a package comment needs no kit)
- **Related**: [ADR 0164](0164-an-error-code-is-the-designs-and-kit-writes-it.md), [ADR 0165](0165-a-performance-contract-is-the-designs-kit-measures-what-the-compiler-decides-and-a-test-holds-the-rest.md), [ADR 0138](0138-a-doc-link-resolves-or-it-is-not-written.md) (doc links), [ADR 0153](0153-the-repository-carries-no-devcontainer-and-no-git-hooks.md)

## Context

After stages 1 to 3 the design held the SDK's exported surface, its error
codes and its facades, and the doc comments of what kit writes — ports,
codes, sentinels, re-exports. Every other doc comment — 7 000-odd hand-written
declarations, their fields and methods, and every package comment — was still
the code's alone, and ADR 0163 §4 said so plainly: "a doc edit needs no kit".
Three documents were therefore written from the code after the fact: docs/api
(genindex, from the code), the READMEs (gomarkdoc, from the code) and the docs
portal (from docs/api and the READMEs).

The owner's goal for kit is that it be the tool that generates the
documentation as well as the code. So the doc text joins the design, and the
SDK's CI must still hold the code to it without kit (ADR 0163 §3): kit is
private, the SDK is public, and principle 26 says a rule nothing checks is a
suggestion.

gomarkdoc had three costs of its own: it showed an alias's declaration and
nothing of the type it names (go/doc does not follow an alias — 480 of
`pkg/v1`'s types are aliases); its exit status was unreliable when one call
checked several packages (the drift script ran it once per package for that
reason); and its source links depended on the checkout's git state, which is
why CI cloned with full history. docs/api already records every symbol, its
doc, an alias's owner and the owner's fields and methods.

## Decision

### 1. Every doc comment is the design's (`project.docs: design`)

`design/sdk.yaml` says `docs: design` (kit `v0.1.0-rc.5`, platform ADR 0010's
amendment of 2026-10-05, [kitsunium/platform#59](https://github.com/kitsunium/platform/pull/59)),
and every designed symbol records its documentation:

- `doc:` on every symbol — a function, a type, a method, an alias, a constant,
  a variable, a struct field, an interface method —, the comment's text as
  `go/ast`'s `CommentGroup.Text` gives it, its last newline left out;
- `blockDoc:` on each member of a parenthesized declaration that has a doc:
  the doc Go reads for a member that has none of its own;
- `comment:` for a field's and an interface method's line comment, which
  docs/api reads as the member's doc when it has none;
- a package's `doc:`, its package comment.

The design refuses a doc in any other form, and refuses a hand-written
declaration's doc in a design that does not say `docs: design`. The import
read them all from the code: `kit design import -type library` over the tree
writes 9 242 `doc:`, 788 `blockDoc:` and 77 `comment:` (3 545 before, the
generated declarations'), and run again it writes nothing.

### 2. kit gen owns the doc-comment slots, and only them

A slot is the comment group `go/ast` attaches to a designed declaration — a
function's or a method's doc, a declaration's that is no block, a block
member's and the block's own, a field's or an interface method's doc and line
comment — and a file's package comment. `kit gen` rewrites each slot whose
lines differ from the design's, in hand-written files too: a splice of the
file's bytes, a directive the slot holds (`//go:noinline`, `//ktn:…`) kept after
the doc below a blank `//`, and the result refused unless its tokens are the
original's and every comment that is no slot is still there, in order. It
refuses what it cannot write without changing what a tool reads — a bare
`//nolint`, a comment beside a line directive — rather than rewriting it.
`kit gen -check` reports a slot edited by hand as drift on its file.

The package comment lives in one file, the package's **`doc.go`**: kit writes
it there and takes it out of every other hand-written file of the package,
since go/doc joins every file's fragment. So 283 `doc.go` are new, eight that
existed hold the joined comment, and the 59 files left holding nothing but a
package clause are gone (rule 5) — content moved, never deleted.

### 3. The proof holds without kit: a doc digest in every pin marker

Each pin marker carries its symbol's doc digest, and each pin file the
package's:

```
// go:<id> <kind> <canonical signature> doc:<sha256>
// package <import path> doc:<sha256>
```

The digest is the sha256 of the doc exactly as docs/api records it — a
declaration's doc, else its block's; a member's doc, else its line comment;
with its last newline — and, for a struct, continues with each exported field
that has a doc, as NUL, the field's name, NUL, its doc. `tools/genindex
-check-api -markers` (`make api-check`, in `make lint-check`) computes the
same digest from the code on every cell and fails on a difference:

- `<id>: doc differs: the code's doc comment is not the design's …` — a doc
  edited in the code alone;
- `<package>: package comment differs …` — a package comment edited, or a
  fragment added to another file.

genindex still parses Go and hashes bytes; it never reads YAML. A design edit
without `kit gen` still fails on the header digests (ADR 0163 §3).

### 4. A doc edit is a design edit, and it needs kit (amends ADR 0163 §4)

Stated as plainly as ADR 0163 §4 stated the opposite, because it is the cost of
this decision: **from now on a doc edit is a design edit followed by `kit
gen`** — then `make api` (docs/api) and `make docs-readme` (the READMEs). A doc
edited in the Go code alone fails `make api-check` on its digest. While kit is
not public, a contributor without kit cannot land a doc change — exactly as
they cannot land an API change since ADR 0163. A body, a test, a private
symbol's comment, a comment inside a body, a note standing apart from a
declaration and a script still need no kit.

### 5. The READMEs are written from docs/api; gomarkdoc is retired (amends ADR 0008)

`tools/genindex -write-readmes` (`make docs-readme`) writes the `README.md` of
every package under `pkg/` and `framework/` that docs/api records — 116 — from
the committed docs/api, and `-check-readmes`
(`scripts/pre-commit/check-readme-drift.sh`, now in `make lint` as well as a
`bazel`-job step) fails on a README that differs, is missing, or was written
for a package docs/api no longer records. A README holds, in order: the
import, the package comment, an index, the constants and variables (each
declaration, doc, the symbol it is initialised with, an error code's dotted
quad and a sentinel's reason and public text), the functions, and the types —
an alias with the declaration of the type it names, its fields' docs and its
methods, which gomarkdoc never showed (1 067 member anchors more) —, and the
examples of the package's tests (`go/doc`'s `Examples`, the one part docs/api
does not record: they are code). Doc text is printed by `go/doc/comment`'s
Markdown printer, code blocks fenced, doc links to the page's anchors.

gomarkdoc, its pinned install in CI, `check-readme-determinism.sh` (it tested
gomarkdoc's reproducibility) and every `//go:generate gomarkdoc` line (117) are
gone. genindex is stdlib-only and needs nothing installed; its output is sorted
and carries no timestamp or absolute path, and a test writes it twice.

## As built

- `design/`: 101 surface files and `sdk.yaml` (`docs: design`, `kit.version:
  v0.1.0-rc.5`), +51 013 / −5 864 lines; the import over the tree writes
  nothing; `kit gen -check` clean; `kit check` gives no finding.
- `kit gen` (19 s on the SDK) changed nothing in a hand-written file but the
  package comment's place: every hunk of the 1 474 hand-written files it
  touched lies above the package clause — the package-comment fragments taken
  out, the `//go:generate gomarkdoc` lines with them —, verified hunk by hunk.
- The pins: 337 files, 7 528 markers, each with its doc digest, and 337 package
  markers; `make api-check` passes on the twelve cells, and fails, naming the
  symbol or the package, on a doc or a package comment edited by hand.
- docs/api: no symbol record changes. 27 package records each lose their
  per-cell variants — their comment had a fragment in a file only some cells
  build, and is now the same everywhere (`internal/service/proc/*`,
  `framework/connectors/{sqlite,ssh}`, …). Two package comments change their
  text, the three sentences ADR 0166 §Consequences named: `pkg/v1/errs` — its
  introspection is no longer "this file" and its construction surface is
  `construct.go` plus the Field helpers re-exported in `facade_gen.go` — and
  `pkg/v1/security/token` — its codes are re-exported in `facade_gen.go`, not
  `codes.go`. Both were corrected in the design, then `kit gen`.
- The READMEs: 116 rewritten by genindex; `framework/kit/testdata/cliprobe`'s,
  a `main` under `testdata/` that docs/api does not record, is written by hand.
  Compared with gomarkdoc's, every exported symbol is still there, under the
  same anchor (generic types' methods drop their type parameters from it:
  `Command.Allow`, not `Command[C, R].Allow`); the differences, each named:
  - added — an alias's owner's declaration, fields and methods; each value's
    error code, reason and public text; the symbol a re-export is initialised
    with; a doc link to a value inside a block resolves to that value (gomarkdoc
    linked every name of a block to its first one);
  - changed — a constant or variable is shown alone with its type, not inside
    the source of its `const ( … )` / `var ( … )` block; a constructor is
    listed among the functions, not under the type it returns; a source link
    names the file and no line (docs/api records no line, and a line moved by
    any edit rewrote every README); an example shows its body, not a
    synthesized `package main`; doc headings carry no `{#id}`;
  - not shown — what docs/api does not record: a declaration's initializer
    expression and a sentinel's private text (gomarkdoc printed the source),
    the line comments beside a constant (313 in `pkg/` and `framework/`, every
    one beside a doc, most of them a code's dotted quad, now shown as the
    code), the doc of a `const ( … )` / `var ( … )` block whose every member
    has its own, and gomarkdoc's "contains filtered or unexported fields".
  The docs portal reads these READMEs as before: its split on the first
  `## Constants|Variables|func |type ` heading and its removal of `## Index`
  are unchanged.

## Consequences

- One source for every doc: the design. The code, docs/api, the READMEs and
  the portal are written from it, in that order, and each step is checked
  without kit: the code by the marker digests, docs/api by `make api-check`,
  the READMEs by `check-readme-drift.sh`.
- A doc edit needs kit (§4). The owner's open decision — making kit public —
  would remove that cost, as it would ADR 0163's.
- A package comment is in `doc.go` alone; a fragment added to another file
  fails `make api-check`, and `kit gen` takes it out. ktn-linter's
  `KTN-COMMENT-PKGDOC` — a descriptive comment before `package` in every file,
  the convention every fragment came from — is excluded in `internal/`,
  `pkg/`, `framework/` and `third-party/`, the trees the design covers: there
  it asks for the opposite of this law. The role a file's fragment described
  is now a paragraph of its package's `doc.go`, where go/doc already showed it.
- `make docs-readme` and the drift gate need only the Go toolchain; CI no
  longer installs a binary for them.
- The release is a patch: no exported name, kind, value or signature changes.
  Doc text changes in two package comments (the corrections above) and the
  per-cell package comments of 27 packages become one — documentation only,
  which a patch carries.

## Breaking changes

None to the API. A contributor without kit can no longer change a doc comment
of a designed symbol, nor a package comment.

## Deferred

- A struct field's or a const's line comment that is a doc in the design but
  docs/api does not record beside the member's doc is not in the READMEs;
  recording it would change the docs/api format, whose schema kit mirrors.
- The doc of a parenthesized block whose members all have their own is the
  design's and the code's, but docs/api records it nowhere, so neither the
  READMEs nor the portal show it.

- A file no cell of the platforms builds (`exec_other.go`,
  `lookup_other.go`, `dir_other.go`: `!unix && !windows`) keeps its own
  package-comment fragment: kit owns the files the design's cells build, and
  docs/api reads no other. They are what go/doc shows on a GOOS outside the
  table only.

## Alternatives considered

- **Keep gomarkdoc** (the owner's option a): it would read the code's doc
  comments, which the digests now hold to the design, and keep its three
  costs — no alias members, an unreliable exit status, git-state-dependent
  links. Rejected by the owner.
- **Write the READMEs from the design**: genindex reads no YAML (ADR 0163
  §3), so the SDK's CI could not check them without kit. docs/api is the
  code's, and the code's docs are the design's.
- **Digest the doc text into a separate file**: a second artefact to keep in
  step; the markers are already one per symbol and per cell, and already read
  by `make api-check`.

## References

- [kitsunium/platform#59](https://github.com/kitsunium/platform/pull/59) — `project.docs: design`, the import of every doc, kit gen's slots and `doc.go`, the marker digests (kit `v0.1.0-rc.5`)
- `tools/genindex/CLAUDE.md` — `-write-readmes`, `-check-readmes`, the markers' doc digests
