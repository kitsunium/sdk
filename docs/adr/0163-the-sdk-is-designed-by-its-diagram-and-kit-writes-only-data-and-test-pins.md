# ADR 0163 — the SDK is designed by its diagram, and kit writes only data and test pins

- **Status**: Accepted
- **Date**: 2026-10-04
- **Deciders**: SDK maintainers (the owner's decision of 2026-10-03: "everything must be managed by the diagrams")
- **Amended by**: [ADR 0164](0164-an-error-code-is-the-designs-and-kit-writes-it.md) — §2 and §8: the codes, the sentinels and the range table are the design's, and kit writes them (`codes_gen.go`, `codes_gen_test.go`); [ADR 0166](0166-a-facade-is-the-designs-and-a-forwarders-form-is-measured.md) — §1, §2 and §10: `pkg/v1`'s re-exports are the design's `facade:`, kit writes them (`facade_gen.go`), and `errs.ReasonOf`'s variable is an inline budget's measured verdict rather than an exception
- **Amends**: none — it records what `docs/api`, `make api-check` and the docs portal became, and the rule that binds them
- **Related**: [ADR 0005](0005-sdk-error-codes-dotted-quad.md) / [ADR 0006](0006-sdk-error-code-registry-extension.md) (the codes `docs/error-codes.yaml` lists), [ADR 0008](0008-readme-from-code-generation.md) (READMEs stay gomarkdoc's), [ADR 0035](0035-pp-range-ownership-enforcement.md) (`codeRangeOwners`, the authority on ranges), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (a port grows by a sibling, generated or not), [ADR 0040](0040-changing-a-published-shape-while-v0.md), [ADR 0052](0052-sdk-lock-domain.md) (the domain whose ports are generated first), [ADR 0068](0068-layer-firewall-is-a-checked-graph.md), [ADR 0094](0094-a-test-compiles-where-its-package-does.md) (the cross-build compiles tests), [ADR 0137](0137-a-lane-that-loops-over-modules-reads-the-census.md), [ADR 0138](0138-a-doc-link-resolves-or-it-is-not-written.md), [ADR 0144](0144-illumos-and-solaris-supervise-on-their-own-kernels-word.md) (twelve cells), [ADR 0154](0154-the-sdks-principles-are-one-charter-and-an-incidents-rule-lives-with-its-code.md) (principles 26 to 28), [ADR 0155](0155-every-layer-groups-its-packages-by-family-and-a-path-may-move-while-v0.md) (the families), [ADR 0160](0160-every-service-has-a-core-and-a-code-keeps-its-value-when-it-moves.md) (where a code is declared), [ADR 0162](0162-the-sdk-is-one-module-and-a-release-is-one-tag.md) (one SDK module, thirteen vendor modules)

## Context

Until this record the SDK's API was whatever its code declared, and three
documents described it after the fact: the READMEs gomarkdoc renders from doc
comments (ADR 0008), a ⌘K index go/doc built — blind to the methods and
fields of `pkg/v1`'s 480 aliases, which go/doc does not follow — and
`docs/error-codes.yaml`, a grep over the sources.

The owner decided that the SDK is designed by its diagram: every exported
symbol of every module — the SDK module and the thirteen vendor modules
(ADR 0162) — is declared, with its inputs and outputs, in a design that is
edited first; the code follows it, and kit, the owner's construction tool,
turns the design into what holds the code to it. kit is not public. The SDK
is, and its CI must not need a tool its contributors cannot run. Charter
principle 26 says what such a rule needs:

> **A rule nothing checks is a suggestion.** Enforcement runs at build time,
> over source, in a gate CI names: a suite nothing runs is not a suite, a lane
> that loops over modules reads the census, a doc link resolves or is not
> written.

So the design is the law only if the SDK's own gates, with no kit, fail on
any difference between it and the code. The scale, measured at this record's
commit: 14 modules, 367 packages, 7,510 exported symbols in 7,528 records — a
symbol whose form differs between platforms is one record per form, as
`concur/group.Unlimited` is `math.MaxInt` and so `2147483647` on linux/386
and linux/arm — and 739 error codes.

## Decision

### 1. `design/` is the law of the exported surface, and of the structure

`design/` holds the SDK's design in kit's format (`kit.kitsunium/design/v2`):

- **`design/sdk.yaml`, the project file** — the structure and the rules
  (§7): `project` (`type: library`, the module, `go.work`, `surface:
  closed`); `platforms`, read from `scripts/ci/platforms.sh` (§9);
  `auxiliary` (`e2e`, `tools/genindex`, `tools/sdkguard`: outside `go.work`,
  outside the layers and the surface); the six `layers` in their direction
  (ADR 0068, 0147, 0157) — the kernel `stdlibOnly` and `generic` (rule 1), the
  service mirroring the core (ADR 0160), `pkg/v1` a facade (ADR 0074); the
  `families` (§11); `codes` (§8); `stdlib` (ADR 0156); `allocLane`
  (rule 12); the `budgets` — five kernel accessors measured to inline, three
  allocation claims the alloc lane holds; and the `exceptions` (§10).
- **101 surface files, one per domain or package group**, each package in
  exactly one: one per core domain path, with that path's core, service and
  `pkg/v1` packages (`design/app/lock.yaml` holds `internal/core/app/lock`,
  `internal/service/app/lock` and `pkg/v1/app/lock`); one per kernel root
  package or family, with the `pkg/v1` packages that alias it; one per
  framework package group, connector and vendor module; one for
  `observe/internal/otlp`, the only service package with no core.

A surface file records every exported symbol of its packages:

- a function's parameters and results, with their names and their types as
  the package spells them, its type parameters with their constraints, and
  whether it is variadic;
- a defined type's underlying type — a struct's exported fields with their
  types and tags, an interface's methods and embeds —, a method's receiver,
  pointer or value, an alias's target;
- a constant's type and exact value — the 739 codes among them, each a
  constant of its package with its value, so there is no table of codes —,
  and a variable's type;
- per package, the imports its signatures name; per symbol, the build
  constraint when it is not universal (`concur/group.Unlimited` is declared
  twice, under `!(386 || arm)` and `386 || arm`).

Doc text is recorded for ports alone (§2): every other doc stays a Go
comment. Ids are the package-scoped `go:` ids of §5, derived, never written.
The design was imported once from the code, with `kit design import -type
library -ports internal/core/app/lock`, then once more with `-ports
internal/core/...` to make every core interface a port (§2); re-importing it
over itself writes nothing, and from now on it is edited first.

### 2. What kit writes — and nothing else

`kit gen` reads `design/` and writes two kinds of file, each under a header
that names its design file and two digests:

```
// Code generated by kit from design/app/lock.yaml (sha256 <the design file's bytes>, body <the bytes after this line>). DO NOT EDIT.
```

- **The pins** — `api_gen_test.go` in every package that has a designed
  symbol, and `api_gen_<n>_test.go` for each other build constraint its files
  carry (one file per distinct constraint, numbered in the order of the
  sorted constraints; never a GOOS file suffix). Each is an external test
  package that imports only what the package's own files import, so the layer
  firewall, `go mod tidy` and gazelle see no new edge. Each pin follows a
  marker `// go:<id> <kind> <canonical signature>` — the strings `docs/api`
  records (§5) — and makes the compiler check the symbol against the design:
  - a function by value, a generic one instantiated inside a generic function;
  - a method through its method expression;
  - an interface both ways — its designed method set assigned to it and it to
    that set —, one way for a sealed interface, whose unexported method the
    markers carry;
  - a type's underlying type through a constrained generic helper, a struct's
    exported fields by address, an alias by pointer;
  - a constant's exact value through a map literal whose two `bool` keys
    collide when the value differs, and its type through its address;
  - a variable by address — or, when its package cannot spell its type, held
    equal to a variable of that type it can (282 sentinels);
  - the existence alone of a type with nothing else to pin, or whose
    signature its package's own files cannot spell (95: structs that export
    no field, options over an unexported configuration, and
    `security/redact.Redactor`, a port whose `Attrs` names a package only its
    `design_gen.go` imports), whose marker still carries its whole signature
    for `make api-check`.
- **The ports** — `design_gen.go`, the interfaces a design file declares under
  `ports:`, with their names, signatures, embeds, type parameters and doc
  comments as the design writes them; no package comment, no suffix, no
  forced `ctx` or `error`. Every exported interface of `internal/core` is
  one: 106 interfaces with 269 methods in 33 packages, the nine generic ones
  and the twelve that embed another among them. The first were
  `internal/core/app/lock`'s `Locker`, `Lease` and `Deadliner`: they moved out
  of `lock.go` and `lease.go` into the design, doc comments included, and
  `lease.go`, left empty, is gone (rule 5); the engines still assert them in
  `internal/service/app/lock/lock_compliance.go`. Every other core interface
  followed in a second step, moved the same way — content moved, never
  deleted. A file the move left with nothing but its package comment is gone
  as well, its comment joined to its neighbour's in file-name order, the order
  go/doc joins them in, so every core package's `go doc` reads as it did; a
  file holding its package's own documentation stays, as `lock.go` does. One
  difference the dialect makes: `app/view`'s `Factory` and `Renderer` put a
  blank line between their two methods, which the design does not record.

Nothing else in production code is generated: concrete types, constants,
sentinels and every body stay hand-written, held by the pins. Nothing kit
writes runs: the pins are tests, the design and `docs/api` are data, and a
port is an interface declaration, which compiles to no instruction — the
symbols of a consumer's binary are the same before and after (§As built).
That is why kit "writes only data and test pins": the one file it writes into
a production package holds types and nothing that executes.

### 3. What the SDK's CI checks, with no kit

- **The compiler, through the pins**: a symbol removed or renamed, a
  parameter or result changed, a constant's value or type, a variable's type
  or a variable turned constant or function, an exported field, a value
  receiver turned pointer, an interface method added or removed, a
  constraint narrowed, an alias turned defined type. The pins compile in the
  Bazel race suite (gazelle gives them their test targets), in the
  cross-build's `go vet` on the twelve cells (tests compiled, ADR 0094) and in
  the linux/386 test lane.
- **`make api-check`**, in `make lint-check` and named in GATES
  (`tools/genindex -check-api -markers -digests`):
  - `docs/api` regenerated from the code in memory and compared byte for byte
    with what is committed, each record added, changed or removed named — a
    doc edit without `make api` included;
  - `-markers`: on every cell, the code's exported (id, kind, canonical
    signature) set equals the pins' markers — a symbol added in the code
    alone, a function turned variable, a constraint widened, a struct tag, a
    pointer receiver turned value, a sealed interface's unexported method;
  - `-digests`: every generated file's header names a design file whose bytes
    hash to the header's sha256, and its body hashes to the body digest — a
    design edit without `kit gen`, a generated file edited by hand.

genindex parses Go and hashes bytes; it never reads YAML. The design files
are read by kit alone, so the SDK's CI holds the code to them through their
bytes and through what kit wrote from them, which is how a public repository
is governed by a private tool without depending on it.

### 4. An API change needs kit; a doc edit does not

Stated plainly, because it is the cost of this decision:

- **An API change needs kit**, which is not public: edit the design file of
  the domain (`design/<family>/<domain>.yaml`), run `kit gen` (the pins, the
  ports), make the code follow, then `make api` (`docs/api`,
  `docs/error-codes.yaml`, gazelle for new test targets). A change of the API
  made in the code alone turns `make api-check` red — the markers name the
  symbol — and a design edit without `kit gen` turns it red on the digest.
  Until the owner regenerates, a contributor without kit cannot land an API
  change.
- **A doc edit needs no kit**: it is a Go comment, and `make api` rewrites
  `docs/api` with the SDK's own tool. The one exception is a port's doc
  comment, which lives in the design because it lives in a generated file.
- A body, a test, a private symbol, a script: no kit.

Making kit public would remove the first point; it is the owner's open
decision, outside this record.

### 5. `docs/api`: the format, the writer, the canonical signature

`docs/api/<module>.json`, one document per module of `go.work`
(`docs/api/sdk.json`, `docs/api/sdk/third-party/aws.json`, …), format
`sdk.api/v1`, described by `docs/api/schema.json`, which carries the `go:` id
vectors and the canonical-signature vectors both kit's side and the SDK's are
tested against. Each exported symbol, internal packages included, records:
its id — `go:<importpath>.<Name>`, `go:<importpath>.<Type>.<Method>` or
`go:<importpath>.(*T).M`, `%2e`-escaped, Go's runtime names —, its kind, its
signature as its file spells it and canonically, an alias's owner, a
constant's exact value, an initializer that is one name, an error code's
dotted quad (and a sentinel's reason and public text), a struct's exported
fields, its doc text, its cells when not all twelve, its module-relative file,
its layer and its family.

- **The writer is the SDK's** — `tools/genindex -write-api` (`make api`), a
  stdlib-only auxiliary module: one `go list -deps` per cell of the platforms
  table in workspace mode, then go/types over every package from source,
  function bodies ignored, **every cell checked in full** — no compiler
  export data and no shortcut to "the packages whose files differ": that
  shortcut writes the 64-bit `Unlimited` for the 32-bit cells, whose file has
  no constraint. About 1.2 s per cell; a cold run costs what a warm one does.
  The output is sorted, module-relative, without timestamp or absolute path,
  every go command's environment pinned: `make api` twice writes the same
  bytes on every machine.
- **The canonical signature is what go/types prints** with every package by
  its import path, aliases kept, type parameters grouped as go/types groups
  them — save two rules: a defined struct keeps its exported fields only (an
  unexported field is no part of the surface), and an interface keeps every
  method, unexported ones included (how a sealed interface is told apart). The
  pins' markers are these strings.
- **`.bazelignore` ignores `docs/site`, not `docs`**, so the Bazel test of
  genindex reads `docs/api/schema.json` and the vector tests run in the
  required job.
- **The schema changes with its readers**: `docs/site/scripts/lib/api.mjs`
  reads `sdk.api/v1` and refuses any other format by name, and kit mirrors the
  schema under a test.

### 6. `docs/error-codes.yaml` is written from `docs/api`

The file lists every errs.Code constant a package declares under a name
starting with `Code` — a re-export names its origin and is no entry, the errs
package's four masks are of the code type and no code — with its dotted quad,
hex, name and package: `make error-codes` (`tools/genindex
-write-error-codes`), which `make api` runs. The grep it replaces listed what a
regular expression matched: two codes a doc comment's example wrote, until
comments were skipped, and never the six meta-codes `0.0.0.1`–`0.0.0.6` the
errs package declares with `iota`. `check-error-codes-drift.sh` runs
`-check-error-codes`, which names each entry that differs; with `make
api-check` holding `docs/api` to the code, the file is the code's.

### 7. The surface is a gate in the SDK; the structural rules advise

kit checks a library against its design with nine rules. In the SDK only the
surface is a gate, because only it has a gate the SDK's CI runs without kit;
the structural rules repeat checks the SDK already enforces with its own
scripts, which stay authoritative (principle 26: a rule nothing checks is a
suggestion — and a check CI cannot run is not a gate):

| kit rule | Fails when | The SDK's gate | In the SDK |
|---|---|---|---|
| `api` | the code differs from the design, or kit's view from `docs/api` | the pins and `make api-check` | **gate** |
| `ports` | a port's code differs from its design | the compiler, through `design_gen.go`; the digests | **gate** |
| `layers` | an import goes against the declared order; a `stdlibOnly` layer imports a module; a `generic` layer names a domain | `scripts/check-layer-deps.sh` (ADR 0068) | advisory |
| `stdlib` | the SDK module requires a module outside the standard library | ADR 0156's tests | advisory |
| `mirrors` | a service domain has no core, or a code is declared outside the core | `check-core-symmetry.sh` (ADR 0160) | advisory |
| `codes` | two codes share a value, a range has two owners or none, a code is away from its engine | the registry audits, `check-audit-coverage.sh` | advisory |
| `facade` | `pkg/v1` re-exports a function through a variable `exceptions` does not list | none: the forwarders (§10) | advisory |
| `budgets` | a function marked `inline` does not inline; an `allocs` claim is in no alloc-lane target | the compiler's log; the alloc lane (rule 12) | advisory |
| `exceptions` | the exception list grew against a base | none | advisory |

kit runs every rule in its own CI against an SDK commit it pins and bumps on
purpose; at this record's commit `kit check` reports no finding under any of
the nine.

### 8. `codeRangeOwners` stays the authority on ranges

The design declares each code as a constant of its package with its exact
value, which the pins hold, and its `codes` section points kit at
`internal/kernel/errs/registry_ownership_external_test.go#codeRangeOwners`,
the hand-kept table ADR 0035 made independent of the constants it audits,
and at `BUILD.bazel#audit_sources`. There is no `codes.yaml`: a second table
of codes would be a second authority.

### 9. One table of platforms, and the bytes that are hashed stay LF

`scripts/ci/platforms.sh` is the twelve cells: genindex reads its
here-document without running it (doc links and `docs/api`), the design's
`platforms.from` names it, and `scripts/pre-commit/check-platforms.sh` holds
it to `bazel-ci.yml`'s cross-build matrix, cells and order. `.gitattributes`
fixes `design/**`, the generated files, `docs/api/**` and the table to LF, so
a Windows checkout hashes the bytes Linux and macOS hash.

### 10. The one function variable left in `pkg/v1`

A `pkg/v1` function is a forwarder, never a variable a consumer could reassign
for the whole process and that hides its callee from the inliner and escape
analysis. One stays a variable, measured, and the design lists it under
`exceptions`: `errs.ReasonOf`. Its forwarder cannot inline (cost 88 against
the budget of 80: it inlines the kernel accessor, 75 on its own) and costs
more than the 3 % the rule allows — CPU time per call +1.88 %, +1.07 %,
+3.88 % (p = 0.035) and +5.13 % (p = 0.043) over four benchstat runs of ten
samples, +2.91 % pooled over the forty (p < 0.001). It stays `var ReasonOf =
kerrs.ReasonOf` until the kernel accessor returns from one place.

### 11. The core's crypto, net and proc are families; the service's are one domain each

`docs/api` writes a package's family as ADR 0155 names it: the first directory
below its layer, or `root` for the kernel's and `pkg/v1`'s root packages,
which belong to no family. The core has no root package — its `crypto`, `net`
and `proc` are their families' root packages (ADR 0155 §1) —, so
`internal/core/crypto` is in the family `crypto`. kit places a package by the
design's `families`, where a `root` entry is one domain whose own package is
a root package. The project file therefore declares the core's seven
directories as families, and the service's `crypto`, `net` and `proc` as root
entries: in the service they are one domain each — an engine beneath one
answers to the core at its family's root, ADR 0160 §1 and the `ROOTS` of
`check-core-symmetry.sh` —, which is what the `mirrors` rule reads, and the
service has no package at their roots for kit to call `root`. Placed by this
project file, kit's view of every module equals `docs/api` byte for byte,
layer and family included.

### 12. The docs portal is built from `docs/api`

- The ⌘K index of each release that has `docs/api` is
  `symbols-<release>-<major>.json`, written by `docs/site/scripts/gen-symbols.mjs`
  from the page models, with no go command: every exported symbol of a
  `pkg/v1` package page, and every method an alias reaches at its owner —
  2,777 entries where go/doc gave 1,967.
- Each `pkg/v1` package page gains an API section in place of gomarkdoc's
  symbol dump: every constant, variable, function and type, each form a symbol
  has per platform, its code, what it re-exports, and an alias's declaration,
  fields and methods at its owner (3,685 anchors). Doc text is rendered as
  text, never as HTML.
- A release cut before `docs/api` existed keeps its README pages as they
  were, and has no symbol index: Pagefind's full text finds its pages. The
  READMEs stay gomarkdoc's for pkg.go.dev (ADR 0008).
- `docs/site/scripts/check-api-counts.mjs` fails unless the ⌘K index and the
  API sections equal `docs/api`; `check-links.mjs` unless every link and ⌘K
  entry resolves under the deploy base, fragments included, a missing fragment
  on an old release's frozen README page counted and not failed.
  `DOCS_RELEASES=local` builds the working tree alone, with no network: `make
  docs-check`, which CI's `docs-site` job runs and GATES names; the deploy
  runs the same checks over every release.

## As built

- `design/`: 102 files, 40,526 lines (1.1 MB): the project file and 101
  surface files, 315 packages, 7,528 records, 106 of them ports with their
  doc comments. `kit design import -type library` over it writes nothing;
  `kit check` gives no finding with all nine rules.
- The pins: 337 files — 312 `api_gen_test.go`, 25 per constraint —, 40,239
  lines, 7,528 markers; 282 sentinels held by reference, 95 existence pins.
  Gazelle added them to 297 test targets and gave 18 packages their first.
- The ports: `design_gen.go` in 33 core packages, 2,711 lines, 106
  interfaces and 269 methods — `app/lock`'s three first, then the 103 others.
  `kit gen` writes 370 files from the design, then nothing on a second run.
  The second step removed 35 hand-written files the move had left with
  nothing but their package comment, and kept eight that hold their
  package's comment alone.
- `docs/api`: 14 documents, 367 packages, 7,510 symbols in 7,528 records,
  2,175 with a code. Moving the ports changed 375 records' `file` — nine in
  the first step, 366 in the second — and one package comment, the core lock
  package's, which lost `lease.go`'s; nothing else.
- `go doc -all` of the 72 core packages: the same text before and after the
  second step but for `app/view`, whose `Factory` and `Renderer` each lost the
  blank line between their two methods.
- `docs/error-codes.yaml`: 739 codes, the 733 the grep listed and the six
  meta-codes.
- Cost, measured on an M1 Pro shared with other jobs (a load of 16 to 100),
  judged on CPU (user + system):
  - `bazel test --config=race //...` from cold — a fresh output base, no
    disk cache —, the base and this change started together so the load
    weighs on both alike: the actions' CPU 1,089 s → 1,148 s (+5.4 %), the
    identical standard-library compile equal (120 s, 119 s); wall 1,204 s →
    1,210 s (+0.5 %); 299 → 317 test targets. The 59 s are the pins compiled
    into the external test packages (+19 s) and the 18 new test targets'
    compiles (+19 s), links (+15 s) and runs (+6 s). With a warm disk cache,
    as CI's `bazel` job runs, a change rebuilds the test targets of the
    packages it touches, as before.
  - The cross-build lane, 12 cells × 17 modules (`go build ./...` then
    `go vet ./...`, tests compiled), each cell on a fresh build cache:
    1,529 → 1,545 CPU s in all (+1.0 %). A cell's own spread, −7 % to +11 %,
    is the machine's: linux/arm64 and windows/amd64 measured again with both
    sides started together gave −1.1 %, −8.8 % and +0.8 %.
  - `make api-check` with `-markers -digests` costs what it cost without
    them — 61 CPU s against 61 over three pairs on that load —, both checks
    reusing the model the `docs/api` check builds; `make lint-check` takes
    17 s and 35 CPU s on a quiet machine.
  - The second step costs nothing measurable, each lane measured with the
    first step's tree and the second's started together, every side cold:
    `bazel test --config=race //...` — run in four parts, two output bases
    at a time being all the disk held — 1,117.7 → 1,118.5 s of the actions'
    CPU (+0.07 %), the same 4,326 actions and 317 tests; the cross-build
    lane, 12 cells × 17 modules, 2,065 → 2,060 CPU s (−0.3 %), a cell
    between −5.3 % and +4.1 % and its user CPU within 1.3 %.
  - A consumer's binary — every `pkg/v1` package imported, the lock ports in
    use — has the same byte count, the same 9,068 symbols with their kinds
    and sizes, and the same 5,426 functions at the same lines; only its build
    information (the scratch module's replace path) and the length of the
    runtime's function table (16 bytes) differ: the ports compile to
    nothing. The second step, measured with the same consumer also holding
    the runtime type of each of the 93 ports `pkg/v1` aliases: the same
    8,703,426 bytes, the same 9,198 symbols with their kinds and sizes, the
    same 5,549 functions in the same files at the same sizes; nine of them
    start at another line, and the function table's line encoding moves
    96 bytes with them — positions, not code.

## Consequences

- An API change is a design change first, and needs kit (§4); the SDK's CI
  says so by failing, never by running kit.
- A review of an API change reads one domain's YAML and the Go kit wrote from
  it: each generated header names one design file, so `kit gen` rewrites that
  domain's pins and ports alone, and two changes to two domains touch
  different design files and different generated files — `docs/api`, one
  document per module, is the one file they may both change.
- `make api-check` costs about the same as before the markers and digests
  (they reuse the model it builds), and `make api` runs gazelle.
- The pins add a test file to almost every package's test target and a test
  target to eighteen packages: their compile is in the Bazel and cross-build
  lanes' cost, measured above.
- The structural rules stay the SDK's scripts' (§7); kit holding the SDK to
  them in its own CI is a second opinion, never the gate.

## Alternatives considered

- **Generate `docs/api` from the design.** Rejected: a doc edit would need
  kit, and `docs/api` would describe the design rather than the code; written
  from the code by the SDK's tool, it is checked against the design through
  the markers instead.
- **Pin `pkg/v1` alone and leave `internal/` undesigned.** Rejected: the
  surface is every exported symbol of every module, and `pkg/v1` aliases the
  internal types its members belong to.
- **Run kit in the SDK's CI.** Rejected while kit is not public: the SDK's
  gates would depend on a tool its contributors cannot run.
- **A table of codes in the design.** Rejected (§8): `codeRangeOwners` is the
  one hand-kept table, and each code is a constant the pins hold.
- **A digest of each doc comment shared by both tools.** Rejected: `docs/api`
  carries the doc text from the code and `make api-check` diffs it, so no doc
  digest crosses repositories; a header digest is the sha256 of bytes and
  needs no specification.

## References

- `design/` — `design/sdk.yaml`, and one surface file per domain or package group
- `tools/genindex/CLAUDE.md` — `-write-api`, `-check-api -markers -digests`, `-write-error-codes`, `-check-error-codes`
- `docs/api/schema.json` — the format, the id and canonical-signature vectors
- `docs/site/CLAUDE.md` — the portal built from `docs/api`
- `internal/core/app/lock/CLAUDE.md` — the first generated ports
- `internal/core/CLAUDE.md` — every core interface a generated port, and each package's `CLAUDE.md` the files its ports left
- `scripts/ci/platforms.sh`, `scripts/pre-commit/check-platforms.sh` — the platforms table
