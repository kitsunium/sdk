<!-- updated: 2026-10-04T08:50:00Z -->
# tools/genindex/

## Purpose

One Go program that reads the SDK's Go code for the documentation, in five
modes:

- **the symbol index** (the default) — every exported declaration of a
  module's packages, over `go/parser` and `go/doc`, as one JSON document. It
  was the docs site's ⌘K index until the site built that index from
  `docs/api` (`docs/site/scripts/gen-symbols.mjs`, no go command): go/doc does
  not follow an alias to the methods and fields of the type it names, and
  `docs/api` records them at their owner. No build step runs this mode now;
  it is kept, with its tests, until it is retired;
- **the doc-link check** (`-check-doclinks <dir>…`, ADR 0138) — no index; it
  fails on every same-package doc link a comment writes that names no symbol
  its package declares, which go/doc renders as literal bracketed text on
  pkg.go.dev, in `go doc` and in the generated READMEs. `make doclinks` runs it
  over the repository, and `make lint-check` runs that;
- **`-write-api`** (`make api`) — `docs/api/<module>.json` for every module of
  `go.work`, the SDK module and the 13 vendor modules: every exported symbol,
  internal packages included, read from the CODE with go/types on every cell of
  the platforms table, each with its go: id, kind, signature as its file spells
  it and canonically, an alias's owner, doc text, cells, module-relative file,
  codes, layer and family. `docs/api/schema.json` is the format, with the id
  and canonical-signature vectors kit's side tests too;
- **`-check-api`** (`make api-check`, in `make lint-check`) — writes nothing:
  regenerates `docs/api` in memory and fails on any byte that differs, naming
  each record added, changed or removed. `-markers` adds, per cell, the code's
  (id, kind, canonical signature) set against the `// go:<id> <kind>
  <canonical>` markers of `api_gen*_test.go` pin files; `-digests` adds every
  generated file's header digests against the design files' bytes. Both are
  tested on fixtures and not yet passed by `make api-check`: they are armed in
  the change that commits `design/` and the pins kit writes, so no commit in
  between is red;
- **`-write-error-codes`** (`make error-codes`, which `make api` runs) and
  **`-check-error-codes`** (`scripts/pre-commit/check-error-codes-drift.sh`)
  — `docs/error-codes.yaml` written from the committed `docs/api`, or compared
  with what that writes: every constant record with a code, no `init` and a
  name starting with `Code`, under its package's directory, sorted by value.
  `make api-check` holds `docs/api` to the code, so the file is the code's.

The cells come from `scripts/ci/platforms.sh` (`-platforms`, default
`../../scripts/ci/platforms.sh`, where every make recipe runs this program):
its one here-document, read without running the script.
`scripts/pre-commit/check-platforms.sh` holds that table to the cross-build
matrix of `bazel-ci.yml`.

An auxiliary module (`github.com/kitsunium/sdk/tools/genindex`), stdlib-only,
OUTSIDE `go.work`: run it with `GOWORK=off`. It still has a `BUILD.bazel`
(`genindex_lib`, the `genindex` binary, `genindex_test`), which a module with
no dependency allows; `# gazelle:exclude testdata` keeps gazelle out of the
fixture repository. The `tools/` conventions are in `tools/CLAUDE.md`.

## Contents

| File | Holds |
|---|---|
| `main.go` | `main`, `runIndex` (the index mode), `buildIndex` (the stamped `generatedAt`), `defaultRepoRoot` (two levels above `-input`, or above the working directory), `writeIndex` (stdout, or the file and its directory created — a failed close is a failed write), `exitErr` |
| `cli.go` | the flags (`-input`, `-output`, `-url-base`, `-module`, `-repo-root`, `-source-url-prefix`, `-platforms`, `-check-doclinks`, `-write-api`, `-check-api`, `-markers`, `-digests`, `-write-error-codes`, `-check-error-codes`), `mode`, `usage` (one mode at a time; `-markers` and `-digests` need `-check-api`), `runMode`, `runAPIMode`, `runErrorCodesMode` (no platforms table: `docs/api` names its cells) |
| `platforms.go` | `platform` (`String`, `matches` — the go command's `build.Context.MatchFile`, cgo off), `readPlatforms` / `parsePlatforms` (the cells between `cat <<'CELLS'` and `CELLS`; no table, a table never closed, an empty one, a malformed or repeated cell refused by name; a carriage return ignored), `cellNames` |
| `options.go` | `indexOptions`, the parameters every stage of the index's walk takes together |
| `collect.go` | `collect` (the index's walk under the module root), `skipDir` (dot directories, `vendor`, `testdata`, `node_modules`), `loadDir` / `packageSymbols`, `importPathOf`, `relPath`; `_test.go` files parsed so `Example` functions attach, their declarations never indexed |
| `emit.go` | `emit` — one documented package projected into index rows: funcs, types, methods, consts and vars; `typeRows` — a type, the functions go/doc files under it, its methods, its consts and vars; `packageLabel`, `packageURL` |
| `render.go` | a declaration rendered into an index row's fields: `renderDecl` / `signatureOf` (one line), `synopsis` (the first sentence), `isDeprecated`, `exampleNames`, `sourceURL` / `sourceLinkOf` |
| `index.go` | the index document: `schemaVersion` (1; the docs site's own index, from `docs/api`, is schema 2), `index` (`schema`, `generatedAt`, `module`, `symbols`), `symbol` (short field names: the file shipped gzipped on every page view) |
| `doclinks.go` | the check: `runDocLinkCheck` (exit 0, or 1 on a dead link, an unreadable package or no directory given), `checkDocLinks` / `deadLinksInDir` / `deadLinksInPackage`, judged on the cells given, `sameScopeLinks`, `docComments`, `docLinkAdvice` (write `[Type].Member` for a member of an aliased type) |
| `api.go` | the API modes: `apiOptions`, `realRoot` (symbolic links resolved, as go list reports directories), `buildAPI` (modules, every cell listed at once, one cell checked at a time), `apiBuilder` (`readCell`, `readPackage`, `noteDirs`, `result`), `packageRecord`, `runWriteAPI` / `writeDocument` / `removeStale` (a module that is gone takes its document with it), `existingDocuments` |
| `apicheck.go` | `runCheckAPI`, `documentFindings` / `documentDrift` (missing, differing or stale documents), `symbolDrift` (each record added, changed or removed, by id and cells, bounded), `surfaceFindings` (the markers and the digests, when asked) |
| `apiload.go` | the loader: `goCommand` (the pinned environment: `GOOS`, `GOARCH`, `GOWORK` — the root's `go.work`, or off —, `GOFLAGS=-mod=readonly`, `CGO_ENABLED=0`, `GOTOOLCHAIN=local`, every build knob at its default, `PWD`), `modules` (`go list -m -json`), `list` (`go list -e -json=… -deps <module>/...` per cell), `fileCache` (each file parsed once, kept while a cell still to be checked compiles it), `checkCell` / `checkPackage` (go/types from source, `IgnoreFuncBodies`, the cell's `Sizes`, the module's go line), `cellImporter` |
| `apisym.go` | one package's records on one cell: `apiSymbol`, `apiCode`, `apiField`, `pkgReader` (`readSymbols`, the file's `qualifier`, `funcDecl`, `valueSpec`, `constSymbol`, `varSymbol`, `initID`, `resolveCodes`, `defineCode`), `pathQualifier`, `isDefine`, `isErrsCode`, `dottedQuad`, `relSlash` |
| `apisym_types.go` | types, aliases, interfaces' exported methods and structs' exported fields: `typeSpec`, `aliasSymbol` (its owner), `typeSymbol`, `interfaceMethods`, `fields`, `exportedOnly`, `typeParamList`, `memberDocs`, `specDoc` |
| `apisym_place.go` | `layerRules` and `placeOf`: a package's layer by directory and its family within it, `root` for the kernel's and pkg/v1's root packages |
| `apiid.go` | the go: id: `goID` (`String`, `parseGoID`, `goIDPattern` — the grammar platform's design calls goid), `escapePath` (`%2e`), `objectID` / `funcID` / `methodRecv` — the names platform's `loader.ObjectID` gives |
| `apidoc.go` | the document: `apiDocument`, `apiPackage`, `builtAPI`, `packageDir`, `variants` (records merged across cells: one per distinct form, with its cells), `moduleAPI.document`, `apiSymbolOrder`, `encodeDocument`, `documentName` |
| `markers.go` | the pin markers: `readPins` / `readPin` / `parseMarker`, `cellMarkers` (the pin files a cell compiles, an id declared twice), `markerFindings` / `compareCell` / `judgeSymbol` (undeclared, missing, kind, constraint, tag, signature, receiver, foreign), `signatureFinding`, `splitTypeParams`, `findingCells` (each finding once, with its cells) |
| `errcodes.go` | `docs/error-codes.yaml`: `errorCode` (`dotted`, `hex`), `parseQuad`, `errorCodes` / `documentCodes` / `declaresCode` (the rule, each constant once whatever its cells, one read with two values refused), `compareErrorCodes`, `packageDirs` (module directory joined with the package's), `encodeErrorCodes` (the header and four lines per entry), `readDocuments` / `readDocument` (strict: an unknown member or another format refused), `runWriteErrorCodes`, `runCheckErrorCodes`, `errorCodesDrift` / `entryKeys` (each entry listed and not declared, or declared and not listed, bounded) |
| `digests.go` | the generated files: `kitHeader` (`// Code generated by kit from <design file> (sha256 <hex>, body <hex>). DO NOT EDIT.`), `digestFindings`, `generatedFiles` (a pin or port file by name, or any file with the header), `checkGenerated`, `checkDesignDigest`, `sha256Hex`, `readFirstLine` |
| `main_test.go`, `doclinks_test.go` | the index's rendering and walk on fixtures, the functions filed under a type among them (`Test_emit_typeFuncs`); the check per platform — illumos and solaris judged apart —, its errors, its ordering and its positions; `Test_platforms`, the repository's table read as genindex reads it |
| `platforms_test.go` | the table's reader, every refusal by name |
| `apivectors_test.go` | `Test_goIDVectors` and `Test_canonicalVectors`: the writer held to `docs/api/schema.json`'s vectors |
| `apisym_test.go`, `apidoc_test.go`, `markers_test.go`, `digests_test.go` | codes, re-exports, docs, places; the merge across cells, the order, the bytes, the drift report; every marker finding and every digest finding by name |
| `api_e2e_test.go` | `-write-api` and `-check-api` through the go command on `testdata/apirepo`: the document, the doc edits a check catches, and `TestCheckAPIMarkers` and `TestCheckAPIDigests` — every case reported by name |
| `api_census_test.go` | `Test_apiCensus` (an independent go/parser and go/build census of every module of `go.work` against `docs/api`'s counts) and `Test_apiCodes` (`docs/api`'s codes against `docs/error-codes.yaml`, the errs masks named apart, every sentinel complete and listed) |
| `errcodes_test.go` | the rule, the refusals and the bytes on fixture documents; `-write-error-codes` then `-check-error-codes` on a staged repository, each drift named; `Test_errorCodesCensus` — the repository's `docs/error-codes.yaml` against the sources read with go/parser alone (a `Code…` constant of `errs.Code`, written or carried down an `iota` group, that re-exports nothing; a hex literal's value too) |
| `testdata/apirepo/` | a repository of its own, `example.com/fixture`, with a two-cell platforms table |

## Rules

- **A partial index is worse than none.** Every failure is fatal and printed on
  stderr: a docs site shipping half the API would show a search box that cannot
  find what is missing, and nothing would look broken. A partial `docs/api` is
  worse still, since `make api-check` vouches for it.
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
- **The platforms are one table, all twelve cells.** genindex reads them from
  `scripts/ci/platforms.sh` — a data dependency under Bazel, exported by the
  root package — and `scripts/pre-commit/check-platforms.sh` fails `make lint`
  and the `bazel` job until that table equals `bazel-ci.yml`'s cross-build
  matrix, cells and order. illumos and solaris are two cells: the go command
  compiles a `_solaris.go` file and the `solaris` tag for both, and an
  `_illumos.go` file for illumos alone.
- **Same-package links only.** A link qualified by another package resolves
  through imports go/doc cannot see from here; the package it names judges it.
- **Comments are collected before go/doc runs**, and the package is documented
  with `doc.AllDecls|doc.PreserveAST`: without `AllDecls` go/doc drops every
  unexported declaration and its comment, and a first version lost eleven dead
  links that way.
- **docs/api is read from the code, on every cell, from source.** One
  `go list -deps` per cell, in parallel, names the cell's whole universe —
  standard library and vendor dependencies included —, and go/types checks
  every package of it from source with function bodies ignored, one cell at a
  time: no compiler export data, so no build cache, and a cold run costs what a
  warm one does. A record that differs between two cells — `group.Unlimited` is
  `math.MaxInt`, so 2147483647 on linux/386 and linux/arm — is one record per
  form, each with its cells; a record of every cell names none. Checking only
  the packages whose files differ per cell would have written the 64-bit value
  for the 32-bit cells.
- **The canonical signature is what go/types prints**, every package qualified
  by its import path, the declaring one included, aliases kept — with one
  exception: a defined struct keeps its exported fields only, because an
  unexported field is not part of the surface a design declares. An interface
  keeps every method, unexported ones included: that is how a sealed interface
  is told apart. These are the strings the pin markers carry; the schema's
  vectors pin them, type parameters grouped as go/types groups them.
- **The output is the same bytes on every machine.** Records sorted, paths
  module-relative with `/`, no absolute path, no timestamp, the environment of
  every go command pinned — a tag, an experiment or a microarchitecture level a
  caller exported would change which files a cell compiles. `make api` twice
  writes identical bytes.
- **Codes are read from the code.** An `errs.Code` constant records its value
  as a dotted quad; a variable initialised with `errs.Define` — under any import
  name — records the sentinel's code, reason and public text; a variable
  initialised with a sentinel inherits its code through any chain of
  re-exports, and names it in `init`. `docs/error-codes.yaml` is written from
  these records (`-write-error-codes`): every constant with a code, no `init`
  and a name starting with `Code` — the errs package's six meta-codes
  included, its four masks, of the code type and no code, left out. It used to
  be a grep over the sources, which listed two codes a doc comment wrote and
  none of the meta-codes `iota` declares; `Test_errorCodesCensus` now holds the
  file to the sources read another way.
- **Errors are plain `fmt.Errorf`.** `tools/` sits outside rule 2's scope
  (`internal/`, `pkg/`, `third-party/`, `framework/`); nothing imports this
  program, and every error ends the process.

## Attention points

- Until the platforms table gained `illumos/amd64` and `solaris/amd64`
  (ADR 0144), a doc comment in a file only those two compile was never judged:
  four production files then (`flock_other.go`, `lock/nofollow_other.go`,
  `childwait/waitany_solaris.go`, `reaper/timersweep_solaris.go`), all clean.
  A cell added to the lane fails `check-platforms.sh` until the table gains it,
  and the check and docs/api judge it from then on with no other edit.
- **Two groups of tests skip under Bazel**, which stages this package's data
  alone: the end-to-end tests (`api_e2e_test.go`, which run the go command
  over the fixture repository under `testdata/`, never staged) and the census
  and codes tests (`api_census_test.go`, which read the whole repository).
  Each skips on what it reads being absent (`needFixture`,
  `needRepository`), never on the PATH alone: GitHub's ubuntu runner puts a
  `go` on a Bazel test's PATH and a Mac with Homebrew's go does not, and the
  first push of these tests, guarded by `needGo` alone, failed the `bazel` job
  there on a fixture it could not stage. `go test` runs them — CI's `test-386`
  job and e2e-cross over every module of the census, and any local run — which
  is their gate (rule 12). The vector tests and every unit test run in both.
- `make api-check` needs the workspace's modules in the module cache: a first
  run downloads the vendor modules' dependencies. It costs about 11 s warm on
  an M1 Pro (`make lint-check` 4 s before, 15 s after; peak 1.3 GB, the vendor
  dependencies' per-platform files dropped once no cell left needs them).
- The docs site reads `docs/api` — the API section of every package page and
  the ⌘K index, with the members of `pkg/v1`'s aliases this program's index
  could not see (1,967 entries from go/doc, 2,777 from `docs/api` at
  `79e3ba7d`). A change to `docs/api/schema.json` changes the site's reader,
  `docs/site/scripts/lib/api.mjs`, in the same commit: it reads `sdk.api/v1`
  and refuses any other format by name.

## Verify

```sh
cd tools/genindex && GOWORK=off go vet ./... && GOWORK=off go test ./...
make doclinks              # the check over the whole repository
make api && git diff --exit-code docs/api   # nothing to write on a clean tree
make api-check             # what lint-check runs
bazel test //tools/genindex:genindex_test
```
