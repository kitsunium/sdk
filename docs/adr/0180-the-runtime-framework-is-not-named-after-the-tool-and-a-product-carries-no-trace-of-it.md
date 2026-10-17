# ADR 0180 — the runtime framework is not named after the tool, and a product carries no trace of it

- **Status**: Proposed
- **Date**: 2026-10-17
- **Deciders**: SDK maintainers, on the owner's decision of 2026-10-17 (transcribed in §Context, *The owner's decision*); arbitrated by the owner, who answers the questions of §Open questions
- **Amends** (once accepted): [ADR 0147](0147-the-framework-is-a-module-of-the-sdk-above-pkg.md) §1 — its sentence "The name `kit` is kept for the runtime package" is withdrawn; [ADR 0179](0179-a-store-is-a-non-generic-engine-under-a-typed-facade-and-a-subsystem-is-imported.md) §Decisions of 17 October — this is "the ADR to come" its last paragraph announces
- **Related**: [ADR 0040](0040-changing-a-published-shape-while-v0.md) (the v0 licence), [ADR 0149](0149-a-product-reports-numbers-on-a-port-and-an-exporter-only-emits.md) (the telemetry wire), [ADR 0158](0158-distribution-mechanisms-are-the-frameworks-not-the-sdks.md), [ADR 0160](0160-every-service-has-a-core-and-a-code-keeps-its-value-when-it-moves.md) (a code keeps its value when its declaration moves), [ADR 0162](0162-the-sdk-is-one-module-and-a-release-is-one-tag.md) and [ADR 0171](0171-a-module-per-family-and-a-version-is-never-reused.md) (the framework is the root module, released on its own), [ADR 0163](0163-the-sdk-is-designed-by-its-diagram-and-kit-writes-only-data-and-test-pins.md), [ADR 0167](0167-a-doc-comment-is-the-designs-and-a-readme-is-written-from-docs-api.md) and [ADR 0168](0168-a-declaration-is-the-designs-and-a-body-is-the-codes.md) (a rename is a design edit first), [ADR 0172](0172-a-product-initialises-what-it-imports-and-the-stores-stay-in-the-core.md) (a subsystem sits where its reader is); kitsunium/platform's ADR 0007 (`kit.Revisions`, the `history=N` tag), ADR 0008 (a module exports one `*kit.Module`) and ADR 0010 (kit is a design-first tool: D2, D8, D13, D17, D22, the header and the harness)

## Context

### The owner's decision (2026-10-17)

Transcribed as given, and the only thing this record takes as decided:

- **kit is a development tool.** kit — kitsunium/platform: the `kit` CLI,
  the generator, the Studio — is a tool a developer runs, not something a
  product is made of.
- **The design imposes the structure.** The diagrams of `design/` define
  the interfaces and the structure of the project and of its packages,
  imposed by the chosen architecture, the way Angular imposes a tree. The
  generated part could be a git repository of its own; the hand-written
  implementation lives in dedicated directories that import those
  interfaces; regenerating shows at once what is not implemented yet.
- **The aim.** An AI drives the Studio, and the user watches the diagrams
  and the code advance in real time.
- **The rule.** The exported, compiled code of a product contains **no
  trace of the kit tool nor of the Studio**: one could copy the generated
  repository, it works, and nobody can tell it was made with kit.
- **What is barred is the tool, not the runtime framework.** Depending on
  `github.com/kitsunium/sdk/framework/...` — the app, the services, the
  stores, the files — is acceptable, as depending on Spring is. **But that
  framework must no longer be called "kit"** (for example `framework/app`),
  and `dev_gen.go` — the Studio wired in under the `kitdev` build tag — must
  leave the exported code.

### The gap with ADR 0147

ADR 0147 moved the runtime out of kitsunium/platform into the SDK so that a
product imports the SDK and never the platform (the platform's D2, D17). It
kept the name on purpose (§1): *"The name `kit` is kept for the runtime
package: the design's generated shells are written in kit's words
(`kit.Name`, `kit.Bind`), and a product that migrates changes an import
path, not a vocabulary."* So the line ADR 0147 drew is a **module** line —
no `github.com/kitsunium/platform` in a product's `go.mod` — while the
owner's line is a **name** line: no `kit` anywhere a reader of the product's
code, of its binary or of its running process can see it. ADR 0147 meets its
own line; it does not meet the owner's, and nothing checks the owner's. ADR
0179's last decision recorded the rule in one line and deferred the rename
to this record.

### What a product carries today, measured

Measured on 2026-10-17 on: the SDK's `main` after ADR 0179's step 10; the
platform's `main` of the same day (its reference product
`examples/reference` and its generator `generate/`); and the default
branches of the three framework products — kitsunium/todo (2026-10-04),
kitsunium/statusline (2026-10-01) and kodmain/moderation (2026-09-30). The
token counted is `kit`, case-insensitive, **outside** `kitsunium` (the
vendor's name, which the owner accepts as the name of the SDK a product
depends on). Counts come from `grep`, `docs/api/sdk.json`, `strings`,
`go tool nm` and `go version -m` on binaries built with
`go build -trimpath` (linux/amd64), and from running them.

Three places are distinguished: the **code** a product exports (its tree,
what a reader of its repository sees), its **binary** (what `strings` or
`nm` shows), and its **execution** (what an operator, a client, a database
administrator or a mail recipient sees).

#### In the framework — what every framework product inherits

| Category | Measure |
|---|---|
| Import paths | the facade `framework/kit`; its eleven subpackages `framework/kit/{store,queue,workflow,mail,jobs,server,studio,config/yaml,config/toml,kittest,storetest}`; the implementation `framework/internal/kit` and its engines `plug`, `storekit`, `queuekit`, `workflowkit`, `mailkit`, `jobkit`, `serverkit`, `studiokit` |
| Public identifiers through the package name | `framework/kit` publishes **298** names (`docs/api`: 80 aliases, 109 functions, 105 constants, 4 variables) — `kit.App`, `kit.Service`, `kit.Store`, `kit.Migration`, `kit.Dialect`, `kit.Auth`, `kit.Error`, `kit.WireNotFound`… —, `kittest` 3, `storetest` 4; none of the 298 contains `kit` in its own name, every one is written `kit.X` by a product |
| Identifiers containing the word | `framework/model`'s `ProvenanceKit`, `PortOwnerKit`, `SettingKitDev` (and their core twins) |
| Struct tag key | `kit:"…"` — the field classification and redaction tag (`personal`, `secret`, `public`, `subject`, `erased`, `history=N`), read with `Tag.Get("kit")` |
| Environment variables | **25** names read by the framework. Set by an operator (16): `KIT_ADDR`, `KIT_ALLOWED_HOSTS`, `KIT_DATA_DIR`, `KIT_DATA_KEY`, `KIT_ENV`, `KIT_INDEX_KEY`, `KIT_LOG_LEVEL`, `KIT_RETENTION`, `KIT_SECRETS`, `KIT_SECRETS_KEY`, `KIT_SECRETS_KEY_FILE`, `KIT_SMTP_URL`, `KIT_SMTP_URL_FILE`, `KIT_TELEMETRY`, `KIT_TELEMETRY_GIDS`, `KIT_TRUST_PROXY`. Set by the tool (9): `KIT_DEV_BUILD_MS`, `KIT_DEV_BUILT_AT`, `KIT_DEV_CHANGED`, `KIT_DEV_CHANGED_MORE`, `KIT_DEV_FORCED`, `KIT_DEV_REBUILDS`, `KIT_STUDIO`, `KIT_STUDIO_REMOTE`, `KIT_ANALYZE` |
| HTTP routes | **25** distinct `/_kit…` literals: the three probes `/_kit/health/{live,ready,startup}`, served always, production included; `/_kit/` reserved in production, answering 404 with "the Studio is only served in dev (KIT_ENV=dev)"; **18** `/_kit/api/*` Studio reads, served in dev when the Studio is linked, but **their handlers are compiled into the core** (`mountIntrospection`), so into every production binary |
| String literals | **158** distinct literals in the 340 production files of `framework/{kit,internal/kit,model,telemetry}` name kit (388 occurrences); 29 begin `"kit: "`, the Private-text convention |
| Error codes | 174 construction sites over 78 codes: **no** Reason names kit, **13** Public texts and **20** Private texts do ("kit's data keys could not be read", "a store kit can write: KIT_SECRETS=file:<dir>…"), on **22** codes. The 84 codes of `0.4.2.*` have no `kit` in their constant names; their values say layer 4, the framework, not the tool |
| Data at rest | SQL tables `kit_migrations`, `kit_tables`, `kit_queue`; the framework's migration set named `kit`; the secret-name prefix `kit-` (`kit-smtp-url`…); the dev data directory `.kit/data`; the domain-separation labels of the sealed data and of the privacy keys — `kit/v1 sealed at rest`, `kit/v1 subject reference`, `kit/v1 record reference`, `kit/v1 database index key`, `kit:subject`, `kit:fingerprint` |
| Wire and graph | the telemetry handshake `kit-telemetry/v1`; the graph's JSON field `"kit"` (the framework's version), provenance and port owner `"kit"`, the reserved module name `kit`, the service `kit.privacy`, the labels `kit_node` and `kit_loop`; the readiness and liveness checks named `kit` (in the probes' detail); the header `X-Kit-Attachments` of a captured mail (the Studio's capture view) |
| What the process says | `<product> -h` opens with `<product> — a product built with kit.`, lists the `KIT_*` variables and "a secret kit generates"; at start in dev, the info line "the product, as a diagram: kit dev prints the Studio's link" |
| Generated headers in the SDK itself | 580 files of the SDK (220 not tests, 45 under `framework/`) begin `// Code generated by kit from design/…` — the SDK is designed with kit (ADR 0163); `design/framework/kit.yaml` is the facade's design |

#### In a binary — the smallest framework product

`framework/kit/testdata/cliprobe` (one service, one command, the facade
alone), 14.44 MB: **952** symbols under `framework/kit` or
`framework/internal/kit`; **596** distinct printable strings naming kit;
27 naming `framework/kit`; 25 distinct `KIT_*` names; 13 strings with
`/_kit`. Run, its help prints the sentence and the variables above, and
`cliprobe graph` prints `"kit"` as a field, a provenance and a service
prefix.

#### In the platform's reference product

`examples/reference` is a plain-Go product (it imports `pkg/v1/errs` and no
framework package), so it shows what the **tool** leaves when the runtime
leaves nothing:

| Place | Measure |
|---|---|
| Code | 27 Go files: 10 generated files whose first two lines are `// Code generated by kit from design/v1; DO NOT EDIT.` and `// kit:digest sha256:…`; **17** hand-written files opening "Written once by kit gen, never again: this file is yours … kit check reads a body that still …"; 2 generated tests reading `KIT_SEED` ("when kit test -seed sets it"); `design/` (4 files, `apiVersion: kit.kitsunium/design/v2`), `design/generated.lock` ("Written by kit gen"), `kit.measure.json`; its module path `example.com/kit/reference`, the example's own spelling |
| Binary | 2.60 MB: 0 framework symbol, 0 `KIT_`, 0 `/_kit`; 47 strings name kit, **every one** the module path `example.com/kit/…`; 9 strings name a `*_gen.go` file |
| Execution | nothing |

So, measured, the tool's own traces are the header, the digest line, the
skeleton comment, `KIT_SEED`, the design's `apiVersion`, the lock, the
version pin and `dev_gen.go`; everything a framework product shows beyond
them comes from the runtime's name.

#### `dev_gen.go` and `kitdev`

`generate/framework.go` declares `DevTag = "kitdev"` and
`DevFile = "dev_gen.go"`: when a role of a binary declares the Studio, `kit
gen` writes `cmd/<binary>/dev_gen.go`, generated, `//go:build kitdev`,
importing `framework/kit/studio`, and `kit dev` builds with `-tags kitdev`
(`cmd/kit/dev.go`), then hands the product the `KIT_DEV_*` variables. A
release build without the tag does not link the Studio, but the file, its
tag and its import are in the exported tree. The products do it by hand:
the todo has a hand-written `kitdev.go` under the same tag; moderation
imports `framework/kit/studio` **unconditionally** from `main.go`, so its
release binary links the Studio.

#### In the three products

| | todo | moderation | statusline |
|---|---|---|---|
| Go files / importing `framework/kit…` | 44 / 32 | 89 / 60 | 215 / 10 |
| `kit.X` uses (distinct) | 361 (49) | 611 (62) | 34 (17) |
| `kit:"…"` struct tags | 4 | **174** (87 `personal`, 50 `secret`, 20 `public`, 17 `subject`, 10 `erased`) | 0 |
| Studio wiring | `kitdev.go`, hand-written, tagged | `main.go`, untagged, linked in release | none |
| Generated headers / skeleton comments | 0 / 0 | 0 / 0 | 36 / 54 |
| `KIT_*` outside Go (Dockerfile, compose, CI) | 7 files (`KIT_ADDR`, `KIT_DATA_DIR`, `KIT_ENV`, `KIT_SMTP_URL`, `KIT_TRUST_PROXY`, `KIT_APP`) | 17 files, 71 occurrences, 11 names | `KIT_SEED`, `KIT_READ_TOKEN` (CI installing the tool) |
| Other | `/_kit` in 13 files, `.kit/` ignored | `/_kit` in 11 files, `.kit/` ignored | `design/` + lock, `kit.version`, `.kit/` ignored |

The import of `framework/kit` and the `kit.X` spelling are a product's
whole surface on the framework: ~1 000 call sites across the three, every
one of them a trace under the owner's rule.

## Decision

Proposed. Everything below follows from the owner's decision; where the
decision leaves a choice, the choice is a question of §Open questions and
the text names its recommendation without taking it.

### 1. The runtime framework takes a name that is not the tool's

`framework/kit` is renamed, with everything beneath it:

- the facade `framework/kit` → `framework/<name>`;
- its subsystems `framework/kit/{store,queue,workflow,mail,jobs,server,config/yaml,config/toml}`
  → `framework/<name>/…`, the same leaves; `framework/kit/kittest` →
  `framework/<name>/<name>test` (the `httptest`/`fstest` convention);
  `framework/kit/storetest` keeps its leaf;
- the implementation `framework/internal/kit` → `framework/internal/<name>`,
  its engines renamed so that no path segment ends in `kit`;
- `framework/model`'s `ProvenanceKit`, `PortOwnerKit`, `SettingKitDev` and
  every value spelled `"kit"` in the graph renamed after the framework; the
  graph's `Version` goes from 5 to 6;
- the design file `design/framework/kit.yaml` → `design/framework/<name>.yaml`.

`<name>` is Q1. The candidates, measured against the three products:

| Candidate | For | Against |
|---|---|---|
| **`framework/app`** (recommended) | the owner's example; a product writes `app.Service`, `app.Store[T]`, `app.Auth()`, `app.Transact` — the words of the thing it builds, as `http.Handler` is; no standard package of that name; the SDK has no Go package named `app` (`pkg/v1/app` is a family directory with no Go code, ADR 0155) | a local variable `app` shadows the package: **14** files of the three products declare one in a file that imports the facade (renamed in the migration, or the import aliased); `app.App` and `app.AppOption` stutter (Q2) |
| `framework/runtime` | says what it is — the runtime the tool's code runs on | collides with the standard library's `runtime`: every file needing both aliases one, forever (no product file imports both today; any `runtime.GOOS` later does) |
| `framework/core` | short, neutral | the SDK's layer vocabulary already means `internal/core` (ADR 0155, ADR 0160), so every document would have to say which core; says nothing of what a product builds |

The identifiers keep their names: a product's `kit.Store[Order]` becomes
`app.Store[Order]` and nothing else. The wire codes (`WireNotFound` and its
siblings' `snake_case` strings, ADR 0147 §6) keep their values. The error
codes keep their values: `0.4.2.*` stays the framework's range, and
`codes.ranges` re-points its owner at the new path (ADR 0160 §3).

### 2. The framework speaks of itself by its name, and of the tool nowhere a product shows

Every string the framework compiles into a product — a Public or a Private
text, a log message, a help line, a refusal — names the framework or the
product, never the tool: the 13 Public and 20 Private texts naming kit are
reworded, the help's first line loses "a product built with kit", "a secret
kit generates" becomes "a secret the product generates". The doc comments of
`framework/<name>` speak of the framework by its new name; a comment may
name the tool only where it describes the contract between the two (the
Studio hook of §4), and no such comment is compiled.

### 3. Every runtime name a product shows is renamed, and an old name is refused, never ignored

| Category | Becomes | Why it cannot be dropped silently |
|---|---|---|
| The 16 operator variables `KIT_*` | the product's own prefix, `<PRODUCT>_ADDR`, `<PRODUCT>_DATA_DIR`… — the framework already derives `<PRODUCT>_<NAME>` for a product's settings and secrets, so these names become reserved in that namespace (Q3) | an operator's `KIT_DATA_DIR=/data` ignored would start the product on an empty default directory |
| The 9 tool variables (`KIT_DEV_*`, `KIT_STUDIO*`, `KIT_ANALYZE`) | read only by the Studio hook (§4), which only a dev build links; the core reads none | — |
| `/_kit/health/{live,ready,startup}` and the reserved `/_kit/` | a reserved prefix named after the framework, `/_<name>/health/…` (Q4) | an orchestrator's probe on the old path would see 404 and kill the process; for the transition window the old paths answer, logged as deprecated (Q12) |
| The 18 `/_kit/api/*` Studio reads | under the same reserved prefix, and **moved out of the core** into the Studio subsystem (§4): a release binary does not compile them | — |
| The struct tag key `kit:"…"` | the framework's name, `<name>:"personal"` (Q7) | a `personal` field whose tag is no longer read would be stored **in clear**: the start refuses a field still tagged `kit:"…"`, naming the new key, every field at once, like `CodeSubsystemMissing` (ADR 0179 §6) |
| Graph values (`"kit"` field, provenance, port owner, reserved module, `kit.privacy`, `kit_node`, `kit_loop`), the checks named `kit` | the framework's name | a module named after the framework is reserved as `kit` was |
| The telemetry handshake `kit-telemetry/v1` | a new protocol name, `<name>-telemetry/v1`; the tool's collector accepts both for the transition window | the wire is the tool's to read, the name is the product's to send |
| The captured mail's `X-Kit-Attachments` | written by the Studio subsystem, which shows it, under a neutral name | — |

### 4. The Studio is plugged in by the tool at build time, and nothing of it is in the product's tree

- **The generator writes no `dev_gen.go`.** `DevTag`, `DevFile` and the
  `kitdev` tag leave the generator; a product's tree has no file whose only
  reason to exist is the tool.
- **`kit dev` injects the hook with `go build -overlay`.** It writes, in the
  tool's own state directory **outside** the product's tree, one Go file
  `package main` that blank-imports the Studio subsystem, and an overlay
  JSON that maps a path in the binary's main package directory to that file
  (`go help build`: the build "will run as if the disk file path exists
  with the contents given by the backing file path"). The Studio package is
  in the framework's module, which the product already requires, so the
  build list does not change and `go.mod` is not touched. Nothing is
  written in the tree; `git status` after `kit dev` is what it was before.
- **The Studio subsystem holds everything the Studio reads.** Since ADR
  0179 the subsystem is enabled by an import (`framework/kit/studio`, today
  only the catalogues); the introspection routes, their handlers, the
  capture view and the tool's variables move from the core into it, as ADR
  0172 §2 put the process sampler where its reader is. A release binary —
  built by `go build` with no overlay — links none of it.
- **A product never imports the Studio subsystem.** `kit check` refuses the
  import in any package of a product (beside the platform's `imports`
  rule); the todo's `kitdev.go` and moderation's import in `main.go` are
  deleted.
- **Where the hook lives** is Q8; recommended: it stays the framework's
  (`framework/<name>/studio`), never imported by a product, injected by the
  tool.

The overlay's limits, as `go help build` states them: overlays "will not
appear when binaries and tests are run through go run and go test" — `kit
dev` runs `go build`, then the binary —, and "files beneath GOMODCACHE may
not be replaced" — the overlay adds a file to the product's own directory,
never to the module cache. A product's tests never link the Studio, as
today.

### 5. What the tool leaves in a product's tree carries no tool name

These are the platform's to implement, in an ADR of its own; this record
states what the SDK's rule requires of them and the recommended shape:

- **The generated header** stays Go's convention — the first line matching
  `^// Code generated .* DO NOT EDIT\.$`, which gopls, linters and code
  review tools read — without naming the tool:
  `// Code generated from design/v1; DO NOT EDIT.`, and the digest line
  `// design:digest sha256:…` (Q9). `design.IsGenerated` recognises both
  forms during the transition.
- **A skeleton** (a hand-written twin written once) carries no provenance:
  it is the developer's file from its first byte; at most
  "Written once from the design; this file is yours." (Q9).
- **A generated test's seed** reads a neutral variable, never `KIT_SEED`.
- **The design, the lock and the version pin** carry no tool name in their
  content; where they live is Q10.
- **File names** stay as they are: `*_gen.go`, `design_gen.go`,
  `wire_gen.go`, `main_gen.go` and `design/` name a convention, not a tool,
  and `go generate`'s ecosystem uses the same.

### 6. The rule, and what holds it

**Rule.** A product's exported tree, its release binary and its execution
contain no occurrence of the token `kit`, case-insensitive, outside
`kitsunium`. The exported tree is every file `go build` or `go test` reads
(Go sources, tests included, `go.mod`, embedded files); the release binary
is `go build` with no tag and no overlay; the execution is `-h`, `graph`,
`config`, the probes' answers and the environment and paths the process
reads. A product's prose (`README.md`, its documentation) is its owner's,
out of the mechanical check.

**What holds it**, each a named gate (SDK rule 12):

- **In the SDK**, a test of `framework/<name>` beside the initgraph tests
  (ADR 0179 §7), `TestAProductCarriesNoTraceOfTheTool`: it builds
  `testdata/traceprobe` — a product that declares a store, a queue, a
  workflow, a mailer, a job and a server, every subsystem but the Studio —
  with the release flags, then refuses the token in the probe's sources, in
  `go tool nm` and `strings` of the binary, in `go version -m` (the
  `kitsunium` module paths excepted), in the output of `-h`, `graph` and
  `config`, and in the names the probe's environment table and route table
  hold. Like the initgraph tests it skips under Bazel; its gate is CI's
  `test-386` job, which runs `go test` over every module. A second check
  refuses the token in the string literals of `framework/<name>` and
  `framework/internal/<name>` outside the Studio subsystem, which only a
  dev build links. Its allow-list starts empty; an entry names the ADR that
  admits it.
- **In the platform**, `TestTheReferenceCarriesNoTraceOfTheTool` over
  `examples/reference`: a fresh generation, the same refusal over the tree
  and its release binary, and `kit dev`'s overlay leaving the tree
  byte-identical.

## Consequences / Semantics

### Migration, in this order

Each step builds and passes its gates on its own; steps 1 and 2 break
nothing.

| Step | Where | What |
|---|---|---|
| 0 | — | The owner answers §Open questions; this record is accepted with its answers |
| 1 | SDK | The Studio's routes, handlers, capture view and tool variables move from the core into the Studio subsystem; the core keeps the probes (non-breaking: a product that imports `framework/kit/studio` keeps its Studio) |
| 2 | platform | `kit dev` injects the hook by overlay; the generator stops writing `dev_gen.go`; `kit check` refuses a product's import of the Studio subsystem; the todo deletes `kitdev.go`, moderation its import |
| 3 | SDK | The rename of §1, a design edit first (ADR 0163, 0167, 0168): `design/framework/<name>.yaml`, `kit gen`, `make api`, `make docs-readme`; the facade's and implementation's paths; `model` `Version` 6; the scripts, `BUILD.bazel`, `//:audit_sources`, `tools/alloc-lane-targets.txt` and `codes.ranges` owners re-pointed; the three database connectors follow; the texts of §2 reworded |
| 4 | SDK | The runtime names of §3: the prefix, the routes, the tag key, the graph values, the telemetry name; the refusals of an old name at the start (§3, Q12) |
| 5 | SDK | The names at rest (Q5, Q6): the tables, the migration set, the secret prefix, the dev data directory — renamed by a framework migration —, the domain-separation labels as Q6 decides |
| 6 | SDK | `TestAProductCarriesNoTraceOfTheTool`, with an empty allow-list; it can land earlier with an allow-list shrinking step by step, as ADR 0179's `stillInCore` did |
| 7 | platform | Its ADR for §5: the header, the digest line, the skeletons, the seed variable, the design's `apiVersion`, the lock, the version pin; the analyzer and `subsystem.Table` on the new paths; `internal/modelts` regenerated for `Version` 6; the Studio's client on the new prefix; the reference regenerated, and its guard |
| 8 | products | todo, moderation, statusline: the import path and `kit.` → `<name>.` (a rewrite of the import path and the package qualifier, then the 14 shadowing files by hand), the struct tags (moderation's 174), the variables in Dockerfiles, compose files and CI, `.gitignore` |

### Impact

- **The SDK.** Under `framework/`, 694 Go files mention the word (7 012
  occurrences, comments included); most change by the path and the package
  clause. The design file of the facade (450 KB) is renamed and regenerated
  with the owner's pinned kit, so steps 3 and 4 need the owner's tool, as
  every exported-surface change does since ADR 0163.
- **The platform.** The generator (`frameworkKit`, `Subsystems`, `DevTag`,
  `DevFile`, the header, the skeleton text, the seed), the analyzer (which
  recognises declarations by the facade's import path), `subsystem.Table`,
  `internal/modelts`, the Studio's client paths. The tool keeps its own name
  everywhere in itself: its CLI, its daemon's `/_kit/p/{id}/…` routes, its
  state directory — the rule is about a product.
- **The products.** The counts of §Context: todo 32 files and 361 uses,
  moderation 60 files, 611 uses and 174 tags, statusline 10 files and 34
  uses, plus 36 headers and 54 skeleton comments its next `kit gen`
  rewrites.

### Cost, estimated (not measured)

| Part | Estimate |
|---|---|
| Step 1 (the Studio out of the core) | 1–2 days |
| Step 2 (overlay, generator, check) | 1–2 days, platform |
| Step 3 (the rename, design-first, gates) | 2–3 days |
| Steps 4–5 (runtime names, refusals, migrations at rest) | 2–4 days, Q5 and Q6 deciding the upper half |
| Step 6 (the guard) | 1 day |
| Step 7 (platform, header and paths) | 1–2 days |
| Step 8 (three products, scripted) | 1 day |
| **Total** | **9–15 days**, one change at a time (no two lanes at once on the shared machine) |

## Breaking changes

None in this record, which decides nothing yet. Forecast for steps 3 to 5,
under ADR 0040's v0 licence, which the root module still holds: a minor of
the root module (`release:minor`) and of each connector module, each at its
own version (ADR 0171). Every product changes its imports and its
qualifier; nothing is kept under the old path unless Q2 decides otherwise.
The operator's variables, the probes' paths and the struct tag key change,
each refused or answered as §3 and Q12 say rather than ignored. The graph's
`Version` goes to 6, which the platform's Studio reads. What is at rest
changes only by a migration the framework runs (Q5, Q6); where the owner
answers "freeze" to Q5 or Q6, a product keeps its bytes.

## Alternatives considered

- **Keep the name (ADR 0147 §1).** Refused by the owner.
- **Rename the package and keep the runtime names.** It removes the
  traces a reader of the code sees and leaves the ones an operator, a
  client and a database administrator see — the probes' paths, the
  variables, the tables, the help. The owner's rule names the compiled code
  and its execution, not only its source.
- **Keep `dev_gen.go` and the `kitdev` tag.** The release binary is clean,
  but the file, its tag and its import are in the exported tree — what the
  owner named.
- **Link the Studio always, enabled by a variable.** The release binary
  carries the Studio and its routes; moderation does this today.
- **A Go plugin loaded by `kit dev`.** `plugin` needs cgo, runs on few
  platforms, and requires the plugin and the binary built from identical
  dependencies.
- **A dev `main` module in the tool's state directory** that imports the
  product's role packages and the Studio. It works — the generated role
  packages are importable — but it needs a second module, a `replace` or a
  `go.work` pointing at the product, and a second wiring of the roles; the
  overlay is one flag and changes no module graph.
- **An export command that scrubs the traces** (`kit export`). The shipped
  tree would differ from the tree developed and checked; the rule must hold
  of the tree as it is, so that "copy the repository" is literally true.
- **A deprecated alias package left at `framework/kit`.** Q2: it keeps
  the name in the code of every product that has not moved, which is the
  thing being removed.

## Open questions

Each with options and a recommendation; nothing here is decided.

**Q1 — The framework's name.**
(a) `framework/app`; (b) `framework/runtime`; (c) `framework/core`.
*Recommendation*: **(a) `framework/app`** — the owner's example, the word of
what a product builds, no collision with the standard library or the SDK's
layer vocabulary; the cost is 14 product files with a local `app`
(§1's table).

**Q2 — The old import path.**
(a) a hard break under ADR 0040, the three products migrated in step 8;
(b) a deprecated alias package at `framework/kit` for one minor.
*Recommendation*: **(a)** — three products, all in-house, a scripted
rewrite; an alias would carry the name into every product that does not
move. And keep the identifiers (`app.App`, `app.AppOption`): renaming them
to `app.New`, `app.Option` is a second break with no rule behind it.

**Q3 — The operator's variables.**
(a) the product's own prefix (`TODO_DATA_DIR`), reserved in the namespace
the framework already derives for a product's settings; (b) one fixed
neutral prefix (`APP_DATA_DIR`); (c) no prefix (`DATA_DIR`).
*Recommendation*: **(a)** — the operator sees only the product's name, and
the framework already speaks that namespace; (b) still shows a framework
convention and collides between two products of one environment, (c)
collides with everything.

**Q4 — The probes' paths and the reserved prefix.**
(a) `/_<name>/health/{live,ready,startup}`, one reserved prefix, the
Studio's reads under it in dev; (b) the Kubernetes spellings `/livez`,
`/readyz`, `/startupz` at the root; (c) a prefix the product configures,
`/_<name>/` by default.
*Recommendation*: **(a)** — one reserved prefix, as today, neutral, and no
root path taken from a product.

**Q5 — Names at rest: tables, migration set, secret prefix, dev data directory.**
(a) renamed by a framework migration (the framework's own migration set
renames its tables in one transaction where the engine allows — MySQL's DDL
commits implicitly, ADR 0055 — and its secrets by a rewrite under the new
prefix); (b) frozen, as documented exceptions of the guard's allow-list;
(c) renamed for new installations only, the old names read where present.
*Recommendation*: **(a)** — a database administrator reading
`kit_migrations` knows; (c) keeps both spellings in every binary for good.

**Q6 — The domain-separation labels (`kit/v1 sealed at rest`…).**
(a) a re-seal: new data under new labels, a framework command re-sealing
what exists, then the old labels removed in a later minor; (b) frozen
forever as opaque format identifiers, in the allow-list; (c) new labels for
new data, old ones read forever.
*Recommendation*: **(a)** — the labels are strings in every binary, and
renaming them without re-sealing makes every sealed value unreadable; (a)
is the only path to a binary with none, at the cost of one re-seal per
installation, which the privacy commands already know how to run.

**Q7 — The struct tag key.**
(a) the framework's name, `app:"personal"`, as `json:` names its package;
(b) a word for what it says, `privacy:"personal"`; (c) `data:"personal"`.
*Recommendation*: **(a)** — the tag also carries `secret`, `public` and
`history=N`, which (b) misnames; a tag key named after the package that
reads it is Go's convention.

**Q8 — Where the Studio hook lives.**
(a) in the framework, `framework/<name>/studio`, never imported by a
product, injected by `kit dev`'s overlay; (b) in the platform, injected
with a `go.work` written in the tool's state directory; (c) out of process:
the Studio reads the product through the telemetry port and the collector
(the platform's D10), no hook at all.
*Recommendation*: **(a)** — no change of module graph, no platform module in
a product's build; (c) is the long-term shape and its own ADR.

**Q9 — The generated header and the skeleton comment (the platform's).**
(a) `// Code generated from design/v1; DO NOT EDIT.` with
`// design:digest sha256:…`, and a skeleton with no provenance line;
(b) keep naming kit in the header only; (c) a header the product
configures.
*Recommendation*: **(a)** — Go's convention needs "Code generated" and "DO
NOT EDIT", not a tool's name.

**Q10 — The design, the lock, the version pin and CI.**
(a) `design/` and its lock stay in the product's repository, with no tool
name in their content (a neutral `apiVersion`), and the version pin and the
CI step that installs the tool move to the developer's environment and the
CI's configuration outside the product's files; (b) the design lives
outside the exported tree — a repository or a directory the tool points at
—, the generated tree and the hand-written directories alone being the
product; (c) as today.
*Recommendation*: **(a)** — it meets "nobody can tell" with `kit check`
still running in the product's CI, and keeps (b), the owner's "the
generated part could be a git repository of its own", open for the
platform's ADR.

**Q11 — Does the rule extend to the SDK's own sources?**
(a) no: the SDK is a library the tool designs (ADR 0163), its 580 generated
headers and its design files stay; the rule binds a product and what the
framework compiles into one; (b) yes: the SDK's headers and docs lose the
tool's name too.
*Recommendation*: **(a)** — a product depends on the SDK as on Spring; the
SDK's sources are not the product's code, and nothing of a header comment
is compiled.

**Q12 — An old name met at the start.**
(a) refused, naming the new one, for two minors, then forgotten (a probe
path answered and logged as deprecated over the same window, since a
refusal there is a restart loop); (b) silently ignored; (c) accepted beside
the new one for two minors.
*Recommendation*: **(a)** — (b) loses a data directory or stores personal
data in clear (§3); (c) keeps the old names in every binary for longer than
a refusal does.

## Deferred

- The platform's ADR for §5 and Q9–Q10.
- Q8 (c), the Studio out of process, if the owner wants no hook in the
  framework at all.
- The product's prose (README, docs) — its owner's, out of the guard.

## References

- [ADR 0147](0147-the-framework-is-a-module-of-the-sdk-above-pkg.md) §1,
  [ADR 0179](0179-a-store-is-a-non-generic-engine-under-a-typed-facade-and-a-subsystem-is-imported.md)
  §6, §7, §Decisions of 17 October.
- `framework/kit/CLAUDE.md`, `framework/internal/kit/introspect.go`
  (`mountHealth`, `mountIntrospection`), `framework/internal/kit/config.go`,
  `secret.go`, `seal.go`, `privacy_keys.go`, `store_sql_migrate.go`,
  `consumer.go`, `mailer.go`, `framework/telemetry/telemetry.go`
  (`Protocol`), `framework/model/internal/core/runtime.go`, `docs/api/sdk.json`,
  `docs/error-codes.yaml`, `framework/kit/testdata/cliprobe`.
- kitsunium/platform: `CLAUDE.md`; `generate/CLAUDE.md`,
  `generate/framework.go` (`DevTag`, `DevFile`, `Subsystems`),
  `design/header.go` (`GeneratedHeader`), `cmd/kit/dev.go`;
  `examples/reference`; ADR 0007, ADR 0008, ADR 0010.
- Go: `go help build` (`-overlay`); the generated-file convention,
  [golang.org/s/generatedcode](https://golang.org/s/generatedcode).
