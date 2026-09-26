# ADR 0138 — A doc link resolves or it is not written, and a facade links the alias

- **Status**: Accepted; implemented in `tools/genindex` (`-check-doclinks`) and run by `make doclinks`, which `make lint-check` — a CI gate — invokes.
- **Date**: 2026-09-26
- **Deciders**: kitsunium maintainers
- **Related**: [ADR 0008](0008-readme-from-code-generation.md) (READMEs are generated from the doc comments this checks), [ADR 0074](0074-what-a-public-alias-may-point-at.md) (the facade aliases its types, which is the cause), [ADR 0088](0088-a-suite-nothing-runs-is-not-a-test-suite.md) (a gate is a name CI says out loud)

## Context

A Go doc comment links a symbol by naming it in square brackets. When the name resolves, `go doc`, pkg.go.dev, gopls and gomarkdoc render a link; when it does not, they render the brackets and the name as literal text — `\[Broker.Publish\]` in a generated README — and nothing fails: not the compiler, not `go vet`, not the linter, not any gate ([#241](https://github.com/kitsunium/sdk/issues/241)).

Measured on this repository with `go/doc`'s own resolution (`doc.Package.Parser().LookupSym`) against a permissive parse of every doc comment: **152 same-package doc links named no symbol their package declares**, 131 of them in `pkg/v1` — the surface pkg.go.dev renders. Two classes, of very different nature:

1. **The facade, 129.** Every one has the shape `[Type.Member]` where `Type` *is* declared in the package — as an alias. `pkg/v1/<domain>` re-exports its types (`type Broker = corequeue.Broker`, ADR 0074), and `go/doc` collects methods and fields from the declarations of the package it documents. An alias declares none, so `[Broker]` resolves and `[Broker.Publish]` does not. This is not an editing mistake: it is a structural consequence of the re-export layer, and it recurs on every new domain unless something checks it.
2. **Wrong names, 23.** A name that lives in another package (`[LockBackendFailed]` in `internal/service/lock`, which is `corelock.LockBackendFailed`), a method written without its receiver (`[Wait]` for `Group.Wait`), a field without its type (`[Path]` for `Spec.Path`), and byte layouts written in brackets whose first word is capitalised (`[Version][algID][nonce]`), which the parser reads as a link. Eleven of them sit in doc comments of UNEXPORTED declarations, which `go doc -u` and gopls render: a first version of the check missed all eleven, because building a `doc.Package` without `doc.AllDecls` removes unexported declarations from the AST it is given — `doc.PreserveAST` does not prevent it — and their comments with them. The check now collects the comments first.

Lowercase names in brackets (`[checkDir]`) are never links — a doc link's name must start with an upper-case letter — and they stay a house convention, out of scope.

## Decision

1. **A same-package doc link must resolve.** `tools/genindex -check-doclinks <dir>…` walks every package (production files only, `testdata`/`vendor`/`node_modules`/dot directories pruned), builds each package's symbol table with `go/doc` exactly as `go doc` does, parses every doc comment — package clause, declarations, specs, struct fields and interface methods, exported or not — and fails on every `[Name]`, `[Type.Member]` or `[*Type.Member]` that does not resolve, at the line that writes it. A package that does not parse fails the check; "nothing checked" is not "nothing wrong".
2. **A facade links the alias, and names the member after it: `[Type].Member`.** It reads the same as the link it replaces, it resolves, and on pkg.go.dev it lands on the alias, whose declaration names the aliased type where the member is documented.
3. **A wrong name is corrected to the name that resolves** — qualified through the file's own imports (`[corelock.LockBackendFailed]`, `[coreproc.Spec.Path]`), given its receiver (`[Group.Wait]`), or taken out of brackets when it was never meant as a link (a byte layout, a symbol in a package this one may not import).
4. **It is a gate.** `make doclinks` runs the check over the repository; `make lint-check`, which `scripts/ci-gates-check.sh` lists and the required `bazel` job runs, invokes it, and so does `make lint`. It needs the Go toolchain and nothing else — no network, no Bazel.
5. **In `tools/genindex`, not a new tool.** genindex already walks the repository's packages through `go/doc` to build the docs site's symbol index, it is stdlib-only and outside `go.work` (`tools/CLAUDE.md`), and a new module would add a ninth `go.mod` for one function.

Qualified links (`[pkg.Name]`) are not judged: they resolve through the file's imports into a package this walk does not load, and can only be judged by the package they name.

## Consequences / Semantics

- All 152 are fixed in this change set, so the gate starts green: 129 facade links rewritten to `[Type].Member`, 23 wrong names corrected. 27 generated `pkg/v1` READMEs change accordingly — where they printed `\[Delivery.Deliveries\]` they now link `[Delivery](<#Delivery>).Deliveries`.
- A new domain's facade that documents `[Type.Method]` over an alias fails `make lint-check` with the file, the line and the fix; the report ends with the rule in one line.
- Branches written before this lands may carry new dead links; they fail the gate once they merge `main`, with the same message.

## Breaking changes

None. Doc comments and generated READMEs only; no symbol, signature or behaviour changes.

## Alternatives considered

### Why not qualify every facade link, `[corequeue.Broker.Publish]`

It resolves — and renders `corequeue.Broker.Publish` on pkg.go.dev, naming an `internal/` import alias a consumer cannot import, in the one surface written for consumers. `[Broker].Publish` keeps the facade's own vocabulary.

### Why not drop the brackets and write `Broker.Publish` as plain text

It loses the link that `[Broker]` still provides, for no gain in accuracy.

### Why not a URL link definition per member

`[Broker.Publish]: https://pkg.go.dev/…#Broker.Publish` would render the text as a link to the internal package's page — 129 hand-maintained URLs pinned to paths and versions, each able to rot without failing anything, which is the defect this ADR removes.

### Why not declare methods on the facade instead of aliasing

It is the re-export shape ADR 0074 decided and the reason `pkg/v1` costs nothing at run time; a wrapper type per domain to make links resolve would be the tail wagging the dog.

### Why not a Go test in a module instead of a genindex mode

A test sees its own module's files; the repository has eight modules (ADR 0137) and the dead links were spread across five of them. A walk over the tree, run by the lint gate, covers all eight in one pass.

## Deferred

- **Qualified links are not verified** to name a symbol that exists in the package they point at. It would mean loading every imported package; none was found broken in this sweep, and the same-package class is where every measured defect was.
- **Bracketed lowercase names** (`[checkDir]` and 47 like it in `internal/service/lock`) remain plain text by the language's rule. A stale one is invisible in every direction; checking them would need a convention first.

## References

- `tools/genindex/doclinks.go`, `tools/genindex/doclinks_test.go` — the check and its table tests
- `make doclinks`, `make lint-check` — where it runs
- `go/doc/comment` — the doc-link grammar: a name must start with an upper-case letter; headings carry no links
- Issue [#241](https://github.com/kitsunium/sdk/issues/241) — the measurement, and the policy question it asked to settle with an ADR
