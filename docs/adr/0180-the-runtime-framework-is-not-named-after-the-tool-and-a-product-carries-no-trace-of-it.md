# ADR 0180 — the runtime framework is not named after the tool, and a product carries no trace of it

- **Status**: Accepted on 2026-10-17 by the owner, who named the framework `framework/app` (Q1) and retained the recommendation of each of the eleven other questions (Q2 to Q12), as written; no text changed but the status, the header, each question's *Decision* line, and the passages that depended on an open question
- **Date**: 2026-10-17
- **Accepted**: 2026-10-17
- **Deciders**: SDK maintainers, on the owner's decision of 2026-10-17 (transcribed in §Context, *The owner's decision*); arbitrated by the owner, whose answers to the twelve questions are §Decisions of 17 October
- **Amends**: [ADR 0147](0147-the-framework-is-a-module-of-the-sdk-above-pkg.md) §1 — its sentence "The name `kit` is kept for the runtime package" is withdrawn; [ADR 0179](0179-a-store-is-a-non-generic-engine-under-a-typed-facade-and-a-subsystem-is-imported.md) §Decisions of 17 October — its last paragraph's "ADR to come": the runtime framework is renamed, by this record
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

Everything below follows from the owner's decision; where that decision
left a choice, the choice was a question, answered by the owner on
2026-10-17 (§Decisions of 17 October), and the text states the answer.

### 1. The runtime framework takes a name that is not the tool's

`framework/kit` is renamed, with everything beneath it:

- the facade `framework/kit` → `framework/app`;
- its subsystems `framework/kit/{store,queue,workflow,mail,jobs,server,config/yaml,config/toml}`
  → `framework/app/…`, the same leaves; `framework/kit/kittest` →
  `framework/app/apptest` (the `httptest`/`fstest` convention);
  `framework/kit/storetest` keeps its leaf;
- the implementation `framework/internal/kit` → `framework/internal/app`,
  its engines renamed so that no path segment ends in `kit`;
- `framework/model`'s `ProvenanceKit`, `PortOwnerKit`, `SettingKitDev` and
  every value spelled `"kit"` in the graph renamed after the framework; the
  graph's `Version` goes from 5 to 6;
- the design file `design/framework/kit.yaml` → `design/framework/app.yaml`.

The name is `app` (Q1, decided). The candidates, measured against the three products:

| Candidate | For | Against |
|---|---|---|
| **`framework/app`** (decided) | the owner's example; a product writes `app.Service`, `app.Store[T]`, `app.Auth()`, `app.Transact` — the words of the thing it builds, as `http.Handler` is; no standard package of that name; the SDK has no Go package named `app` (`pkg/v1/app` is a family directory with no Go code, ADR 0155) | a local variable `app` shadows the package: **14** files of the three products declare one in a file that imports the facade (renamed in the migration, or the import aliased); `app.App` and `app.AppOption` stutter (Q2) |
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
`framework/app` speak of the framework by its new name; a comment may
name the tool only where it describes the contract between the two (the
Studio hook of §4), and no such comment is compiled.

### 3. Every runtime name a product shows is renamed, and an old name is refused, never ignored

| Category | Becomes | Why it cannot be dropped silently |
|---|---|---|
| The 16 operator variables `KIT_*` | the product's own prefix, `<PRODUCT>_ADDR`, `<PRODUCT>_DATA_DIR`… — the framework already derives `<PRODUCT>_<NAME>` for a product's settings and secrets, so these names become reserved in that namespace (Q3) | an operator's `KIT_DATA_DIR=/data` ignored would start the product on an empty default directory |
| The 9 tool variables (`KIT_DEV_*`, `KIT_STUDIO*`, `KIT_ANALYZE`) | read only by the Studio hook (§4), which only a dev build links; the core reads none | — |
| `/_kit/health/{live,ready,startup}` and the reserved `/_kit/` | a reserved prefix named after the framework, `/_app/health/…` (Q4) | an orchestrator's probe on the old path would see 404 and kill the process; for the transition window the old paths answer, logged as deprecated (Q12) |
| The 18 `/_kit/api/*` Studio reads | under the same reserved prefix, and **moved out of the core** into the Studio subsystem (§4): a release binary does not compile them | — |
| The struct tag key `kit:"…"` | the framework's name, `app:"personal"` (Q7) | a `personal` field whose tag is no longer read would be stored **in clear**: the start refuses a field still tagged `kit:"…"`, naming the new key, every field at once, like `CodeSubsystemMissing` (ADR 0179 §6) |
| Graph values (`"kit"` field, provenance, port owner, reserved module, `kit.privacy`, `kit_node`, `kit_loop`), the checks named `kit` | the framework's name | a module named after the framework is reserved as `kit` was |
| The telemetry handshake `kit-telemetry/v1` | a new protocol name, `app-telemetry/v1`; the tool's collector accepts both for the transition window | the wire is the tool's to read, the name is the product's to send |
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
- **Where the hook lives** was Q8; decided: it stays the framework's
  (`framework/app/studio`), never imported by a product, injected by the
  tool.

The overlay's limits, as `go help build` states them: overlays "will not
appear when binaries and tests are run through go run and go test" — `kit
dev` runs `go build`, then the binary —, and "files beneath GOMODCACHE may
not be replaced" — the overlay adds a file to the product's own directory,
never to the module cache. A product's tests never link the Studio, as
today.

*Found by the platform at step 2 (2026-10-18, kitsunium/platform#100):* `kit
dev` builds with a copy of the product's `go.mod` and `go.sum` outside the
tree (`go build -modfile`), because a product's `go.mod` does not always list
what the Studio needs (`pkg/v1/net`, for one) — the build list can grow, which
the overlay alone could not allow; the product's `go.mod` stays unchanged.

### 5. What the tool leaves in a product's tree carries no tool name

These are the platform's to implement, in an ADR of its own; this record
states what the SDK's rule requires of them and the shape the owner
retained (Q9, Q10):

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
  content; they stay in the product's repository, and the tool's version
  pin and the CI step that installs the tool leave the product's files
  (Q10).
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

- **In the SDK**, a test of `framework/app` beside the initgraph tests
  (ADR 0179 §7), `TestAProductCarriesNoTraceOfTheTool`: it builds
  `testdata/traceprobe` — a product that declares a store, a queue, a
  workflow, a mailer, a job and a server, every subsystem but the Studio —
  with the release flags, then refuses the token in the probe's sources, in
  `go tool nm` and `strings` of the binary, in `go version -m` (the
  `kitsunium` module paths excepted), in the output of `-h`, `graph` and
  `config`, and in the names the probe's environment table and route table
  hold. Like the initgraph tests it skips under Bazel; its gate is CI's
  `test-386` job, which runs `go test` over every module. A second check
  refuses the token in the string literals of `framework/app` and
  `framework/internal/app` outside the Studio subsystem, which only a
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
| 0 | — | Done on 2026-10-17: the owner answered the twelve questions, and this record is accepted with its answers |
| 1 | SDK | The Studio's routes, handlers, capture view and tool variables move from the core into the Studio subsystem; the core keeps the probes (non-breaking: a product that imports `framework/kit/studio` keeps its Studio) — done (2026-10-17, §Step 1, done) |
| 2 | platform | `kit dev` injects the hook by overlay; the generator stops writing `dev_gen.go`; `kit check` refuses a product's import of the Studio subsystem; the todo deletes `kitdev.go`, moderation its import |
| 3 | SDK | The rename of §1, a design edit first (ADR 0163, 0167, 0168): `design/framework/app.yaml`, `kit gen`, `make api`, `make docs-readme`; the facade's and implementation's paths; `model` `Version` 6; the scripts, `BUILD.bazel`, `//:audit_sources`, `tools/alloc-lane-targets.txt` and `codes.ranges` owners re-pointed; the three database connectors follow; the texts of §2 reworded — done (2026-10-18, §Step 3, done) |
| 4 | SDK | The runtime names of §3: the prefix, the routes, the tag key, the graph values, the telemetry name; the refusals of an old name at the start (§3, Q12) — done (2026-10-18, §Step 4, done) |
| 5 | SDK | The names at rest (Q5, Q6): the tables, the migration set, the secret prefix, the dev data directory — renamed by a framework migration —, the domain-separation labels re-sealed under new ones, the old ones removed in a later minor (Q6) — split in three: 5a done (2026-10-18, §Step 5a, done): the tables, the migration set, the dev data directory and the privacy service moved; 5b done (2026-10-18, §Step 5b, done): the secret prefix, every data key re-wrapped under `app-data-key`, and the label of a box sealed under data-key itself; 5c to come: the labels of the references and the privacy service's ids, re-sealed, and the module name `kit` released |
| 6 | SDK | `TestAProductCarriesNoTraceOfTheTool`, with an empty allow-list; it can land earlier with an allow-list shrinking step by step, as ADR 0179's `stillInCore` did |
| 7 | platform | Its ADR for §5: the header, the digest line, the skeletons, the seed variable, the design's `apiVersion`, the lock, the version pin; the analyzer and `subsystem.Table` on the new paths; `internal/modelts` regenerated for `Version` 6; the Studio's client on the new prefix; the reference regenerated, and its guard |
| 8 | products | todo, moderation, statusline: the import path and `kit.` → `app.` (a rewrite of the import path and the package qualifier, then the 14 shadowing files by hand), the struct tags (moderation's 174), the variables in Dockerfiles, compose files and CI, `.gitignore` |

### Step 1, done (2026-10-17)

The Studio's routes, their guard and their handlers, and the reading of the
tool's variables, are `framework/internal/kit/studiokit`'s, which
`framework/kit/studio` plugs in as it is imported (`plug.Studio`, a
`StudioDriverConfig`), on the model of ADR 0179's steps:

- **The routes.** `studiokit`'s `mount` registers every route the core's
  `mountIntrospection` registered, in its order and with its patterns — the
  graph, the event stream, traces, one trace, source, instances, items, the
  process, goroutines, the heap profile, logs, the mailbox and one mail,
  and, on the store subsystem, the databases, former values, versions, the
  trash and the Privacy page —, the not-found answers under `/_kit/api/` and
  `/_kit/`, and the guard (loopback unless `KIT_STUDIO_REMOTE=on`, the Host
  allowed, the Studio's CSP, `nosniff`, `no-referrer`). The event stream
  (the SDK's `net/sse`) and the profiler (the SDK's `profiling`) are now
  `studiokit`'s own, under its routes: `plug.OpenEventStream`,
  `plug.StudioProfiler`, `plug.EventStream`, `plug.ProfilerConfig` and
  `plug.SourceFunc` are gone.
- **What the routes read.** A route reads the running app through a
  `plug.StudioHost`, which the core implements (`studio_host.go`,
  `studioHost`): the graph, the phase, the clock, the hub's events, traces
  and logs, a workflow's instances, a store's items redacted, a source file
  the graph names — still read through `os.Root`, never a link
  (`readSource`) —, where a function lies, the mailboxes, the store tools'
  reads (`studioDatabases`, `studioFormer`, `studioRevisions`, `studioTrash`,
  `privacyView`), the guard's facts, and kit's answers (`Reply`, `Forbid`,
  `Fail`, `NotFound`, `Invalid`, `ProfileUnreadable` — `CodeProfileRead`
  stays kit's —, `Debug`). What the app keeps stays beside its state, as
  ADR 0179 §3 lets code that names no SDK type of the subsystem stay; it is
  reached only through the host, which only `mountStudio` makes, which only
  `EnableStudio` stores — so a program that does not import
  `framework/kit/studio` links none of it. A query parameter judged against
  what the app keeps — a log level, a page of the trash, two version
  numbers — is handed to the host as received, so that a request with two
  faults is answered with the fault kit answered first.
- **The tool's variables.** `KIT_DEV_BUILT_AT`, `KIT_DEV_BUILD_MS`,
  `KIT_DEV_REBUILDS`, `KIT_DEV_CHANGED`, `KIT_DEV_CHANGED_MORE`,
  `KIT_DEV_FORCED`, `KIT_STUDIO`, `KIT_STUDIO_REMOTE` and `KIT_ANALYZE` are
  read by `studiokit` alone (`studioEnv`, `studioSettings`, `devForced`,
  `devBuildOf`), behind `Env`, `Settings`, `Forced` and `DevBuild`. The core
  resolves the Studio from what they ask (`plug.StudioEnvValue`), its zero
  value without the Studio — a run in which none is set —, and keeps the
  code's options (`kit.Studio`, `kit.Analyze`), the production warning and
  the start's warning that names the import.
- **What stays in the core.** The three probes and the reserved `/_kit/`
  outside dev (`mountHealth`, renamed by step 4). The mailbox's record of a
  captured mail — its headers, `X-Kit-Attachments` among them — stays the
  mailer's: it is also what `Mailer.Captured` hands a product's tests, which
  are not the Studio, so the header is renamed with the runtime names
  (step 4) rather than moved here; the mailbox's routes, the capture view
  the Studio reads, moved. The announcement in dev of the Studio's API
  (`/_kit/api/`) and its sentence are §2's text and step 4's prefix.

**Non-breaking.** A product that imports `framework/kit/studio` keeps its
Studio: the same routes, answers and guards — the core's suites that serve
the Studio (`studio_test.go`, `devtools_test.go`, `trash_view_test.go`,
`revisions_view_test.go`, the mailbox and source suites), whose binary
imports the package, pass unchanged, and `studiokit`'s own suite holds the
guard, the mailbox, the source endpoint's refusals and the tool's variables
over a fake host. A product that does not import it reads none of the
tool's variables: its settings carry no row of them, its graph no dev
build, and `KIT_STUDIO=off` no longer silences the start's warning that the
Studio is not linked.

**Held by the initgraph test.** `studioCode` lists one function of each part
of the Studio's code, by its symbol — `studiokit`'s `mount`, the guard's
handler, the event stream, the source endpoint, the mailbox, the heap
profile, `studioEnv`, `studioSettings`, `devForced`, `devBuildOf`, and the
core's `mountStudio`, `studioHost.Source`, `studioHost.Reply`, `studioLogs`,
`studioDatabases`, `studioTrash`, `readSource` —, and `toolVariables` the
names of the tool's variables. `linksNoStudio` refuses every one of them,
symbol or name, in `cliprobe`, `coreprobe`, `storeprobe`, `queueprobe`,
`workflowprobe`, `mailprobe` and `jobsprobe`;
`TestAProductThatImportsTheStudioLinksItsLanguages` requires every one in
`studioprobe`. Run against the tree before this step, the test fails where
it should: in `cliprobe` and in `coreprobe` each of the seven entries of
`toolVariables` matches — read as substrings, `KIT_DEV_CHANGED` and
`KIT_STUDIO` also cover `KIT_DEV_CHANGED_MORE` and `KIT_STUDIO_REMOTE`, so
the seven entries are the nine names the table below counts —, which the
core read.

Measured as ADR 0179's steps (Go 1.27.2, `GOTOOLCHAIN=local`, `-trimpath`,
`GOWORK=off`, MB = 10⁶ bytes, text symbols `T`/`t` by `go tool nm -size`),
before on `origin/main` at ff3fd191:

| | cliprobe before | cliprobe after | coreprobe before | coreprobe after | studioprobe before | studioprobe after |
|---|---|---|---|---|---|---|
| packages (`go list -deps`) | 341 | 341 | 340 | 340 | 355 | 355 |
| file | 14.451 | **14.447 (−0.004)** | 14.440 | 14.433 | 16.874 | 16.931 (+0.057) |
| `.text` | 4.545 | 4.542 | 4.542 | 4.540 | 5.359 | 5.376 |
| text symbols | 4.394 | **4.392 (−0.002)** | 4.391 | 4.389 | 5.183 | 5.199 (+0.016) |
| kit's core | 0.4276 | 0.4256 | 0.4275 | 0.4255 | 0.7388 | 0.7281 |
| `studiokit` | 0 | 0 | 0 | 0 | 0.0133 | 0.0404 |
| names of the tool's variables in the binary | 9 | **0** | 9 | **0** | 9 | 9 |

**Reading.** The measure corrects §Context: before this step a product that
did not import `framework/kit/studio` already linked none of the Studio's
handlers — `mountIntrospection` was reached only through `EnableStudio`
since ADR 0179 —, so "compiled into every production binary" held of the
core's package, not of the linked program. What such a product did link of
the Studio was the reading of the tool's variables (`devBuildOf`,
`changedFiles`, `resolveStudio`, `devForced`: 2.5 kB of code, 2.1 kB net) and their nine
names, which it no longer holds; its code is 2.1 kB smaller and its file
4.0 kB. The step's gain is where the source lies: the Studio's routes,
handlers and literals are out of the core's package, as §6's literal check
(step 6) needs. A product shown in the Studio links 0.016 MB more code —
the host's methods, each route encoding its own answer, and the event
stream and profiler without their former hooks. Not measured: the latency
of a fresh process, which nothing here moves.


### Step 3, done (2026-10-18)

The rename of §1, the design first: `design/framework/kit.yaml` is
`design/framework/app.yaml`, every package it declares at its new path, and
`design/sdk.yaml`'s `codes.ranges` re-point their owners — `0.4.2` at
`framework/internal/app`, `0.4.4` at `framework/app/storetest` — with every
value unchanged (ADR 0160 §3: `docs/error-codes.yaml` lists the same 790
codes under the new paths). `kit gen` (rc.9, the pin) wrote the pins, the
codes, the doc slots and `internal/kernel/errs/codes_gen_test.go`; `make api`
wrote `docs/api` and `docs/error-codes.yaml`; `make docs-readme` the
READMEs.

| Was | Is |
|---|---|
| `framework/kit` (package `kit`) | `framework/app` (package `app`) |
| `framework/kit/{store,queue,workflow,mail,jobs,server,studio,config/yaml,config/toml,storetest}` | `framework/app/…`, the same leaves |
| `framework/kit/kittest` | `framework/app/apptest` |
| `framework/kit/testdata/*probe` | `framework/app/testdata/*probe` |
| `framework/internal/kit` (package `kit`) | `framework/internal/app` (package `app`; the facade imports it as `iapp`, which was `ikit`) |
| `framework/internal/kit/{storekit,queuekit,workflowkit,mailkit,jobkit,serverkit,studiokit}` | `framework/internal/app/{storeengine,queueengine,workflowengine,mailengine,jobengine,serverengine,studioengine}` — the role the package plays (ADR 0179: an engine under a facade), so no path segment ends in `kit` |
| `framework/internal/kit/plug` | `framework/internal/app/plug` |
| `model.ProvenanceKit`, `model.PortOwnerKit`, `model.SettingKitDev` (and their core twins) | `model.ProvenanceApp`, `model.PortOwnerApp`, `model.SettingAppDev` |
| `model.Version` 5 | 6 |

Every identifier of the facade keeps its name: `kit.Store[Order]` is
`app.Store[Order]`, `kit.App` is `app.App`. The step changes 686 files, 527
of them moves: the packages and their tests, the connectors' imports, the
BUILD files (sorted as gazelle sorts them), `//:audit_sources`,
`.ktn-linter.yaml`'s paths, the release scripts' tests and the docs. No
`framework/kit` is left: a product that still imports it fails to build,
the break Q2 chose over an alias.

**`Version` 6 before its values.** The graph's version moves here, as the
migration table says, because a source position in the framework's code
names `framework/app` and `framework/internal/app` from this step on; the
values §3 renames — `"kit"` as a provenance, a port owner, a reserved
module, the `kitdev` setting origin — keep their spelling until step 4, under
the same version.

**The texts of §2.** Every string the core compiles names the framework or
the product: the Private prefix `kit: ` is `app: `; the word kit in a Public
or Private text, a log message, a refusal, the English catalogue (and its
French twin) is "the framework"; the help's first line is the product's name
alone; "a secret kit generates" is "a secret the product generates"; the dev
announcement no longer names `kit dev`. The design's doc comments of
`framework/app` and of its implementation speak of the framework by its name,
naming the tool only where they describe the contract with it. What is left
is §3's and step 4's — `KIT_*`, `/_kit/`, the `kit` tag key, the graph's
`"kit"` values, the checks and the module named `kit`, the `config`
command's owner column — and step 5's — the `kit_*` tables, the `kit` set,
the `kit-` secret prefix, `.kit/data`, the `kit/v1` and `kit:` labels; and
the Studio subsystem's own sentence about the tool, which §6's literal check
leaves out.

**A local `app` in the SDK's tests.** Where a test declared a variable `app`
in a file that now imports package `app`, the variable is `a` (`prod` where
`a` was taken), in every such test file and in the conformance suite. An example in a
doc comment declares `a := app.NewApp(…)`.

**Verified.** `GOWORK=off go test -race ./...` in the root module and in the
four connector modules (`postgres` and `mysql` also against servers in
Docker), `kit gen -check`, genindex `-check-api -markers -digests`, the
guards of `scripts/pre-commit/`, `make guard`, `make doclinks`, `gofumpt -l`
and `make lint-ktn-check`.

### Step 4, done (2026-10-18)

The runtime names of §3, in `framework/internal/app/renamed.go` and the code
that reads each name; the design first for what is exported
(`design/framework/{app,model,telemetry}.yaml`, `kit gen` rc.9, `make api`,
`make docs-readme`).

| Was | Is |
|---|---|
| the 16 operator variables `KIT_ADDR`, `KIT_ALLOWED_HOSTS`, `KIT_DATA_DIR`, `KIT_DATA_KEY`, `KIT_ENV`, `KIT_INDEX_KEY`, `KIT_LOG_LEVEL`, `KIT_RETENTION`, `KIT_SECRETS`, `KIT_SECRETS_KEY`, `KIT_SECRETS_KEY_FILE`, `KIT_SMTP_URL`, `KIT_SMTP_URL_FILE`, `KIT_TELEMETRY`, `KIT_TELEMETRY_GIDS`, `KIT_TRUST_PROXY` (and the `_FILE` twins of the two keys, `KIT_DATA_KEY_FILE`, `KIT_INDEX_KEY_FILE`) | `<PRODUCT>_ADDR`… — the product's prefix as its settings and secrets already had it (`appPrefix`: the name upper-cased, every other character `_`, `TODO` for "todo", `MY_APP` for "my-app") |
| `/_kit/health/{live,ready,startup}`, the reserved `/_kit/` | `/_app/health/{live,ready,startup}`, the reserved `/_app/` |
| the Studio's reads `/_kit/api/*`, its not-found answers under `/_kit/api/` and `/_kit/`, the host guard "only `/_kit/`" | `/_app/api/*`, `/_app/api/`, `/_app/`, "only `/_app/`" — in `studioengine`, which a release binary does not link (step 1) |
| the struct tag key `kit:"…"` | `app:"…"`, the model's own fields included |
| `model.ProvenanceApp`, `PortOwnerApp` = `"kit"`; `SettingAppDev` = `"kitdev"` | `"app"`; `"appdev"` |
| the graph's `app.kit` (`AppMessage.Kit`) and `app.build.kit` (`BuildMessage.Kit`) | `app.app` and `app.build.app` (`AppMessage.App`, `BuildMessage.App`), under `Version` 6 as the migration table says |
| `model.VarEnv`… = `"KIT_ENV"`… | the name after the product's prefix: `VarEnv` = `"ENV"`, `VarAddr` = `"ADDR"`, `VarDataDir` = `"DATA_DIR"`, `VarTrustProxy`, `VarAllowedHosts`, `VarLogLevel`, `VarSMTPURL`, `VarRetention` likewise; a setting's `Name` in the graph is the product's variable, `TODO_DATA_DIR` |
| the readiness and liveness checks named `kit`; the loops' library `kit`; the `config` command's owner column `kit`; the `secrets` command's "made by" `kit`; the privacy journal's actor when no node acts, `kit` | `app` |
| the pprof labels `kit_node`, `kit_loop` (`plug.LabelNode`, `plug.LabelLoop`) | `app_node`, `app_loop` |
| a captured mail's header `X-Kit-Attachments` | `X-Attachments` |
| `telemetry.Protocol` = `kit-telemetry/v1` | `app-telemetry/v1`; the wire is unchanged |
| the help's `KIT_*` lines | `<PRODUCT>_*`, the product's own prefix printed, each name on a line of its own |

**What keeps the tool's name, by §3.** The nine tool variables —
`KIT_DEV_BUILT_AT`, `KIT_DEV_BUILD_MS`, `KIT_DEV_REBUILDS`,
`KIT_DEV_CHANGED`, `KIT_DEV_CHANGED_MORE`, `KIT_DEV_FORCED`, `KIT_STUDIO`,
`KIT_STUDIO_REMOTE`, `KIT_ANALYZE` — are the tool's: §3 does not rename them,
`studioengine` alone reads them since step 1, and a release binary links none.
`model.VarStudio`, `VarStudioRemote`, `VarAnalyze` and `VarDevForced` keep
their whole names. `KIT_DEV_FORCED` now lists the product's variables the
tool set (`TODO_ENV,TODO_ADDR,TODO_DATA_DIR`): only those three are labelled
`appdev`.

**What stays for step 5, and why.** The names at rest: the `kit_*` tables, the
`kit` migration set, the `kit-` secret prefix (`kit-smtp-url`,
`kit-data-key`, `kit-index-key`), `.kit/data`, the `kit/v1` and `kit:`
labels. One graph value of §3 is among them: the framework's own service
`kit.privacy`, whose stores rest under its name (`kit.privacy/keys.json` in
the data directory, `kit_privacy_*` tables on a database) — renamed without
step 5's migration, a product would start without its data keys and lose the
personal data they seal. The service keeps its name until step 5, and so does
the reservation of the module name `kit`, which would otherwise let a
module's service take a name under `kit.`; `app` is reserved beside it.

**An old name met at the start is refused** (Q12), every one at once, with
`CodeRenamed` (`0.4.2.85`, allocated after `0.4.2.84` in
`design/framework/app.yaml`, re-exported by `framework/app`; reason
`OLD_NAME`, public "a name the framework no longer reads was met at the start:
use the name that replaces it"): the `DiagnosticsError` gains `Renamed`, each
entry `"<old> -> <new>"`, sorted, and its `Unwrap` carries `CodeRenamed` —
joined with `CodeSubsystemMissing` when the start found both.

- *A variable.* A `KIT_<STEM>` of the 18 above set non-empty in the
  environment is refused before anything is read or opened (`resolve`, so the
  start and every command that reads the configuration — `config`, `graph`,
  `secrets`, `migrate`, `privacy`, `watch`, `revisions`, `data copy` —, and
  the healthcheck, which would probe a wrong address), naming
  `<PRODUCT>_<STEM>`: "KIT_DATA_DIR is set, but the framework no longer reads
  it: it reads TODO_DATA_DIR — rename it where the product is deployed". A
  product whose prefix is `KIT` reads them as its own and is not refused.
- *A tag.* A field still tagged `kit:"…"`, in any type a declaration shows or
  keeps, nested ones included, is refused at the start with the declaration
  problems, every field at once, like `CodeSubsystemMissing`:
  `shop.Report.Email is tagged kit:"personal", which the framework no longer
  reads: tag it app:"personal"`.
- *A probe path.* The former `/_kit/health/{live,ready,startup}` answer as the
  new ones; the first request of the run to each is logged at warning level,
  "a probe asked a deprecated path, which answers for two minors more: probe
  the new one", with the path asked and the one to use. The rest of `/_kit/`
  stays reserved — a product route under it is refused, a request there
  answers 404 — over the same window.

**The product's namespace.** Since the framework's own variables now share the
product's prefix, they are reserved in it, as Q3 says: a setting, a secret or a
database URL of the product read from one of them (`app.Database("smtp", …)`,
whose URL would be `TODO_SMTP_URL`; a setting `log-level`) is refused at the
start (`setting.reserved`, `secret.reserved`), and none of them is warned of
as a stray variable.

**Texts.** A sentence the start or a command says names the product's variable
itself (`TODO_DATA_DIR`); a Public text, a literal by rule 4, writes
`<PRODUCT>_SECRETS_KEY` and carries the variable's name in a field
(`variable`).

**Verified.** `GOWORK=off go test -race ./...` in the root module and in the
four connector modules, `kit gen -check`, genindex `-check-api -markers
-digests`, the guards of `scripts/pre-commit/`, `make guard`, `make
doclinks`, `gofumpt -l` and `make lint-ktn-check`. The refusals were seen red
before green: with the two checks stubbed out, the tests of the variables and
of the tags fail where they should.

### Step 5a, done (2026-10-18)

Step 5 is two changes, because two of its names are not names at rest
alone. A table, the migration set, a directory are found by their name and
hold bytes that do not mention it: moving them is a rename, and renamed
back, the data is the former release's again. The secret prefix and the
labels are bound into what is sealed: the SDK's keyring binds the secret's
name into every box and every wrapped data key
(`internal/service/security/secret`, `Keyring.binding`: the name, then the
version), so `kit-data-key` renamed is every data key re-wrapped and every
box under it re-sealed — Q6's re-seal, not Q5's rename —, and the store
offers no rename that keeps a version's number, which the binding names
too. 5a moves what moves; 5b (to come) re-seals what is sealed, with the
secret prefix. 5a re-seals nothing, which is what keeps it undoable.

| Was | Is |
|---|---|
| the framework's version table `kit_migrations`, its set named `kit` | `app_migrations`, the set named `app`; the history taken whole — every version, name and instant |
| its registry `kit_tables` (and `kit_tables___ix`) | `app_tables`, each record re-keyed under its table's present name |
| a consumer's queue `kit_queue__<service>__<name>` (a command's `__command`, a mailer's outbox `__outbox`) | `app_queue__…` |
| the privacy service's stores on a database `kit_privacy__<store>` (with `___ix`, `___vs`) | `app_privacy__…`; on PostgreSQL the indexes and constraints named after a table follow it |
| dev's default data directory `.kit/data` | `.app/data`; `.kit` goes only when nothing is left in it — the tool's own state stays |
| the framework's own service `kit.privacy`, its directory `<data>/kit.privacy/` | `app.privacy`, `<data>/app.privacy/` |

The ADR named the categories, not the names; each takes the framework's name
(Q1), as step 4's `/_app/` and `app:"…"` did: `app_migrations`, `app_tables`,
`app_queue__`, `app_privacy__`, the set `app`, `.app/data`, `app.privacy`.
The module names `app` and `kit` are both reserved: `app` because its
version table would be `app_migrations`, `kit` because what a former
release kept may still be met under it — a database not yet started, a
rolled-back one — and because the privacy service's sealed values stay
bound to `kit.privacy` until 5b; it is released with the labels.

**The move, in the data directory** (`framework/internal/app/renamed_rest.go`).
When the app resolves — the start and every command, before anything reads
the directory —, dev's `.kit/data` (only when no directory is named: a
`<PRODUCT>_DATA_DIR` or an `app.DataDir` is the operator's) and the data
directory's `kit.privacy` are moved by rename(2), never copied: every byte
is the one the former release wrote. Both moves are judged before either
runs; when the second fails once the first ran, the first is renamed back.

**The move, on a database** (`storeengine/rename.go`, through
`plug.StoreDriverConfig.FormerTables`/`RenameTables`). The core names every
table it keeps both ways (`renamePlan`; a `kitTable`'s `former`); the store
subsystem reads the catalogue (`pg_tables`, `information_schema.tables`,
`sqlite_master`) and moves what is still under a former name, a document
store's `___ix` and `___vs` with their table. The move is a migration of
`kit_migrations` (version `math.MaxInt64`, above every generation the
framework gives): the SDK's migrator holds that table's lock — the one a
former release takes, so the two never run together (ADR 0055 §D7, ADR 0140
on SQLite) — and records the move in the transaction that makes it. In it:
every version `kit_migrations` records is copied into `app_migrations`
(which the SDK's migrator made, with its own DDL), the tables are renamed —
on MySQL in one `RENAME TABLE`, which it runs whole or not at all —,
PostgreSQL's indexes named after a table are renamed with it (an index
renamed renames its constraint), and the history's names of the tables'
migrations are the present ones (`docstore kit_tables` → `docstore
app_tables`). Once recorded, `kit_migrations` is dropped; a drop that fails
is done by the next start. Then the core re-keys the registry's records
(`tableRegistry.rekey`). No row of a moved table is read or written by the
move. Every step is idempotent — a table already moved is not moved again,
a version already copied is not copied twice —, so a run MySQL's implicit
commits stopped completes when it runs again; on PostgreSQL and SQLite a
failed step rolls the whole move back. A table the build no longer declares
keeps its name: it is the product's to drop.

**Never replacing** (`CodeRenameTarget`, `0.4.2.86`, reason
`RENAME_TARGET_TAKEN`, public "data a former release kept is also under its
present name: nothing was moved"). When a former name and its present one
both exist — a directory, a table, one of its derived tables —, the start is
refused, both named, and nothing moves: on a database the refusal is judged
on the pool before anything is made, and again under the lock. Which one
holds the product's data is the operator's to say.

**A move that did not complete** (`CodeRenameFailed`, `0.4.2.87`, reason
`RENAME_FAILED`, public "data a former release kept could not be moved to
its present name: it stays where it was"): a rename the system refused, a
statement the database did not run. What was not moved stays under its
former name, readable, and the next start resumes. A database that migrates
manually (`<name>-migrate: manual`) refuses to start while a table is under
its former name (`DATABASE_MIGRATIONS_PENDING`, naming `<product> migrate
up`); `migrate status` lists them, `migrate up` moves them.

**What does not move in 5a, and why.** The secret prefix `kit-`
(`kit-data-key`, `kit-index-key`, `kit-smtp-url`): the keyring binds the
name, above. The labels `kit/v1 sealed at rest`, `kit/v1 subject
reference`, `kit/v1 record reference`, `kit/v1 database index key`,
`kit:subject`, `kit:fingerprint`. And what the privacy service's own stores
are bound to: a sealed value is bound to its store's node id, so the
journal's and the holds' boxes keep binding `kit.privacy/store/…`
(`sealedAs`), and a record's own data key keeps its reference under that id;
a journal entry a former release wrote names `kit.privacy/store/keys`, and
its chain forbids rewriting it (`ownService` reads both). Re-sealing these
is 5b; until then the binary still holds those strings, which step 6's
allow-list names.

**Held by** — on a former release's data, not on this release's renamed by
hand: `framework/connectors/sqlite/testdata/former`, written on 2026-10-18
by the release before this step (a product with sealed personal fields, a
subject, revisions, former values, the trash, an erased person, a held case,
a held tally, the privacy journal, a letter delivered and one still queued;
on SQLite with the privacy service on the file, and in the data directory
alone), with what that release read of it. `former_move_test.go`: `migrate
up` moves it and every row of every table and every file of the data
directory is the former one, byte for byte, but the registry's keys and the
history's names; `privacy journal -verify` and `privacy holds` print what
the former release printed; the product reads the same cases, versions,
former values, trash and a person's data opened, the erased person erased,
the kept letter delivered; a second start moves nothing and no name of the
database contains `kit`; a present name taken refuses the start and changes
nothing; a renaming the database refuses half-way leaves every table where
it was and the next start moves them; and renaming back restores the
fixture byte for byte. The PostgreSQL and MySQL suites (`former_test.go`,
servers in Docker) make the former layout by renaming back what this release
wrote, then hold the same: every row as it was, no table, index or
constraint naming `kit`, the refusal, and the half-way failure resumed.
`renamed_rest_test.go` holds dev's directory. Seen red before green: with
`sealedAs` taken out, the journal and the holds no longer open; with the
move taken out, every test of it fails; on PostgreSQL without the index
renames, the constraints still name `kit`.

**Verified.** `GOWORK=off go test -race ./...` in the root module and in the
four connector modules (`postgres` and `mysql` also against servers in
Docker), `kit gen -check`, genindex `-check-api -markers -digests`, the
guards of `scripts/pre-commit/`, `make guard`, `make doclinks`, `gofumpt -l`
and `make lint-ktn-check`.

### Step 5b, done (2026-10-18)

What 5a left is two mechanisms, not one, so it is two changes. What a
**keyring wraps** is bound to the secret's name: the SDK's keyring binds
the name and the version into every box and every wrapped data key
(`Keyring.binding`). Moving the data keys from `kit-data-key` to a secret
of another name is a re-wrap — the operation data-key's rotation already
runs every 30 days, one compare-and-swap per key, no box touched. What a
**reference names** is another matter: the HKDF labels `kit/v1 subject
reference` and `kit/v1 record reference` derive the name of every data key
(its reference is its key in the store of data keys and the subject in the
header of every box it sealed), the subject index, the holds' keys and the
references the privacy journal records — and the journal's chain hashes
those references, so it cannot be rewritten. Changing those labels renames
every data key, re-seals every box under it, and leaves the journal naming
references nothing derives any more: a different mechanism, a larger risk,
reviewed apart. 5b does the first with the secret prefix; 5c does the
second with the privacy service's ids (`sealedAs`), whose own-key
references are made the same way.

| Was | Is |
|---|---|
| `kit-index-key` in the environment's store | `app-index-key`, its value copied once (its name binds nothing) |
| `kit-smtp-url` | `app-smtp-url`, copied once, and again when the former is the newer — an operator who went back to the former release and set it there |
| `kit-data-key` | `app-data-key`, a new secret generated under its name, every data key re-wrapped under it; `kit-data-key` read, never wrapped under; retired — one fresh version, the others pruned — on the schedule its rotation would have pruned them on |
| a box sealed under data-key itself, bound to `kit/v1 sealed at rest` | bound to `app/v1 sealed at rest`; the former label read |
| the prefix a product's secret, setting or database may not take: `kit-` | `app-` and `kit-` |
| — | `privacy rewrap [-back]`: the pass now, or every data key back under `kit-data-key` |
| the SDK's `secret.SubjectKeysConfig`, `secret.RewrapReport` | `SubjectKeysConfig.Former`, the root a renamed root secret replaces; `RewrapReport.FromFormer` |

The record named the categories; each takes the framework's name, as 5a's
did: `app-…`, `app/v1 sealed at rest`. A data-key pinned by
`<PRODUCT>_DATA_KEY` is read under its bare name, `data-key`, which did not
change: nothing it wrapped moves, and only the label of a box sealed under it
does.

**The copy** (`framework/internal/app/renamed_secrets.go`,
`moveFormerSecrets`). When the environment's store is opened — the start and
every command —, before anything reads it, the index key and the SMTP URL a
former release kept are copied to their present names. Both are judged
before either is written. An index key kept under both names with two
values refuses the start with `CodeRenameTarget` and nothing is copied:
every reference the framework keeps was made under one of them, and which
is the operator's to say. A copy the store refuses is `CodeRenameFailed`;
the former stays where it was, and is read (`kitSecret`) until the next
start copies it.

**The re-wrap** (`SubjectKeysConfig.Former`, ADR 0142's engine). The sealer
is given `kit-data-key` as the root `app-data-key` replaces, when the
environment's store keeps one (`formerDataKeyring`): a data key wrapped under
it unwraps, under any of its versions, and nothing is ever wrapped under it.
The start's re-wrap pass (`rewrapDataKeys`, which ran at every start
already, opening every key) moves every key only `kit-data-key` opens under
`app-data-key`'s newest version, one compare-and-swap each, and says how
many on the app's log (`RewrapReport.FromFormer`). The durable mark of the
progress is the wrapped key itself: each is under one root or the other,
replaced whole or not at all, and the pass reads which — a separate marker
would be a second copy of what each record already says, and could
disagree with it. A pass stopped anywhere, by a crash or a failed write,
leaves every key readable, and the next start moves the rest.

**Read both, write one.** For the window (Q12): a key under `kit-data-key`
unwraps; a box under data-key itself opens under the present label, then
under the former one by data-key's keyring and by `kit-data-key`'s
(`openRoot`); `kit-<name>` is read while its copy is not made. Nothing is
written under a former name or label: a new key is wrapped under
`app-data-key`, a new box bound to `app/v1 sealed at rest`, the index key
made and `secrets set` writing under `app-…` — `secrets set kit-smtp-url`
is refused, naming `app-smtp-url`.

**An erasure stays an erasure, at once, under both roots.** A subject has
one wrapped key in the store of data keys, under whichever root wraps it —
never one under each. A person erased after the move has their key under
`app-data-key`; the erasure destroys it and folds the store, so nothing the
product keeps opens under either data-key, and `privacy rewrap -back` has
nothing of theirs to move. A key destroyed before the move is not in the
store: the pass has nothing to move and makes nothing.

What the product cannot reach is a copy outside its store — a backup of the
store of data keys, a database's own history of a row (its write-ahead log,
a row version not yet vacuumed). ADR 0142 bounds it: such a wrapped copy
opens only while the root version that wrapped it is kept, and data-key's
rotation prunes a version after Keep × RotateEvery (90 days by default).
`kit-data-key` no longer rotates, so it would have kept every version it had
for the whole window. It is retired on the schedule its rotation would have
followed instead (`retireFormerDataKey`). After a clean re-wrap pass that
found no key under it, each of its versions older than Keep × RotateEvery
is pruned. When all of them are, one fresh random version is written under
the name first: the store offers no deletion, so this is the only way to
destroy the newest version's material. A prune that fails is retried by the
next clean pass, since each version is judged on its own age. After that, a backup's copy of an erased person's key wrapped under
`kit-data-key` opens nothing, as it would not have under the former
release's own rotation. Nothing the running product needs is under those
versions: every data key moved. A box under data-key itself sealed under
them is older than the bound, which data-key's own pruning has always
allowed to read as empty (ADR 0006 §4). The way back still works:
`privacy rewrap -back` wraps under the fresh version, which the former
release opens. The retirement runs after every clean pass, so a fresh
version that wrapped nothing is retired in its turn.

**What 5b does not touch.** The privacy journal (its chain is over the
entries' plaintext and its references, neither of which changes), the holds,
every box under a data key (the re-wrap moves the key, never a box), and
what a former release kept: its three secrets stay byte for byte until
`kit-data-key`'s retirement, so the release before this one still finds
them.

**What is left for 5c, and for the minor that ends the window.** 5c: the
labels `kit/v1 subject reference`, `kit/v1 record reference`, `kit/v1
database index key`, `kit:subject` and `kit:fingerprint`, the privacy
service's ids its sealed values are bound to (`sealedAs`), and the module
name `kit`, released with them. The minor that ends Q12's window removes
the former names read here — `formerSecretPrefix`, `formerSealDomain`,
`formerDataKeyring` — and deletes the former secrets; its start must refuse,
never read as erased, while a data key is still wrapped under
`kit-data-key`, a `kit-…` secret is not copied, or a box under data-key
itself still carries the former label (a message kept in a queue, a dead
letter). Its test is this step's fixture: started on
`testdata/former-keys` unmigrated, that minor refuses and changes nothing;
on a copy this release migrated, it reads as it was.

**Measured.** In memory (the SDK's engine alone, AMD EPYC 7351P, Go 1.27.2),
moving 20 000 data keys from the former root took 106 ms, 5.3 µs a key; the
pass after, which finds nothing to move, 38 ms, 1.9 µs a key — the cost every
start already paid. Not measured: the store's write, one per key, once — on
the data directory an entry of the store's overlay (ADR 0110), on a database
an `UPDATE` of one row.

**Held by** — on a former release's data: `framework/connectors/sqlite/
testdata/former-keys`, written on 2026-10-18 by the release before this step
(the product of 5a's fixture with its secrets in the file store beside the
data — `kit-data-key` rotated once, `kit-index-key`, `kit-smtp-url` — and a
notice about nobody kept in its queue, sealed under `kit-data-key`'s first
version with the former label), on SQLite and in the data directory alone,
with what that release read of it. `former_keys_move_test.go`: the commands
then the product read the journal, the holds, the cases, versions, former
values, trash and a person's data as the former release did, the erased
person erased, the kept letter and notice delivered; the copies made and
`app-data-key` made, the former secrets byte for byte; a second start
changes nothing; with the former secrets deleted everything still reads; an
index key under both names is refused and nothing changes; a copy refused
half-way resumes; `app-data-key` rotates and prunes once moved; `privacy
rewrap -back` leaves every data key under `kit-data-key`. The PostgreSQL and
MySQL suites (`former_keys_test.go`, servers in Docker) make the former
state by `privacy rewrap -back` from what this release wrote, the data keys
on the database, and hold the same. `renamed_secrets_internal_test.go`: the
move stopped after each of its writes, then resumed by a start, every card
read as it was; a box under data-key sealed under the present label alone;
the former secrets read until copied, and copied once. The SDK's
`subjectformer_external_test.go` holds `Former`. Seen red before green:
without `Former` the fixture's journal does not open; without the former
label the kept notice reads as empty; without the former name's read the
index key and the SMTP URL are not found before their copy; the SDK's
engine without its fallback opens nothing the former root wrapped.
`former_keys_erase_test.go`: a person erased after the move, their boxes
restored from a backup taken just before the erasure beside the keys as the
erasure left them, opens under neither data-key — with both, with each
alone, after `privacy rewrap -back`; seen red with the key destruction taken
out. `renamed_secrets_erase_internal_test.go`: a backup of the data taken
before the move, restored beside the present secrets, still opens an erased
person within ADR 0142's bound, and nothing of them once `kit-data-key` is
retired past it; seen red with the retirement taken out. A retirement whose
prune failed after its fresh version was written is finished by the next
pass; seen red when only the newest version is judged.

**Verified.** `GOWORK=off go test -race ./...` in the root module, in
`internal`, and in the four connector modules (`postgres` and `mysql` also
against servers in Docker), `kit gen -check`, genindex `-check-api -markers
-digests`, the guards of `scripts/pre-commit/`, `make guard`, `make
doclinks`, `gofumpt -l` and `make lint-ktn-check`.

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
| Steps 4–5 (runtime names, refusals, migrations at rest) | 2–4 days, Q6's re-seal the upper half |
| Step 6 (the guard) | 1 day |
| Step 7 (platform, header and paths) | 1–2 days |
| Step 8 (three products, scripted) | 1 day |
| **Total** | **9–15 days**, one change at a time (no two lanes at once on the shared machine) |

## Breaking changes

None in this record, which changes no code. Steps 3 to 5 break,
under ADR 0040's v0 licence, which the root module still holds: a minor of
the root module (`release:minor`) and of each connector module, each at its
own version (ADR 0171). Every product changes its imports and its
qualifier; nothing is kept under the old path (Q2).
The operator's variables, the probes' paths and the struct tag key change,
each refused or answered as §3 and Q12 say rather than ignored. The graph's
`Version` goes to 6, which the platform's Studio reads. What is at rest
changes only by a migration the framework runs: the tables, the migration
set, the secret prefix and the dev data directory renamed (Q5), the sealed
data re-sealed under the new labels (Q6) — data-key's re-wrapped in step 5b,
the references' in step 5c.

**Step 3, landed (2026-10-18).** `framework/kit` and everything beneath it
are gone, with no alias (Q2); `framework/internal/kit`'s engines are renamed;
`model.ProvenanceKit`, `PortOwnerKit` and `SettingKitDev` are
`ProvenanceApp`, `PortOwnerApp` and `SettingAppDev`, and `model.Version` is 6.
A minor of the root module and of each connector module (`release:minor`).
A product migrates with a rewrite of the import path and of the qualifier,
then the files where a local `app` shadows the package:

```sh
grep -rlZ --include='*.go' 'github.com/kitsunium/sdk/framework/kit' . | xargs -0 sed -i -E \
  -e 's#github.com/kitsunium/sdk/framework/kit/kittest#github.com/kitsunium/sdk/framework/app/apptest#g' \
  -e 's#github.com/kitsunium/sdk/framework/kit#github.com/kitsunium/sdk/framework/app#g' \
  -e 's/\bkittest\./apptest./g' \
  -e 's/\bkit\.([A-Z])/app.\1/g'
go get github.com/kitsunium/sdk@<the release> && go mod tidy
go vet ./...
```

The rewrite only touches `kit.` followed by an exported name, so a variable
named `kit` (`kit.Name` on a local) needs the same look as a shadowing `app`.
`go vet` then names each file where a local `app` — a variable, a parameter, a
receiver — hides the package: `app.X undefined (type *app.App has no field or
method X)`, or `app.X is not a type`. Rename the local (`a`, `prod`) from its
declaration on, or import the package under another name; the fourteen such
files of the three products are step 8's. A product that names
`model.ProvenanceKit`, `PortOwnerKit` or `SettingKitDev` takes the new names;
one that compares a graph's version with 5 reads 6. The runtime names do not
move at this step: the variables, the probes' paths, the struct tag key, the
tables and what is sealed stay as they are until steps 4 and 5.

**Step 4, landed (2026-10-18).** The runtime names change, under the same v0
licence: a minor of the root module and of each connector module
(`release:minor`). What an operator and a product change:

- *The variables*, in every Dockerfile, compose file, Nomad or Kubernetes
  manifest, systemd unit and CI job: `KIT_<STEM>` → `<PRODUCT>_<STEM>`, the
  product's own prefix — for the todo, `KIT_DATA_DIR` → `TODO_DATA_DIR`,
  `KIT_ADDR` → `TODO_ADDR`, `KIT_ENV` → `TODO_ENV`, `KIT_SMTP_URL` →
  `TODO_SMTP_URL`, `KIT_TRUST_PROXY` → `TODO_TRUST_PROXY`. A variable left
  under its old name is refused at the start, naming the new one, so a
  forgotten one is found at the first deployment, not lost. The tool's own
  (`KIT_DEV_*`, `KIT_STUDIO*`, `KIT_ANALYZE`, and `KIT_SEED` on the platform's
  side) keep their names.
- *The probes*, in every orchestrator's liveness, readiness and startup check
  and every HTTP health check: `/_kit/health/<probe>` → `/_app/health/<probe>`.
  The old paths keep answering, logged as deprecated, for two minors; a
  `HEALTHCHECK` that runs `<binary> healthcheck` needs nothing.
- *The struct tags*: `kit:"…"` → `app:"…"`, whatever the words —
  `grep -rlZ --include='*.go' ' kit:"' . | xargs -0 sed -i 's/ kit:"/ app:"/g'`
  —; a field left under the old key is refused at the start.
- *A setting, secret or database* whose variable is one of the framework's
  (`<PRODUCT>_ADDR`, `_DATA_DIR`, `_SMTP_URL`, `_LOG_LEVEL`…) takes another
  name.
- *A reader of the graph*: `app.kit` and `app.build.kit` are `app.app` and
  `app.build.app`; a provenance or port owner `"kit"` is `"app"`, a setting's
  origin `"kitdev"` is `"appdev"`; a setting's name is the product's variable;
  `model.VarEnv` and its siblings hold the name after the prefix. The
  platform's `kit dev` must set the product's variables (and list them in
  `KIT_DEV_FORCED`) and its Studio read `/_app/api/`: a `kit dev` that still
  sets `KIT_ENV` is refused by a product on this release (step 7).
- *A telemetry collector* reads the handshake `app-telemetry/v1`, the wire
  unchanged.

**Step 5a, landed (2026-10-18).** The names at rest, under the same v0
licence: a minor of the root module and of each connector module
(`release:minor`).

- *The first start of a product that ran a release before this one* moves,
  once, before anything is read: in dev `.kit/data` to `.app/data`; in the
  data directory `kit.privacy` to `app.privacy`; on each database the
  framework keeps tables on, `kit_migrations`, `kit_tables`, `kit_queue__…`
  and `kit_privacy__…` to `app_…`, its log saying what moved. Nothing is
  copied and nothing re-sealed. A database that migrates manually refuses
  to start until `<product> migrate up` has moved them.
- *Stop every process of the former release first*, as for any migration:
  a former release started after the move finds no table under its names,
  makes empty ones, and the next start of this release is then refused
  (`CodeRenameTarget`) rather than replacing either.
- *A refusal* names both places; keep the one that holds the product's data
  and move the other aside — nothing is ever overwritten.
- *`.gitignore`*: a product that ignored `.kit/` ignores `.app/` too.
- *Going back* to the release before, before its first start (and before the
  minor that removes the old labels, which 5b brings): since 5a re-seals
  nothing, renaming back is the whole of it. Stop the product, then
  - in the data directory: `mv <data>/app.privacy <data>/kit.privacy`; in
    dev, `mv .app/data .kit/data`;
  - on each database: rename every `app_privacy__*`, `app_queue__*`,
    `app_tables*` table back to `kit_…` and `app_migrations` to
    `kit_migrations` — on PostgreSQL the indexes named `app_…` too —, then
    in `kit_tables` replace `app_privacy__`/`app_queue__` by `kit_privacy__`/
    `kit_queue__` in `doc_key` and `doc`, and in `kit_migrations` replace
    `app_privacy__`, `app_queue__` and `app_tables` in `name`. The
    statements, for each engine, are `renameTablesBack`,
    `renameRegistryBack` and `renameHistoryBack` in
    `framework/connectors/{postgres,mysql}/former_test.go`, and
    `renameBack` for SQLite in `framework/connectors/sqlite/former_move_test.go`,
    which shows the result is the former release's database byte for byte.
  Then start the former release. The next start of this one moves again.

**Step 5b, landed (2026-10-18).** The secret prefix and data-key's label,
under the same v0 licence: a minor of the root module, of `internal` (the
SDK's `SubjectKeysConfig.Former`, `RewrapReport.FromFormer`, additions) and
of each connector module (`release:minor`).

- *The first start of a product that ran a release before this one* — or
  the first command that opens its secrets — copies `kit-index-key` and
  `kit-smtp-url` to `app-index-key` and `app-smtp-url` in the store the
  environment keeps, makes `app-data-key`, and re-wraps every data key
  under it, once, saying on its log how many moved. Nothing a former
  release kept is written or deleted, but `kit-data-key`, retired once its
  rotation would have pruned its versions (§Step 5b, done). The cost is the re-wrap pass every
  start already ran, plus one write per data key, once (§Step 5b, done:
  5.3 µs a key in memory, the store's write not measured); a product with
  many keys takes that much longer to start the first time. A start
  stopped half-way is resumed by the next one.
- *Stop every process of the former release first.* A former release that
  runs after the move does not open a data key this one re-wrapped (its
  reads fail with `SEAL_KEY_UNREADABLE`, never as an erasure), and a key it
  makes is wrapped under `kit-data-key`, which the next start of this
  release moves.
- *A refusal*: an index key under both names with two values
  (`CodeRenameTarget`) — keep the one the product's references were made
  under, set the other aside; a copy the store refused (`CodeRenameFailed`)
  — the next start resumes it.
- *A product's own names*: a secret, a setting or a database whose name
  starts with `app-` is refused at the start, as one starting with `kit-`
  was; rename it.
- *The operator*: `secrets list` shows `app-data-key`, `app-index-key`,
  `app-smtp-url`; `secrets set` takes those names (`kit-smtp-url` is refused,
  naming `app-smtp-url`). A pinned `<PRODUCT>_DATA_KEY` or
  `<PRODUCT>_INDEX_KEY` changes nothing.
- *Going back* to the release before this one, while the former names are
  still read (until the minor that ends the window): stop the product, run
  `<product> privacy rewrap -back`, which moves every data key back under
  `kit-data-key`'s newest version, then start the former release, which
  finds its three secrets as it left them. What it cannot read: a message
  sealed under data-key itself since this release started (no subject, no
  user), bound to `app-data-key` and the present label, which it reads as
  empty — drain the queues and the mail outbox first —, and an SMTP URL set
  under `app-smtp-url` only, which it does not see — set it again there.
  The next start of this release moves everything forward again.

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
- **A deprecated alias package left at `framework/kit`.** Refused with Q2: it keeps
  the name in the code of every product that has not moved, which is the
  thing being removed.

## Decisions of 17 October — the owner's answers to the twelve questions

The owner named the framework (Q1) and retained the recommendation of each
other question as written.

**Q1 — The framework's name.**
**Decision.** `framework/app` (option a): the owner's example, the word of
what a product builds, no collision with the standard library nor with the
SDK's layer vocabulary; the 14 product files that declare a local `app` are
renamed in step 8.

**Q2 — The old import path.**
**Decision.** A hard break under ADR 0040, no alias left at `framework/kit`,
the identifiers kept (`app.App`, `app.AppOption`) (option a): three in-house
products and a scripted rewrite, where an alias would carry the name into
every product that does not move.

**Q3 — The operator's variables.**
**Decision.** The product's own prefix (`TODO_DATA_DIR`), reserved in the
namespace the framework already derives for a product's settings (option
a): the operator sees only the product's name.

**Q4 — The probes' paths and the reserved prefix.**
**Decision.** `/_app/health/{live,ready,startup}`, one reserved prefix,
the Studio's reads under it in dev (option a): neutral, and no root path
taken from a product.

**Q5 — Names at rest: tables, migration set, secret prefix, dev data directory.**
**Decision.** Renamed by a framework migration (option a): a database
administrator reading `kit_migrations` would know, and reading both names
for good would keep both in every binary.

**Q6 — The domain-separation labels.**
**Decision.** A re-seal: new data under new labels, a framework command
re-sealing what exists, the old labels removed in a later minor (option a):
the only path to a binary without them that leaves every sealed value
readable.

**Q7 — The struct tag key.**
**Decision.** The framework's name, `app:"personal"` (option a): the tag
also carries `secret`, `public` and `history=N`, and a tag key named after
the package that reads it is Go's convention.

**Q8 — Where the Studio hook lives.**
**Decision.** In the framework, `framework/app/studio`, never imported by a
product, injected by `kit dev`'s overlay (option a): no change of module
graph and no platform module in a product's build.

**Q9 — The generated header and the skeleton comment (the platform's).**
**Decision.** `// Code generated from design/v1; DO NOT EDIT.` with
`// design:digest sha256:…`, and a skeleton with no provenance line (option
a): Go's convention needs "Code generated" and "DO NOT EDIT", not a tool's
name.

**Q10 — The design, the lock, the version pin and CI.**
**Decision.** `design/` and its lock stay in the product's repository with
no tool name in their content; the version pin and the CI step that
installs the tool leave the product's files (option a): "nobody can tell"
holds with `kit check` still run by the product's CI, and a design outside
the exported tree stays open to the platform's ADR.

**Q11 — Does the rule extend to the SDK's own sources?**
**Decision.** No (option a): the SDK is a library the tool designs (ADR
0163), its generated headers and design files stay, and the rule binds a
product and what the framework compiles into one.

**Q12 — An old name met at the start.**
**Decision.** Refused, naming the new one, for two minors, then forgotten;
an old probe path answered and logged as deprecated over the same window
(option a): ignoring it loses a data directory or stores personal data in
clear, and accepting both keeps the old names longer.

## Revision history

| Version | Date | What changed |
|---|---|---|
| Proposed | 2026-10-17 | The design, the measured inventory, twelve questions for the owner |
| Accepted | the owner's decision, 2026-10-17 | Q1 answered `framework/app`, the recommendation of Q2 to Q12 retained as written; each question replaced by its *Decision* line; the status, the header (*Accepted*, *Amends*), the placeholder name replaced by `app`, and the passages that depended on an open question (§1's table, §4's hook, §5, migration step 0 and 5, the cost, §Breaking changes, §Deferred) updated. No other text changed |
| Accepted, step 1 done | 2026-10-17 | The migration table marks step 1 done; §Step 1, done records the Studio's routes, guard and handlers in `studiokit` behind `plug.Studio` (`StudioDriverConfig`: `Env`, `Settings`, `Forced`, `DevBuild`, `Mount`), reading the app through `plug.StudioHost`, which the core implements and only `EnableStudio` reaches; the event stream and the profiler `studiokit`'s own, their hooks gone; the tool's nine variables read by `studiokit` alone; the probes, the reserved `/_kit/`, the captured mail's header (shared with `Mailer.Captured`) and the dev announcement kept in the core for step 4; the initgraph test's `studioCode` and `toolVariables`; and the measure — the smallest product 2.1 kB of code and 4.0 kB of file less, none of the tool's names, and §Context's "compiled into every production binary" corrected: the handlers were already unlinked without the Studio. No decision changed |
| Accepted, step 4 done | 2026-10-18 | The migration table marks step 4 done; §Step 4, done records the runtime names — the operator's variables under the product's prefix and reserved in its namespace, `/_app/health/*` and `/_app/api/*`, `app:"…"`, the graph's `"app"` values and its `app` fields, the checks, loops, owner column and actor named `app`, `app_node`/`app_loop`, `X-Attachments`, `app-telemetry/v1`, `model.VarEnv` and its siblings holding the name after the prefix —, what keeps the tool's name (its nine variables, §3) and what waits for step 5 (the names at rest, and `kit.privacy`, whose stores rest under its name), the refusals of Q12 (`CodeRenamed` 0.4.2.85, `DiagnosticsError.Renamed`: a variable, a tag; a former probe path answering, logged as deprecated); §Breaking changes says what an operator and a product change. No decision changed |
| Accepted, step 5a done | 2026-10-18 | The migration table splits step 5 and marks 5a done; §Step 5a, done records the names at rest moved, never copied and never replacing — `kit_migrations` (its history taken by `app_migrations`, the set `app`), `kit_tables`, `kit_queue__…`, `kit_privacy__…` (with their derived tables and PostgreSQL's indexes) to `app_…` by a migration of `kit_migrations` under its lock, `.kit/data` to `.app/data`, `kit.privacy` to `app.privacy` with its directory —, the refusals (`CodeRenameTarget` 0.4.2.86, `CodeRenameFailed` 0.4.2.87), why the secret prefix and the labels are 5b's (the keyring binds the secret's name; a sealed value binds its store's node id), the module name `kit` kept reserved, and what holds it, a former release's data in `connectors/sqlite/testdata/former`; §Breaking changes says what the first start does and how to go back. No decision changed |
| Accepted, step 5b done | 2026-10-18 | The migration table splits step 5 in three and marks 5b done; §Step 5b, done records why the re-seal is two changes (what a keyring wraps is bound to the secret's name and moves by a re-wrap; what a reference names re-keys every data key and every box, and the journal's chain records the references: 5c), the secrets `kit-index-key` and `kit-smtp-url` copied to `app-…` once (`CodeRenameTarget` for two index keys, `CodeRenameFailed` for a refused copy — no new code), `app-data-key` made and every data key re-wrapped under it through the SDK's new `SubjectKeysConfig.Former` (`RewrapReport.FromFormer`), the wrapped key itself as the durable mark of the progress, a box under data-key itself sealed under `app/v1 sealed at rest` and read under both labels, `app-` reserved beside `kit-`, `privacy rewrap [-back]`, an erasure unreadable at once under both roots and `kit-data-key` retired on the schedule its rotation would have pruned it on (ADR 0142's bound for a copy outside the store), what 5c and the minor that ends the window do, the measure, and what holds it (`connectors/sqlite/testdata/former-keys`, written by the release before); §Breaking changes says what the first start does, its cost, and the way back. No decision changed |
| Accepted, step 3 done | 2026-10-18 | The migration table marks step 3 done; §Step 3, done records the rename — `framework/app`, `apptest`, `framework/internal/app` and its engines `storeengine`, `queueengine`, `workflowengine`, `mailengine`, `jobengine`, `serverengine`, `studioengine`, `design/framework/app.yaml`, the codes at their values under re-pointed owners, `model.Version` 6 with `ProvenanceApp`, `PortOwnerApp` and `SettingAppDev` —, the texts of §2 and what they leave to steps 4 and 5; §Breaking changes gives a product's migration, a rewrite of the import path and the qualifier then the shadowing files; §4 records what the platform found at step 2, `kit dev` building with a `go.mod` outside the tree (`-modfile`). No decision changed |

## Deferred

- The platform's ADR for §5 and Q9–Q10.
- Q8 (c), the Studio out of process — no hook in the framework at all —,
  its own ADR if it is ever wanted.
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
