<!-- updated: 2026-10-03T18:53:00Z -->
# tools/genindex/

## Purpose

One Go program with two modes over `go/parser` and `go/doc`:

- **the symbol index** — every exported declaration of a module's packages as
  one JSON document, which the docs site's search modal loads
  (`docs/site/scripts/gen-symbols.mjs` runs it at `npm run prebuild` and writes
  `public/_search/symbols-<major>.json`);
- **the doc-link check** (`-check-doclinks <dir>…`, ADR 0138) — no index; it
  fails on every same-package doc link a comment writes that names no symbol
  its package declares, which go/doc renders as literal bracketed text on
  pkg.go.dev, in `go doc` and in the generated READMEs. `make doclinks` runs it
  over the repository, and `make lint-check` runs that.

An auxiliary module (`github.com/kitsunium/sdk/tools/genindex`), stdlib-only,
OUTSIDE `go.work`: run it with `GOWORK=off`. It still has a `BUILD.bazel`
(`genindex_lib`, the `genindex` binary, `genindex_test`), which a module with
no dependency allows. The `tools/` conventions are in `tools/CLAUDE.md`.

## Contents

| File | Holds |
|---|---|
| `main.go` | the flags (`-input`, `-output` — empty or `-` is stdout —, `-url-base`, `-module`, `-repo-root`, defaulting to two levels above `-input`, `-source-url-prefix`, `-check-doclinks`), `buildIndex` (the stamped `generatedAt`), `writeIndex` (stdout, or the file and its directory created — a failed close is a failed write), `exitErr` |
| `options.go` | `indexOptions`, the parameters every stage of the walk takes together |
| `collect.go` | `collect` (the walk under the module root), `skipDir` (dot directories, `vendor`, `testdata`, `node_modules`), `loadDir` / `packageSymbols`, `importPathOf`, `relPath`; `_test.go` files parsed so `Example` functions attach, their declarations never indexed |
| `emit.go` | `emit` — one documented package projected into rows: funcs, types, methods, consts and vars; `typeRows` — a type, the functions go/doc files under it, its methods, its consts and vars; `packageLabel`, `packageURL` |
| `render.go` | a declaration rendered into a row's fields: `renderDecl` / `signatureOf` (one line), `synopsis` (the first sentence), `isDeprecated`, `exampleNames`, `sourceURL` / `sourceLinkOf` |
| `index.go` | the JSON document: `schemaVersion` (1 — the docs site pins it), `index` (`schema`, `generatedAt`, `module`, `symbols`), `symbol` (short field names: the file ships gzipped on every page view) |
| `doclinks.go` | the check: `runDocLinkCheck` (exit 0, or 1 on a dead link, an unreadable package or no directory given), `checkDocLinks` / `deadLinksInDir` / `deadLinksInPackage`, the `platforms` table (the twelve cells of `bazel-ci.yml`'s `cross-build`, in its order), `sameScopeLinks`, `docComments`, `docLinkAdvice` (write `[Type].Member` for a member of an aliased type) |
| `main_test.go`, `doclinks_test.go` | the index's rendering and walk on fixtures, the functions filed under a type among them (`Test_emit_typeFuncs`); the check per platform — illumos and solaris judged apart —, its errors, its ordering and its positions; `Test_platforms`, the `platforms` table against the cells `bazel-ci.yml` lists |

## Rules

- **A partial index is worse than none.** Every failure is fatal and printed on
  stderr: a docs site shipping half the API would show a search box that cannot
  find what is missing, and nothing would look broken.
- **A function go/doc files under a type is still a function.** go/doc puts a
  function whose only result type of the package is `T` — `T`, `*T` or `[]T`,
  beside any predeclared or imported result such as an `error` — in that type's
  `Funcs`, not the package's: every constructor, and in `pkg/v1` every function
  returning a facade alias. Each gets the row of a package-level function, once,
  anchored by its own name, which is how gomarkdoc anchors it beneath the type.
  Reading the package's `Funcs` alone kept 303 of `pkg/v1`'s 444 functions out of
  the index (1,664 rows before, 1,967 after, at `1896ad23`).
- **A link is judged per platform.** A package declares different symbols on
  different targets, so a comment is checked against what its package declares
  on each platform that compiles the file holding it, with cgo off as every lane
  builds — never against the union of every file.
- **The platforms are the cross-build lane's cells, all twelve.** `Test_platforms`
  reads them from `.github/workflows/bazel-ci.yml` itself — a data dependency
  under Bazel, exported by the root package — and fails until the table equals
  them, in the workflow's order. illumos and solaris are two cells: the go
  command compiles a `_solaris.go` file and the `solaris` tag for both, and an
  `_illumos.go` file for illumos alone.
- **Same-package links only.** A link qualified by another package resolves
  through imports go/doc cannot see from here; the package it names judges it.
- **Comments are collected before go/doc runs**, and the package is documented
  with `doc.AllDecls|doc.PreserveAST`: without `AllDecls` go/doc drops every
  unexported declaration and its comment, and a first version lost eleven dead
  links that way.
- **Errors are plain `fmt.Errorf`.** `tools/` sits outside rule 2's scope
  (`internal/`, `pkg/`, `third-party/`, `framework/`); nothing imports this
  program, and every error ends the process.

## Attention points

- Until the `platforms` table gained `illumos/amd64` and `solaris/amd64`
  (ADR 0144), a doc comment in a file only those two compile was never judged:
  four production files then (`flock_other.go`, `lock/nofollow_other.go`,
  `childwait/waitany_solaris.go`, `reaper/timersweep_solaris.go`), all clean.
  A cell added to the lane fails `Test_platforms` before it can open that gap
  again.
- The usage example in `main.go`'s package comment names an `-output` under
  `src/data/`; the docs build writes `public/_search/symbols-<major>.json`.

## Verify

```sh
cd tools/genindex && GOWORK=off go vet ./... && GOWORK=off go test ./...
make doclinks              # the check over the whole repository
bazel test //tools/genindex:genindex_test
```
