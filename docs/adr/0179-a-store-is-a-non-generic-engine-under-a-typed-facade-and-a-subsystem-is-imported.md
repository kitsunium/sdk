# ADR 0179 — a store is a non-generic engine under a typed facade, and a subsystem is imported

- **Status**: Proposed
- **Date**: 2026-10-16
- **Deciders**: SDK maintainers (issues #273 and #276; the owner's decision of 2026-10-16 to take the clean architecture: a non-generic store engine under a typed `Store[T]`, every subsystem plugged in by an import, breaking under ADR 0040)
- **Amends** (once accepted): [ADR 0172](0172-a-product-initialises-what-it-imports-and-the-stores-stay-in-the-core.md) §4 — the declared subsystems leave the core; its §1 rule is kept and applied to them
- **Related**: [ADR 0147](0147-the-framework-is-a-module-of-the-sdk-above-pkg.md) (the framework above `pkg`), [ADR 0139](0139-a-document-store-over-sql-joins-the-transaction-its-context-carries.md) and [ADR 0178](0178-kits-effects-join-the-transaction-on-the-database-that-keeps-their-queue.md) (the transactions the engine must keep), [ADR 0176](0176-an-error-sentinel-is-static-data-and-its-options-are-the-designs-fields.md) (the last non-breaking lever of #276), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (siblings, never a widening), [ADR 0040](0040-changing-a-published-shape-while-v0.md) (the v0 licence), [ADR 0090](0090-a-port-named-in-public-must-be-implementable-in-public.md) (what a published port promises), [ADR 0162](0162-the-sdk-is-one-module-and-a-release-is-one-tag.md) and [ADR 0171](0171-a-module-per-family-and-a-version-is-never-reused.md) (the framework is the root module), issues #272, #273, #276, kitsunium/statusline#3

## Context

### What a product pays for today

Every product that imports `framework/kit` links and initialises what every
subsystem kit can run needs, whatever it declares. Go runs the
initialisation of every package a binary imports, and the linker keeps every
function reachable from `App.Main` — kit's own stores are among them.

The status line (kitsunium/statusline#3, #276) is the product this costs most:
a new process per redraw. Measured on SDK v0.30.0 with
`testdata/parity/latency-ab.sh` (6 alternating rounds × 300 runs, warm p50,
static `-trimpath` binaries, Go 1.27.2), against the commit before the kit
migration:

| Binary | Warm p50 (median) | Binary | Packages initialised | Init (median) |
|---|---|---|---|---|
| before kit | 5.296 ms | 11.8 MB | 121 | 0.969 ms |
| SDK v0.30.0 | 6.377 ms | 23.4 MB | 183 | 1.425 ms |
| delta | **+1.081 ms (+20 %)** | ×2 | +62 | +0.456 ms |

A byte-for-byte control (the same binary exec'd from two paths) measured
+0.002 ms between medians, and the noise floor this machine has kept since
ADR 0172 is 0.19 ms: +1.08 ms is about 5.7 times it, and the ranges do not
overlap. Initialisation explains about 0.46 ms; the remaining ~0.6 ms is
outside it — a deduction, not a measurement: a binary twice as large, its
pages, its loading and its runtime. ADR 0176 took the error sentinels out of
`init` (34 inits fewer, median init 1.089 → 0.846 ms on the probe), and the
end-to-end p50 did not move: it was the last non-breaking lever #276 named.

The status line declares no store, no topic, no workflow and no mailer: its
design is a client role (`cli`) and a daemon role (listeners, two jobs) in one
executable.

### What the smallest product links, measured again

`framework/kit/testdata/cliprobe` — one service, one default fail-safe CLI
command — built from `origin/main` on 2026-10-16 (`go1.27.2`, `-trimpath`):
363 packages, a 21.0 MB file, of which 7.02 MB of machine code (`.text`),
5.49 MB of `.gopclntab`, 1.49 MB of type data and about 5 MB of debug
information. Its text symbols (`go tool nm -size`, types `T`/`t`), by owner:

| Owner | Machine code |
|---|---|
| `framework/internal/kit`, all of it | 1.66 MB |
| — of which kit's generic store family instantiated (`StoreService` 0.418, `sealedEngine` 0.132, `historied` 0.107, `sqlEngine` 0.071, `docEngine` 0.059, `retentionRun` 0.033, `trashRun` 0.022, `privacyDecl` 0.016, the rest under 0.01 each) | **0.89 MB** |
| SDK `docstore` | 0.70 MB |
| SDK `queue` | 0.15 MB |
| `database/sql` + SDK `sql` | 0.12 MB |
| SDK `i18n` | 0.06 MB |
| SDK `mail` + `mail/spool`, `scheduler`, `statemachine` | 0.03 MB |
| **What this ADR takes out of a product that declares none of it** | **≈ 1.95 MB of 7.02 MB (28 %)** |
| `net/http` and its closure (not this ADR) | 0.62 MB |

The probe declares no store, yet links three instantiations of the store
engine — 698 symbols of `StoreService` alone: kit's own stores (the privacy
journal, the holds, the wrapped data keys) are declared by kit, reachable
from `App.Main` through `mountPrivacy`, and instantiated per record type. By
division, every entity type a product declares adds about 0.3 MB of machine
code for its store, before its own code: a generic engine is compiled once per
type shape. This is an estimate from the symbol table, to be confirmed on a
product with many stores (step 0, §Consequences).

The share of the file that machine code drags with it (`.gopclntab`, type
data) follows it; in proportion, the 1.95 MB of code is 3–4 MB of the 21.0 MB
file. Estimated, not measured: step 0 measures it.

### Two ways to plug a subsystem

kit already has the mechanism this ADR generalises. #272 and ADR 0172 took the
HTTP engine, the frontends' file server, the process sampler, the Studio's
event stream and its profiler out of kit's import graph:
`framework/internal/kit/plug` holds hooks (`atomic.Pointer`s of functions,
stdlib and `model` only), `serverkit` and `studiokit` hold the SDK code behind
them, and `framework/kit/server` and `framework/kit/studio` set the hooks as
they are imported. A server that does not import `framework/kit/server` is
refused at the start (`profile.server-missing`). `framework/kit/config/yaml`
and `config/toml` do the same for configuration formats
(`RegisterConfigFormat`).

Everything else is wired in: stores (`docstore`, `sql`, `database/sql`),
topics, subscriptions, watches and queued commands (`queue`), workflows
(`statemachine`), mailers (`mail`, `mail/spool`), jobs (`scheduler`) and kit's
diagnostic catalogues (`i18n`) are methods of `kit.Service` and options of
`kit.App` whose engines kit imports. Two ways to plug a subsystem, chosen by
history, not by rule.

### Why the stores cannot cross a hook as written

A hook is a value, and a value cannot be a generic function: kit cannot ask a
package it does not import for a `docEngine[Order]`. ADR 0172 §Context 2
recorded it with "8 800 lines in 29 files"; measured again on `origin/main`
(production files of `framework/internal/kit`, tests excluded):

| What | Files | Lines |
|---|---|---|
| `framework/internal/kit`, production | 155 | 41,687 |
| files importing SDK `docstore` | 27 | 8,370 |
| files importing SDK `sql` | 18 | 4,769 |
| files importing `database/sql` | 2 | 448 |
| **union: the stores' SDK packages** | **43** | **12,202** |
| files importing SDK `queue` | 11 | 3,973 |
| files importing SDK `statemachine` | 3 | 1,656 |
| files importing SDK `mail` / `mail/spool` | 1 | 1,004 |
| files importing SDK `scheduler` | 4 | 1,890 |
| files importing SDK `i18n` | 1 | 146 |
| **union: every subsystem of this ADR** | **56** | **18,093 (43 %)** |
| generic functions and methods (44 generic types) | 66 | 7,894 lines of bodies, 686 functions |
| — of the store family (stores, indexes, history, privacy, sealing, holds, revisions, trash, retention, records, data copy, workflows) | 43 | 6,448 (5,577 without workflows) |
| test files importing one of these SDK packages | 32 of 128 | 9,490 |

ADR 0178's work (the trash, `Records.Keys`, `data copy`, the queues on the
database) took the store family from 29 files to 43 since ADR 0172. The cost of
the redesign grows with every feature built on the generic engine.

kit already has the shape the redesign needs, in one place: a store that
seals runs on `sealedEngine[T]`, whose inner engine is a
`storeEngine[sealedDoc]` — "one type for every store's record: the engines
under it are compiled once, not once per record type" (`seal_store.go`). The
document is its JSON at rest plus the record opened, kept as `any`. What this
ADR proposes is that shape for every store.

The other subsystems differ:

- **Topics, subscriptions, watches and queued commands** encode their message
  at the call and hand bytes to the SDK's `queue.Broker`, which is not generic
  (`NewFile`, `NewSQL`, `NewMemory` return a `Broker`). Only kit's typed facade
  (`TopicService[T]`, `Command[Req, Resp]`) is generic; the engine can cross a
  hook today.
- **Workflows** run on the SDK's `statemachine`, generic in the entity and the
  state (`Define[E, S]`, `New[E, S]`): the same obstacle as the stores, on a
  smaller scale (0.001 MB in the probe — generic code a product does not
  instantiate costs nothing in the binary — but the package's import and init
  stay).
- **Mailers**: `Mailer.Send` takes the SDK's `mail.Message`, so `pkg/v1/app/mail`
  stays in kit's graph as a type; the spool and the SMTP transport can leave.
- **Jobs**: `Every` and `Cron` are non-generic; the scheduler can leave.
- **kit's catalogues**: `words.go` loads `locales/en.json` and
  `locales/fr.json` with the SDK's `i18n` at the first diagnostic. The
  terminal, the logs and an error read English only; every other language is
  for the Studio, which shows its reader's. English can be kit's own table,
  and the rest is the Studio's.

### What the standard library does

`database/sql` is a non-generic engine (`*sql.DB`, `driver.Value`) over
drivers a program imports: a driver calls `sql.Register(name, driver)` in its
`init`, the program writes `import _ "github.com/lib/pq"`, and `sql.Open`
with an unknown name returns `sql: unknown driver "x" (forgotten import?)`. The
contract between the two is a package of interfaces of its own,
`database/sql/driver`, which neither side's implementation lives in.
`image.RegisterFormat` is the same shape: a decoder registers itself as its
package is imported, `image.Decode` refuses an unregistered format with
`image.ErrFormat`. In both, the typed convenience (`Scan` into a typed
destination; `png.Decode`) sits above a non-generic core, and the program
links only the drivers it imports.

kit differs in one respect: it refuses at the start, every missing import at
once, before any component runs — not at the first use — because a status
line or a daemon has no first use a person watches.

## Decision

### 1. One way to plug a subsystem: an import

A subsystem is in a program if and only if the program imports its
registration package. ADR 0172 §1's rule applies to every subsystem kit runs,
not only to those whose reader was already opt-in:

| Registration package | Subsystem | Implementation (imported by it alone) | SDK packages that leave `framework/internal/kit`'s graph |
|---|---|---|---|
| `framework/kit/store` | stores, indexes, databases and their migrations, `kit.Transact` on a database, sealing at rest, history, revisions, trash, retention, holds, the privacy journal, `Records`, `data copy` | `framework/internal/kit/storekit` | `data/docstore`, `data/sql`, `database/sql` (see Q5) |
| `framework/kit/queue` | topics, subscriptions, watches, queued commands | `framework/internal/kit/queuekit` | `data/queue` |
| `framework/kit/workflow` | workflows | `framework/internal/kit/workflowkit` | `app/statemachine` |
| `framework/kit/mail` | mailers: the spool, the outbox, the SMTP transport | `framework/internal/kit/mailkit` | `app/mail/spool` (and `net/smtp`) |
| `framework/kit/jobs` | `Every`, `Cron` | `framework/internal/kit/jobkit` | `app/scheduler` |
| `framework/kit/studio` (exists; Q6) | kit's diagnostics in the languages after English, which only the Studio shows | `framework/internal/kit/studiokit` | `app/i18n` |

`framework/kit/server`, `framework/kit/studio` and `framework/kit/config/*`
are already this shape and stay as they are; the Studio's gains the
catalogues. Each new registration package is
`server.go`'s shape: one `var _ = enable()` that calls its `*kit` package's
`Enable` and kit's, nothing exported but its documentation — unless Q2 or Q5
decide that a declaration or the database `Engine` lives there.

### 2. The store engine is non-generic; `Store[T]` is a typed facade over it

The engine works on documents, not on entities:

- **A document** is its bytes as they rest — the JSON kit encodes today,
  unchanged byte for byte — and the entity once decoded, kept as `any` beside
  them so a read, an index function or a privacy walk does not decode twice
  (`sealedDoc`'s shape, generalised; `sealedDoc` itself becomes the general
  case).
- **Keys** are strings, as today. The key function and the index functions
  the product declares on `T` (`Unique[T]`, `Index[T]`, the retention options'
  `func(T)`) are wrapped once, at the declaration, into functions of a
  document; the engine never sees `T`.
- **Indexes, write modes, versions, trash, weight, key listing, fold** are
  the engine's, as the SDK's `docstore` gives them, through the sibling
  interfaces kit already has (`versionKeeper`, `trasher`, `keyser`,
  `versionWeighter`, `folder`), all non-generic.
- **Transactions** keep ADR 0139 and ADR 0178 exactly: kit's `kit.Transact`,
  its levels, the effects held until the commit and the two-phase release stay
  in kit's core, which knows a database only through a non-generic scope the
  store package implements (begin, savepoint, commit, rollback, the
  `Joiner`/`Deferrer`/`Aborter` it carries). The SDK's `docstore` and `sql`
  are instantiated once, at kit's document type.
- **Refusals** are kit's: the engine translates the SDK's sentinels before
  they reach the core, so the core matches kit's codes and never imports
  `docstore` to call `errors.Is`.

`Store[T]` (`StoreService[T]`) stays in kit's core and keeps its name, its
declaration (`Service.Store[T]`) and every typed method. Each method is
thin: encode the entity, call the engine, decode what comes back. Privacy,
history, revisions, trash and retention keep their rules; what changes is
that they read documents (kit already walks a document as it rests for
sealing — `objectMembers`, `sealWalk`) and the type's plan, which is
reflection over a `reflect.Type` (`planOf`, cached) and needs no generic.

What stays generic is what a product calls with its own type: the facade's
methods and declarations, `TopicService[T]`, `Command[Req, Resp]`,
`Query[Req, Resp]`, `WorkflowService[E, S]`. Each is a few lines per method,
instantiated only for the types a product declares.

### 3. The contract between kit and a subsystem is a package of interfaces

As `database/sql/driver` sits between `database/sql` and a driver, the hooks
and the non-generic types a subsystem implements — the store engine, its
siblings, the document, the database scope, the broker, the workflow engine,
the mail spool, the job runner, the catalogue — live in kit's hook package
(`plug`, Q7), which imports the standard library and `model` only. The core
reads the hooks; each `*kit` package implements them and imports the SDK; the
core imports no `*kit` package and no SDK package of a subsystem.

Code of a subsystem that names no SDK type (the privacy rules, the history's
diff, the trash's agenda) may stay in kit's core, provided it is reached only
through a value the subsystem's package provides: the linker drops what
nothing reaches, as it drops `startHTTP` today in a program that does not
import `framework/kit/server`. Code that names an SDK type of a subsystem moves
to its `*kit` package.

These interfaces are internal (`framework/internal/kit/...`): the drivers are
the SDK's own, so ADR 0090's promise — a port named in public is
implementable in public — is not made, and nothing in `pkg/v1` or
`framework/kit` names them. A third-party store engine is out of scope
(§Deferred).

### 4. What a product writes

A designed product writes nothing: see §5. A hand-written one adds one
import per subsystem it declares, in its `main` package or wherever it builds
its `App`:

```go
import (
	"github.com/kitsunium/sdk/framework/kit"
	_ "github.com/kitsunium/sdk/framework/kit/jobs"
	_ "github.com/kitsunium/sdk/framework/kit/store"
)
```

The declarations do not change: `svc.Store[T](...)`, `svc.Topic[T](...)`,
`svc.Every(...)`, `kit.Database(...)` are written as today (Q2). A connector
(`framework/connectors/{postgres,mysql,sqlite}`) imports
`framework/kit/store` itself: a program that declares a database with one has
the stores.

### 5. `kit gen` writes the imports

The platform's `kit gen` already writes Go imports from a product's design.
It writes, in the file that builds the binary, the blank import of each
registration package the design's declarations need — stores or databases →
`store`; topics, subscriptions, watches, queued commands → `queue`; workflows
→ `workflow`; mailers → `mail`; jobs → `jobs` — for the union of the
binary's roles, since an import is the binary's, not a role's. `kit check` reports an
import the design does not need and a need the imports do not meet, so a
generated product never reaches the start's refusal.

### 6. A declaration without its import is refused at the start, typed

At the start, after the declarations and before any component, kit asks of
each declaration whether the hook of its subsystem is set. Every one that is
not is a diagnostic `subsystem.missing` naming the declaration (its node, its
position) and the package to import, all of them at once; the start fails
with them. The refusal carries a code of its own so that a caller can match
it with `errs.HasCode`: `CodeSubsystemMissing`, reason
`SUBSYSTEM_NOT_IMPORTED`, public text "a declaration needs a subsystem this
program does not import", in kit's range `0.4.2.*`. **This ADR allocates
nothing**: the first implementing change declares it in
`design/framework/kit.yaml`'s sentinels (ADR 0164) at the next free serial —
0.4.2.83 if none is taken before. `profile.server-missing` becomes a case of
the same diagnostic and code, so a missing `framework/kit/server` is refused
the same way.

The check runs at the start, not at the declaration: a declaration made in a
package-level variable of a product package runs at that package's `init`,
and Go orders the inits of two packages that do not import each other by
import path, so the registration package's `init` may come after it.

### 7. The import graph is held by tests

`framework/kit/initgraph_external_test.go` grows in two ways:

- `TestTheFacadeImportsNoOptInSubsystem`: `go list -deps framework/kit`
  contains none of `data/docstore`, `data/sql`, `database/sql` (Q5),
  `data/queue`, `app/statemachine`, `app/mail/spool`, `app/scheduler`,
  `app/i18n` — beside what it refuses today. During the migration the test
  carries an allow-list of what is still in the core, which each step empties
  and the last one deletes.
- **A reachability check**: the probe built without any registration import
  holds no text symbol of those packages, nor of kit's store engines
  (`go tool nm`) — an import graph says what is initialised, the symbol table
  what is linked; and, for each registration package, a probe that imports it
  does hold them, so the test cannot pass by testing nothing.

Its gate stays CI's `test-386` job, which runs `go test` with a toolchain on
`PATH` (rule 12).

## Consequences / Semantics

### Migration, in this order

Each step is one PR, green on its own; steps 1–8 are non-breaking because
`framework/kit` itself blank-imports each registration package as it is
created, until step 10 removes those imports.

| Step | What | Breaking |
|---|---|---|
| 0 | **Measure the ceiling** (Q1): the probe and the status line built from a throwaway branch where kit's references to every subsystem of §1 are cut; machine code, file size, inits, `latency-ab.sh` with its control, recorded on #276 | — (not merged) |
| 1 | the hook types of §3 for the store, the start's check of §6 with its code in the design, the extended `initgraph` test with its allow-list | no |
| 2 | the store engine non-generic **inside** kit's core (no package moves yet): `StoreService[T]` over documents; `docEngine`, `sqlEngine`, `historied`, `sealedEngine`, revisions, trash, retention, records, data copy on documents; the per-type instantiations gone. `storetest` passes unchanged | no |
| 3 | `storekit` + `framework/kit/store`: the code naming `docstore`, `sql`, `database/sql` moves; connectors import `framework/kit/store` | no (the facade imports it) |
| 4 | `queuekit` + `framework/kit/queue` | no |
| 5 | `workflowkit` + `framework/kit/workflow` (Q3) | no |
| 6 | `mailkit` + `framework/kit/mail` | no |
| 7 | `jobkit` + `framework/kit/jobs` | no |
| 8 | English a table in kit's core, the other catalogues and the SDK's `i18n` behind `framework/kit/studio` (Q6) | no |
| 9 | platform: `kit gen` writes the imports, `kit check` checks them; statusline, todo, moderation and platform regenerated or edited to import what they declare — while the facade still imports everything, so nothing breaks yet | no |
| 10 | `framework/kit` drops its blank imports; the allow-list is gone; a root-module minor under ADR 0040 | **yes** |

Step 2 is the bulk: about 6,450 lines of generic bodies in 43 files, and the
32 test files that name the SDK's packages. It is also measurable alone: the
per-type instantiations disappear from every product, whatever it imports.

### What does not change

- **Data at rest.** The snapshot, its small files, the versions file, the
  history, the journal, the SQL tables (`<service>__<store>`, `___vs`,
  `__history`, `kit_queue__…`, `kit_tables`) keep their bytes. A fixture
  written by the code before step 2 is read after it, and the same sequence of
  writes leaves the same files and rows, compared byte for byte.
- **Transactions.** ADR 0139's join, ADR 0178's two-phase release, the writer
  turn, the effects held until the commit: the suites that pin them pass
  unchanged.
- **A product's code**, apart from its imports (and Q5's answer for a
  connector author).

### Measurement: target and method

- **The probe** (`framework/kit/testdata/cliprobe`, `-trimpath`): `go list -deps`
  without the packages of §7, `go tool nm` without their symbols, machine
  code down by the ≈ 1.95 MB of §Context (target: at least 1.5 MB; the step-0
  ceiling replaces the estimate), file size, inits (`GODEBUG=inittrace=1`,
  min and median of 50).
- **The status line**: `testdata/parity/latency-ab.sh` with its byte-for-byte
  control, warm p50, 6 rounds × 300, the load recorded, run (a) against the
  same SDK release without this change and (b) against the commit before the
  kit migration; the binary's size; `inittrace` over 40 runs of
  `statusline daemon status`.
- **The criterion**: a gain is claimed only outside the control's spread and
  the 0.19 ms floor; inside it, it is recorded as "within the noise", as ADR
  0172 recorded its own. The binary's size is reported whatever the latency
  says.
- **What to expect, honestly**: this ADR does not by itself bring the status
  line back to the commit before kit. `net/http` (0.62 MB of code, kit's
  endpoints and the hook types name it) and the rest of kit stay; the status
  line links the jobs package for its daemon role. The step-0 ceiling says how
  much of the +1.08 ms the subsystems are; the rest is §Deferred.

### Risks

- **A read decodes.** The generic engine hands back a `T`; the document engine
  hands back bytes and, where it kept one, the entity decoded. A copy that
  shares a slice or a map with what the engine keeps would let a caller change
  stored state. The rule: the facade returns what today's engine returns —
  `storetest`'s copy cases, unchanged, are the proof — and a benchmark of
  `Get`, `List` and `Update` per engine (BENCH.md) shows the cost before step 2
  merges.
- **A shape of 12,000 lines moves under work in flight.** ADR 0178 is still
  being built on the generic engine. Step 2 is planned as one PR, rebased last,
  and nothing new is built on the generic types once this ADR is accepted.
- **A missing import found late.** A hand-written product learns at its first
  start in CI, not at compile time. The refusal names every missing package at
  once, and `kit check` catches it before for a designed product.
- **The hook surface grows.** `plug` goes from five hooks to the interfaces of
  six more subsystems (five new packages and the Studio's catalogues); it remains stdlib and `model` only, which
  `scripts/check-layer-deps.sh` and its own imports hold.
- **Bazel.** Each new package gets its `BUILD.bazel` from gazelle; the
  `initgraph` test skips under Bazel as today and keeps its `test-386` gate.

## Breaking changes

At step 10, under ADR 0040's v0 licence, which the root module still holds:

- A product that declares a store or a database, a topic, a subscription, a
  watch or a queued command, a workflow, a mailer or a job, and does not import
  the matching registration package, is refused at the start with
  `CodeSubsystemMissing` (allocated by the implementing change). It was
  accepted before.
- Nothing for kit's languages (Q6 (a)): only the Studio shows a language
  other than English, and a program that mounts the Studio imports
  `framework/kit/studio` already.
- Depending on Q5, `kit.Engine`'s method set, or its home, changes for a
  connector author; the three in-tree connectors follow in the same change.
- `%T` and reflected names of kit's internal engines change; no exported name
  of `framework/kit` is removed or renamed. `framework/kit/facade_sync` holds
  that.
- Nothing at rest changes (§Consequences).

## Alternatives considered

- **The status quo (ADR 0172 §4).** Refused by the owner: it leaves two ways
  to plug a subsystem, links ≈ 1.95 MB of code into every product that
  declares none of it, and its cost grows with each feature built on the
  generic engine (29 files at ADR 0172, 43 now). ADR 0172 named the
  non-generic engine as what would reopen #273; this is it.
- **Build tags** (`-tags kit_nostore`…). Two builds of the same source, the
  full one the default: the light build is opt-in, its failures appear only
  under the tag, every lane (Bazel, gazelle, `test-386`, the cross-build
  matrix, `kit gen`, `make api` on twelve platform cells) needs both, and
  `docs/api` would describe a surface that depends on tags. It also leaves the
  per-type instantiations untouched.
- **Splitting the status line into a client and a daemon binary.** It fixes
  one product, against its own decision of one executable with two roles, and
  helps no other product; the client would still link every subsystem kit
  wires in. It stays open to the status line, and composes with this ADR
  rather than replacing it.
- **Keeping the generic engines and registering a hook per type.** A hook per
  entity type (`store.Register[Order]()`, written by the product or by
  `kit gen`) would carry a generic engine across the boundary, but every
  type would still be compiled once per shape, the product would hold one
  registration per entity, a registration missing for one type would be a
  refusal per type, and the registry would be keyed on `reflect.Type`. It
  keeps the cost this ADR removes and adds a ceremony.
- **The constructor form for every subsystem** (`store.New[T](svc, …)` in
  place of `svc.Store[T](…)`). The `database/sql` analogy taken literally:
  explicit, and an import a product cannot forget, but every declaration of
  every product changes for a check the start already makes. It remains Q2's
  second option, and Q3's fallback for workflows.

## Questions for the owner

**Q1 — Step 0: a gate, or a measurement?**
(a) A gate: stop after step 0 if the status line's ceiling is within the
0.19 ms floor. (b) A measurement: record the ceiling, set the targets of
§Consequences from it, proceed whatever it says — the decision is
architectural (one way to plug, a product links what it imports), not only a
latency. (c) Skip it. **Recommendation: (b).**

**Q2 — Where a product declares a subsystem.**
(a) The declaring methods stay on `kit.Service` and `kit.App`; the import is a
blank one, written by `kit gen`; the start refuses a missing one. (b) Every
declaration moves to a constructor of its registration package
(`store.New[T](svc, …)`, `queue.Topic[T](svc, …)`): the import cannot be
forgotten, every call site changes. (c) (a), with constructors only where an
engine cannot be made non-generic. **Recommendation: (a)**, with Q3 deciding
whether (c) is ever needed.

**Q3 — Workflows.**
(a) The SDK's `statemachine` instantiated once, over kit's document and the
state as its encoded value; `WorkflowService[E, S]` stays a typed facade,
`svc.Workflow` unchanged. (b) `framework/kit/workflow.New[E, S](…)`, the
generic engine kept, instantiated by the product (Q2 (c)). (c) Workflows stay
in the core, an exception to §1. **Recommendation: (a)**; (b) if step 5 finds
a guard or a timer that cannot be wrapped without a behaviour change.

**Q4 — Do the registration packages import each other?**
(a) `queue`, `workflow` and `mail` import `store`: a workflow is over a
store, a sealed message or mail needs kit's store of data keys, and a queue on
a database joins the store package's transaction. (b) None imports another;
the start names each missing one. (c) One umbrella, `framework/kit/data`,
importing all of them. **Recommendation: (a)**: the dependencies are real, and
(b) would refuse at the start what the import graph can state.

**Q5 — The database `Engine` and `database/sql`.**
`kit.Engine` names `*sql.DB` (`database/sql`) and `sql.Dialect` (the SDK's
`sql`), so the core imports both today.
(a) `kit.Engine` stays in the core with kit's own dialect type, still
returning `*sql.DB`: `database/sql` (0.066 MB, a small init) stays in the
core, the SDK's `sql` leaves; product code unchanged, the three connectors
edited. (b) `Engine` and `kit.Database` move to `framework/kit/store`
(`store.Database`, `store.Engine`): both leave, every product with a database
edits its declaration. (c) Unchanged: both stay in the core.
**Recommendation: (a)**, revisited if step 0 shows `database/sql` matters.

**Q6 — kit's diagnostics in other languages.**
The terminal, the logs and an error read English; only the Studio shows
French, its reader's language.
(a) English is a table in kit's core, written from `locales/en.json`; the
other catalogues and the SDK's `i18n` are plugged in by
`framework/kit/studio`, their only reader; `TestKitSpeaksEveryLanguage` moves
with them. (b) A registration package of their own, `framework/kit/i18n`.
(c) The catalogues stay in the core (0.06 MB, read at the first diagnostic),
an exception to §1. **Recommendation: (a)**: no new import for any product,
and the subsystem is where its reader is, as ADR 0172 §2 put the process
sampler.

**Q7 — Where the contract lives.**
(a) `plug` grows one file per subsystem, stdlib and `model` only, as it is
today. (b) One contract package per subsystem
(`framework/internal/kit/plug/store`, …), as `database/sql/driver` is to
`database/sql`. (c) The interfaces in each `*kit` package, the core importing
none of them through type assertions on `any`. **Recommendation: (a)** while
it stays under a few hundred lines a subsystem; (b) when one outgrows it.

**Q8 — How the break ships.**
(a) Steps 1–9 non-breaking behind the facade's own blank imports, the break
alone in step 10, after `kit gen` and the products have the imports.
(b) Each subsystem breaks in its own step. (c) One PR for everything.
**Recommendation: (a)**: every product moves before anything refuses it, and
the break is one release note.

## Deferred

- `net/http` out of a product that serves nothing: kit's endpoints and
  `plug`'s hook types name it; 0.62 MB of code and its init. Its own ADR,
  after this one's step 0 says what is left.
- A public driver API — a third-party store engine or broker. The contract of
  §3 is internal; publishing it is ADR 0090's promise and an ADR of its own.
- The SDK's `pkg/v1/data/docstore` stays generic for the SDK's own consumers;
  only kit instantiates it once.
- The remaining facade inits of `pkg/v1` (0.019 ms, #276) and the `id`
  registry's init (ADR 0172 §Deferred).
- Splitting the status line's binary (§Alternatives), the status line's call.

## References

- `framework/internal/kit/store.go`, `store_engine.go`, `store_sql.go`,
  `seal_store.go` (`sealedDoc`, `sealedEngine`), `history*.go`,
  `revisions*.go`, `trash*.go`, `retention*.go`, `records*.go`,
  `datacopy*.go`, `transact*.go`, `topic.go`, `queued.go`, `consumer.go`,
  `workflow*.go`, `mailer*.go`, `job.go`, `words.go`, `database*.go`,
  `plug/plug.go`, `serverkit/`, `studiokit/`; `framework/kit/server/server.go`,
  `framework/kit/initgraph_external_test.go`, `framework/kit/testdata/cliprobe`,
  `framework/kit/BENCH.md`
- Go: [`database/sql`](https://pkg.go.dev/database/sql),
  [`database/sql/driver`](https://pkg.go.dev/database/sql/driver),
  [`image.RegisterFormat`](https://pkg.go.dev/image#RegisterFormat), the
  [package initialisation order](https://go.dev/ref/spec#Package_initialization)
- Issues #272, #273, #276; kitsunium/statusline#3
