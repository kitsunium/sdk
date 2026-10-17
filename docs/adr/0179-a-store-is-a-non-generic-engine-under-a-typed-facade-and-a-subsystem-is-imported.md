# ADR 0179 — a store is a non-generic engine under a typed facade, and a subsystem is imported

- **Status**: Accepted on 2026-10-16 by the owner, who retained the recommendation of each of the eight questions (Q1 to Q8), as written; no text changed but the status, each question's *Decision* line, and the passages that depended on an open question (Q2, Q3, Q5, Q6)
- **Date**: 2026-10-16
- **Accepted**: 2026-10-16
- **Deciders**: SDK maintainers (issues #273 and #276; the owner's decision of 2026-10-16 to take the clean architecture: a non-generic store engine under a typed `Store[T]`, every subsystem plugged in by an import, breaking under ADR 0040); arbitrated by the owner
- **Amends**: [ADR 0172](0172-a-product-initialises-what-it-imports-and-the-stores-stay-in-the-core.md) §4 — the declared subsystems leave the core; its §1 rule is kept and applied to them
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
363 packages, a 22.06 MB file (21.0 MiB, as `ls -h` prints it), of which 7.02 MB of machine code (`.text`),
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
data) follows it; in proportion, the 1.95 MB of code is 3–4 MB of the 22.06 MB
file. Estimated, not measured: step 0 measures it — measured on 2026-10-16,
2.39 MB of code and 7.52 MB of the file (§Step 0, measured).

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
| `framework/kit/store` | stores, indexes, databases and their migrations, `kit.Transact` on a database, sealing at rest, history, revisions, trash, retention, holds, the privacy journal, `Records`, `data copy` | `framework/internal/kit/storekit` | `data/docstore`, `data/sql` (`database/sql` stays: Q5) |
| `framework/kit/queue` | topics, subscriptions, watches, queued commands | `framework/internal/kit/queuekit` | `data/queue` |
| `framework/kit/workflow` | workflows | `framework/internal/kit/workflowkit` | `app/statemachine` |
| `framework/kit/mail` | mailers: the spool, the outbox, the SMTP transport | `framework/internal/kit/mailkit` | `app/mail/spool` (and `net/smtp`) |
| `framework/kit/jobs` | `Every`, `Cron` | `framework/internal/kit/jobkit` | `app/scheduler` |
| `framework/kit/studio` (exists; Q6) | kit's diagnostics in the languages after English, which only the Studio shows | `framework/internal/kit/studiokit` | `app/i18n` |

`framework/kit/server`, `framework/kit/studio` and `framework/kit/config/*`
are already this shape and stay as they are; the Studio's gains the
catalogues. Each new registration package is
`server.go`'s shape: one `var _ = enable()` that calls its `*kit` package's
`Enable` and kit's, nothing exported but its documentation: no declaration
(Q2) and no database `Engine` (Q5) lives there.

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

Two invariants, held by `framework/kit`'s `initgraph` test:

- **The facade imports no opt-in subsystem**: `framework/kit` alone brings
  none of the SDK packages §1 takes out of the core, and none of what the
  opt-in subsystems already kept out.
- **A product links only what it imports**: a program that imports no
  registration package links none of those packages' code nor kit's store
  engines — an import graph says what is initialised, the symbol table what
  is linked —, and one that imports a registration package does link them, so
  the test cannot pass by testing nothing.

The packages the test refuses, its allow-list while the migration runs, and
its gate are the test's, recorded in `framework/kit/CLAUDE.md`.

## Consequences / Semantics

### Migration, in this order

Each step is one PR, green on its own; steps 1–9 are non-breaking because
`framework/kit` itself blank-imports each registration package as it is
created, until step 10 removes those imports.

| Step | What | Breaking |
|---|---|---|
| 0 | **Measure the ceiling** (Q1): the probe and the status line built from a throwaway branch where kit's references to every subsystem of §1 are cut; machine code, file size, inits, `latency-ab.sh` with its control, recorded on #276 — done on 2026-10-16 (§Step 0, measured) | — (not merged) |
| 1 | the hook types of §3 for the store, the start's check of §6 with its code in the design, the extended `initgraph` test with its allow-list — done (#359, 2026-10-17) | no |
| 2 | the store engine non-generic **inside** kit's core (no package moves yet): `StoreService[T]` over documents; `docEngine`, `sqlEngine`, `historied`, `sealedEngine`, revisions, trash, retention, records, data copy on documents; the per-type instantiations gone. `storetest` passes unchanged — done (2026-10-17, §Step 2, done) | no |
| 3 | `storekit` + `framework/kit/store`: the stores' implementation — the code naming `docstore` and the SDK's `sql` — moves there; `kit.Engine` stays in the core and still returns `*sql.DB`, its dialect now kit's own type (Q5); the connectors import `framework/kit/store` — done (2026-10-17, §Step 3, done) | no (the facade imports it) |
| 3b | kit's own store code — the privacy service and its journal, the holds, sealing, history, revisions, trash, retention, the databases' components, migrations and checks, the dev tools and Main's commands on the stores (`migrate`, `privacy`, `revisions`, `data`, `watch`) — reached only through `framework/kit/store` (`EnableStores`, on `EnableServer`'s model), the core keeping call sites that fall back to an app without a store — done (2026-10-17, §Step 3b, done) | no (the facade imports it) |
| 4 | `queuekit` + `framework/kit/queue`: the queues' brokers — the code naming the SDK's `queue`, a mailer's sealed outbox on a database among it — move there, behind `plug.Queues`; `framework/kit/queue` imports `framework/kit/store` (Q4) — done (2026-10-17, §Step 4, done) | no (the facade imports it) |
| 5 | `workflowkit` + `framework/kit/workflow` (Q3): the SDK's `statemachine` instantiated once, behind `plug.Workflows`; `framework/kit/workflow` imports `framework/kit/store` (Q4) — done (2026-10-17, §Step 5, done) | no (the facade imports it) |
| 6 | `mailkit` + `framework/kit/mail`: the mailers' outbox — the SDK's mail spool — and transports move there, behind `plug.Mail`; `framework/kit/mail` imports `framework/kit/queue` and `framework/kit/store` (Q4); `plug.HandleProvider` goes — done (2026-10-17, §Step 6, done) | no (the facade imports it) |
| 7 | `jobkit` + `framework/kit/jobs`: the jobs' scheduler — the SDK's `scheduler` — and the schedules it reads from a job's declaration move there, behind `plug.Jobs`; `framework/kit/jobs` imports no other registration package — done (2026-10-17, §Step 7, done) | no (the facade imports it) |
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
- **A product's code**, apart from its imports; a connector's changes with
  `kit.Engine`'s dialect type (Q5).

### Measurement: target and method

- **The probe** (`framework/kit/testdata/cliprobe`, `-trimpath`): `go list -deps`
  without the packages §1 takes out of the core, `go tool nm` without their symbols, machine
  code down by at least 2.15 MB — 90 % of the 2.39 MB step 0 measured
  (§Step 0, measured), which replaces the estimate of ≈ 1.95 MB less
  `database/sql`'s 0.066 MB and the first target of 1.5 MB —, the file at
  least 6.8 MB smaller (90 % of 7.52 MB), inits (`GODEBUG=inittrace=1`,
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

### Step 0, measured (2026-10-16)

Measured on `origin/main` at 33e09545 (Go 1.27.2, `GOTOOLCHAIN=local`,
`-trimpath`; the status line static, as its `Makefile` builds it), on the
8-vCPU VM of `framework/kit/BENCH.md`, every measure started at a 1-minute
load under 1.5 with no build, test or linter running (loads 0.5–1.2 at the
start of each round). Nothing of it is merged: the ceiling was built in a
throwaway copy of the tree, outside the repository.

**How the ceiling was built.** In the copy, `framework/internal/kit`'s
reachability of every subsystem of §1 was cut where a product enters it:
`mountPrivacy` mounts nothing and kit's three own stores leave `privacyRun`'s
type (so no `StoreService[T]` is instantiated); the databases, retention,
trash and scheduler components add nothing; `migrate`, `privacy`,
`revisions`, `watch` and `data` answer nothing; `storeRefusals` is empty; and
`words.go` reads `locales/en.json` alone with `encoding/json`, without the
SDK's `i18n` (step 8's shape). For the status line, the scheduler stays, since
its daemon declares two jobs (it will import `framework/kit/jobs`); the cut status line passes
its 88 parity goldens. The linker then drops what nothing reaches. The SDK
packages that leave the import graph — 22 for the probe, `docstore`, `queue`,
`statemachine`, `mail/spool`, `scheduler`, `i18n` and the SDK's `sql` (the
facade's `kit.Engine` dialect included, Q5), each with its core and service
halves, and `kernel/collections/heap` — are still imported by the copy, so
their remaining `init` is measured per package with `inittrace` and
subtracted; their remaining text is 0.003 MB (the inits themselves).

The units and counts are §Context's: a megabyte is 10⁶ bytes — the same
22,063,718-byte file §Context first wrote as 21.0, its size in MiB —, and
the binary is built with the same flags, its `.text` the same 7.02 MB.
`go list -deps` lists 363 packages, the probe's own main package among them:
362 are its dependencies, the figure the table counts; the count leaves no
package of §1 out either way.

| | cliprobe today | cliprobe, ceiling | delta | status line today | status line, ceiling | delta |
|---|---|---|---|---|---|---|
| file | 22.06 MB | 14.55 MB | **−7.52 MB (−34 %)** | 23.36 MB | 15.95 MB | **−7.41 MB (−32 %)** |
| text symbols (`go tool nm`, `T`/`t`) | 6.80 MB | 4.41 MB | **−2.39 MB** | 7.21 MB | 4.86 MB | **−2.35 MB** |
| `.gopclntab` | 5.50 MB | 3.47 MB | −2.03 MB | 5.82 MB | 3.82 MB | −2.00 MB |
| debug sections | 5.02 MB | 3.65 MB | −1.37 MB | 5.35 MB | 4.00 MB | −1.35 MB |
| packages | 362 | 340 | −22 | 401 | 382 | −19 |
| init lines | 149 | 137 | −12 | 183 | 172 | −11 |
| init, median (`inittrace`) | 0.90–0.99 ms | 0.81–0.91 ms | **≈ −0.08 ms** (three series of 50) | 1.186 ms | 1.100 ms | **≈ −0.09 ms** (40 runs of `daemon status`) |
| warm p50, fresh process | 4.57–4.70 ms | 4.28–4.33 ms | **−0.29 / −0.37 ms** | 5.86–5.99 ms | 5.67–5.71 ms | −0.19 / −0.29 ms |
| control (same bytes, another path) | 4.44–4.63 ms | | 0.07 / 0.12 ms from today | 6.16–6.42 ms | | **+0.18 / +0.27 ms** from today |

The probe's p50 is two runs of 8 alternated rounds × 300 execs of the three
binaries (today, the ceiling, today's byte-for-byte copy); the status line's
is `latency-ab.sh`, 6 rounds × 300, warm, `busy/default`, run twice against
today, twice against its control and once against the control's copy
(−0.52 ms).

The text the ceiling removes, by owner (probe; the status line's differs by
at most 0.015 MB per line, the scheduler apart):

| Owner | Machine code removed |
|---|---|
| kit's generic store family (`StoreService`, `sealedEngine`, `historied`, `docEngine`, `sqlEngine`, …) | 0.866 MB |
| SDK `docstore` | 0.693 MB |
| the rest of `framework/internal/kit` (the privacy journal and holds, sealing, retention, trash, the store commands, the queues, the catalogues) | 0.368 MB |
| the standard library only these reach | 0.152 MB |
| SDK `queue` | 0.113 MB |
| other SDK packages only these reach | 0.064 MB |
| SDK `i18n` | 0.044 MB |
| SDK `sql` | 0.041 MB |
| `database/sql`'s code (its types stay, Q5: no database is declared, so nothing reaches its code) | 0.035 MB |
| SDK `scheduler` (the probe; the status line keeps it, 0.010 MB, and its file 0.07 MB) | 0.009 MB |
| **total** | **2.39 MB** |

**Reading.**

- The binary is where the subsystems are: a third of the file and 35 % of
  the machine code, more than §Context's estimate (1.95 MB) because the rest
  of kit's code that only the subsystems reach, and the standard library under
  it, go with them. `net/http` and its closure (0.59 MB) stay.
- The initialisation is not: about 0.08–0.09 ms of it, 22 packages whose
  inits ADR 0176 had already made almost free.
- On the probe, the ceiling runs 0.29–0.37 ms faster than today, outside its
  control (0.07–0.12 ms) and the 0.19 ms floor in three of the four
  comparisons; against today's copy in the second run, 0.17 ms, inside it.
  The gain is claimed for the probe at about 0.3 ms.
- On the status line, the ceiling is faster in each of its three comparisons
  (−0.19 to −0.52 ms), but the byte-for-byte control itself moved by 0.18 and
  0.27 ms: by §Measurement's criterion the gain is **within the noise**, and is
  not claimed. Its binary is 7.41 MB smaller whatever the latency says.
- So of the status line's +1.08 ms over the commit before kit, the subsystems
  of §1 are at most about 0.3 ms; the rest is `net/http`, the rest of kit and
  the runtime (§Deferred). Q1 decided that the migration proceeds whatever
  this says; the measured ceiling sets §Measurement's targets above.

Not measured by step 0: a product with many stores (the ≈ 0.3 MB per entity
type of §Context stays an estimate until step 2), the status line against the
commit before the kit migration with the cut (§Measurement's run (b)), and a
build where the imports are removed rather than unreached (the leaving
packages' inits are subtracted, not removed).

### Step 2, done (2026-10-17)

The store's engine is one, over documents, inside kit's core. `StoreService[T]`
embeds `storeCore`, the store without its entity type, and keeps the typed
facade — its exported methods, its declarations, `Revisions`/`Revision`, a few
lines each; `docEngine`, `sqlEngine`, `sealedEngine` and `historied` implement
`plug.StoreEngine` and its siblings over `plug.DocumentValue`, the SDK's
`docstore` instantiated once at kit's record type (`docRecord`); the retention,
the trash's agenda, the holds, an erasure, an export, the revisions, `Records`
and `data copy` run on the core; what the product declares on its type is
wrapped once at the declaration (`entityOf`, `privacyFuncsOf`, an index's key
function). The bytes at rest are `json.Marshal`'s, as before, and `storetest`
passes unchanged, in kit's suite and the SQLite connector's. Two parts of
§3's contract are left to step 3, which moves the engines to `storekit`: the
core opens an engine with its own configuration, not `plug.StoreConfig` (a
version's hold is asked with the write's context on a database, and a write's
stamp is read from its context), and an engine's refusals are still the SDK's
`docstore` sentinels, which the core words.

Measured as step 0 was (Go 1.27.2, `-trimpath`, text symbols `T`/`t` by
`go tool nm -size`), before on `origin/main` at bfa13054:

| | cliprobe before | cliprobe after | delta |
|---|---|---|---|
| file | 22.08 MB | 18.21 MB | **−3.87 MB (−18 %)** |
| text symbols | 6.80 MB | 5.65 MB | **−1.15 MB** |
| `.text` / `.gopclntab` / debug | 7.02 / 5.51 / 5.03 MB | 5.84 / 4.40 / 4.43 MB | −1.18 / −1.10 / −0.60 MB |
| kit's generic code (instantiations) | 0.915 MB (`StoreService` 0.418, `sealedEngine` 0.132, `historied` 0.107, `sqlEngine` 0.071, `docEngine` 0.059, `retentionRun` 0.033, `trashRun` 0.022) | 0.035 MB (`StoreService` 0.018, the rest under 0.01) | −0.88 MB |
| SDK `docstore` | 0.696 MB | 0.197 MB | −0.50 MB |
| packages | 363 | 363 | 0 (step 3's) |

The cost of an entity type, measured on a product declaring 1 and 21 stores of
distinct shapes, each called through `Put`, `Get`, `List`, `Update` and
`Delete`: before, **0.41 MB of code and 1.36 MB of file per type** (§Context's
≈ 0.3 MB was the estimate); after, 0.011 MB of code and 0.061 MB of file. A
benchmark of the facade over 100 records (a temporary one, the numbers in the
pull request) found `Get`, `List` and `Update` no slower in memory and in the
data directory — fewer allocations each —, `Update` in files bound by its
`fsync` either way.

### Step 3, done (2026-10-17)

The stores' implementation is `framework/internal/kit/storekit`, which
`framework/kit/store` plugs in (`plug.Stores`) as it is imported, and which
`framework/kit` itself imports until step 10 (`subsystems.go`, Q8): the SDK's
document store in memory, in the data directory and over SQL (`fileEngine`,
`sqlEngine`, instantiated once at `docRecord`), kit's scope on a database
(the SDK's `Transactor`, `Checker` and `Migrator` behind `plug.Database` and
its siblings) and the migrations of kit's tables (`TableMigration`). kit's
core imports neither `pkg/v1/data/docstore` nor `pkg/v1/data/sql`:
`framework/kit`'s initgraph test took both off its allow-list. The two parts
of §3's contract step 2 left are settled: the core opens every engine with a
`plug.StoreConfig` — a version's hold asked with the write's context
(`Held(ctx, key)`), the write's stamp read from its context (`Stamp`), kit's
transaction as an engine takes part in it (`plug.Transaction`: the data's undo
log, the hooks held until the commit, a rollback on a database told again),
a call's context on a database (`Call`) —, and an engine's refusals are kit's
own kinds (`plug.Refusal`, `plug.RefusalError`, `plug.Refused`): storekit
tells each SDK sentinel by its kind, the SDK's error kept inside for its
fields and its cause, and the core matches kinds and words them as before.
`kit.Engine.Dialect` returns kit's own `Dialect` (`DialectPostgres`,
`DialectMySQL`, `DialectSQLite`, Q5); the three connectors return it and
import `framework/kit/store`. The start's check of §6 reads `plug.Stores`:
`subsystems_internal_test.go` takes the hook out and finds a store and a
database refused with `CodeSubsystemMissing`, every declaration at once. The
bytes at rest are unchanged, and `storetest` passes unchanged in kit's suite
and on SQLite, PostgreSQL 17 and MySQL 8.4.

Three things of the SDK's sql the core still reaches, each recorded where it
is: its graph holds `pkg/v1/data/sql` through `pkg/v1/data/queue`, whose SQL
broker names it, until step 4 (the test allows a package the core reaches
only through one still on the list); the queues' SQL broker and the mail
spool, still in the core, take the SDK's transaction manager and dialect,
which storekit hands on behind a pointer the core takes from those packages'
configuration without naming the type (`plug.HandleProvider`), until steps 4
and 6; and storekit writes a consumer's queue's table migration with the SDK's
queue until step 4. `kit.Migrations` keeps its signature: the facade hands
the core the SDK's `sql.Migration`s as `plug.MigrationValue`s, the same
versions, names and steps on the standard library's executor — which leaves
the facade importing the SDK's sql, an open point for step 10, whose
invariant forbids it: a migration type of kit's own, or `Migrations` behind
the registration package, is that step's to decide.

Measured as steps 0 and 2 were (Go 1.27.2, `-trimpath`, MB = 10⁶ bytes, text
symbols `T`/`t` by `go tool nm -size`), before on `origin/main` at 0ef3e768.
`coreprobe` is cliprobe on `framework/internal/kit` alone — the facade
imports every registration package until step 10, so a product built on it
links them all —, and `storeprobe` coreprobe with `framework/kit/store`:

| | cliprobe before | cliprobe after | coreprobe before | coreprobe after | storeprobe after |
|---|---|---|---|---|---|
| packages (`go list -deps`) | 363 | 365 | 362 | **359** | 364 |
| file | 18.206 | 18.203 | 18.194 | **17.253 (−0.94)** | 18.191 |
| `.text` | 5.844 | 5.828 | 5.843 | **5.519 (−0.32)** | 5.826 |
| text symbols | 5.655 | 5.639 | 5.654 | **5.339 (−0.32)** | 5.638 |
| `.gopclntab` / debug | 4.403 / 4.427 | 4.411 / 4.427 | 4.399 / 4.420 | 4.157 / 4.237 | 4.408 / 4.420 |
| kit's core | 1.019 | 0.990 | 1.019 | 0.988 | 0.990 |
| `storekit` | — | 0.046 | — | 0 | 0.046 |
| SDK `docstore` | 0.194 | 0.159 | 0.194 | **0** | 0.159 |
| SDK `sql` | 0.041 | 0.041 | 0.041 | 0.002 (through `queue`) | 0.041 |
| `database/sql` | 0.061 | 0.061 | 0.061 | 0.050 | 0.061 |

**Reading.** A product that imports framework/kit links what it linked
(−0.016 MB of code, two packages more: `store` and `storekit`). On kit's core
alone, the SDK's document store is gone and its sql nearly so, three packages
fewer, 0.32 MB of code and 0.94 MB of file less. That is far from
§Measurement's 2.15 MB, and the table says why: kit's core still links its own
store code — the privacy journal and holds it mounts, sealing, history,
revisions, trash, retention, the database component and the commands
(`migrate`, `privacy`, `revisions`, `data copy`) —, which names no SDK type
and so may stay in the core, but §3's proviso does not hold yet: it is
reached from `App.Main` directly, not only through a value the store package
provides. Cutting that reachability — the core's store code registered by
`framework/kit/store` as `framework/kit/server` registers kit's server
(`EnableServer`), so that a program which does not import it links none of
it — is the change that brings the stores' share of the step-0 ceiling, and
the next one to make before step 4. Not measured here: the initialisation and
the latency, which the cut leaves unchanged for a product of the facade.

### Step 3b, done (2026-10-17)

The change §Step 3, done named: kit's own store code — which names no SDK
type and so stays in the core (§3) — is reached only through a value the
store package provides. `framework/kit/store` calls `EnableStores` (on
`EnableServer`'s model) besides `storekit.Enable`; it sets `storeCode`, the
entries of kit's store code the rest of the core calls: kit's privacy service
mounted with its stores (the data keys, the holds, the journal), the
components of the databases, the retention and the trash, the journal's
append, the problems of sealing, of kit's index key, of the databases and of
the privacy declarations, and Main's commands on the stores — `migrate`,
`privacy`, `revisions`, `data` and `watch`, whose replay opens the watched
stores and journals the read. Each call site keeps its name and falls back to
what an app without a store does: nothing to mount, start or warn of. A
command on the stores, without the subsystem, says the import it needs
(`subsystem.missing`) and exits 78. The classification of the types a
product shows is not store code: without the subsystem it is judged all the
same, once (`classificationProblems`). The Studio's routes on the stores stay
reached through the Studio alone, served only with the subsystem.

Two things the linker needs, found by reading its reachability
(`-ldflags=-dumpdep`): no hook's signature names a type of the store family,
and neither does the app's own type — kit's own stores were three typed
fields of `App`, whose type descriptor kept `StoreService` and every method
under it linked in a program that declares no store; they are one value held
as `any`, read by the store code alone, and whether a store of data keys is
mounted is read from its secret, `data-key`, declared with it. The start's check of §6 also refuses, without the
subsystem, a node that seals what it keeps at rest — a topic, a queued
command or a mailer sealed on disk —, since the data keys it seals under are
kept in kit's own store (Q4's reason, ahead of steps 4 and 6). `framework/kit`'s
initgraph test lists one function of each part of that code (`kitStoreCode`)
and requires none of them in `coreprobe` and every one in `storeprobe`.

Measured as the steps before (Go 1.27.2, `-trimpath`, MB = 10⁶ bytes, text
symbols `T`/`t` by `go tool nm -size`), before on `origin/main` at 7b32c0cd:

| | cliprobe before | cliprobe after | coreprobe before | coreprobe after | storeprobe before | storeprobe after |
|---|---|---|---|---|---|---|
| packages (`go list -deps`) | 365 | 365 | 359 | 359 | 364 | 364 |
| file | 18.209 | 18.220 | 17.252 | **14.792 (−2.46)** | 18.193 | 18.209 |
| `.text` | 5.828 | 5.831 | 5.519 | **4.635 (−0.88)** | 5.827 | 5.830 |
| text symbols | 5.639 | 5.642 | 5.339 | **4.482 (−0.86)** | 5.638 | 5.641 |
| `.gopclntab` / debug | 4.411 / 4.428 | 4.415 / 4.430 | 4.157 / 4.236 | 3.527 / 3.684 | 4.408 / 4.422 | 4.411 / 4.424 |
| kit's core | 0.991 | 0.993 | 0.988 | **0.420 (−0.57)** | 0.991 | 0.993 |
| SDK `queue` (core and service) | 0.114 | 0.114 | 0.112 | **0.001** | 0.114 | 0.114 |
| `slices` (instantiations) | 0.407 | 0.407 | 0.379 | 0.320 | 0.407 | 0.407 |
| SDK `secret` | 0.090 | 0.090 | 0.090 | 0.056 | 0.090 | 0.090 |
| `database/sql` | 0.061 | 0.061 | 0.050 | 0.028 | 0.061 | 0.061 |
| SDK `i18n` / `scheduler` (steps 8, 7) | 0.044 / 0.010 | same | 0.044 / 0.010 | same | 0.044 / 0.010 | same |
| init lines / median of 50 (`inittrace`) | 151 / 0.91 ms | 151 / 0.87 ms | 147 / 0.84–0.86 ms | 147 / 0.71–0.72 ms | — | — |

**Reading.** On kit's core alone, against step 0's base (§Step 0, measured:
the probe's text symbols 6.80 MB, its file 22.06 MB), the code is down by
**2.32 MB** (6.80 → 4.48) and the file by **7.27 MB** (22.06 → 14.79):
§Measurement's targets — at least 2.15 MB of code and 6.8 MB of file — are
met, and the probe is within 0.07 MB of code and 0.24 MB of file of step 0's
ceiling (4.41 / 14.55), which also took the queues, the scheduler and the
catalogues out. The SDK's queue went with the store code: nothing but the
databases and the watches' replay reached its brokers in a product that
declares no topic. What remains of §1's packages in the probe — `i18n`
(0.044 MB, step 8), the scheduler (0.010 MB, step 7), `database/sql`
(0.028 MB, its types and what `kit.Engine` reaches, Q5) — is theirs, and
`net/http` and its closure are §Deferred. A product
of the facade links what it linked (+0.003 MB of code, the hooks): every
store engine and every line of kit's store code is still there, through the
facade's own import. The packages do not change — no import moved, only
reachability —, so the initialisation is the same 147 inits; the median of
50 is about 0.13 ms lower, at a 1-minute load of 1.5, which this step does not
claim. Not measured: the latency of a fresh process, and the status line,
which imports the facade and so links all of it until step 10.

### Step 4, done (2026-10-17)

The queues' implementation is `framework/internal/kit/queuekit`, which
`framework/kit/queue` plugs in (`plug.Queues`) as it is imported, and which
`framework/kit` itself imports until step 10 (`subsystems.go`, Q8). Per Q4,
`framework/kit/queue` imports `framework/kit/store`, and `queuekit` imports
`storekit`: a queue on a database runs on the transaction manager and in the
dialect of the database the store subsystem opened — `storekit.SQLOf`, typed
—, so that a publication joins the transaction its context carries, as ADR
0151 and ADR 0178 C1 require; never the other way, and a program that imports
`framework/kit/store` alone links nothing of the queues. kit's core imports
neither `pkg/v1/data/queue` nor, through it, `pkg/v1/data/sql`: the initgraph
test took `queue` off its allow-list.

§3's contract for the queues (`plug/queue.go`, `plug/queue_interface.go`):
the core opens every queue — a subscription's, a watch's, a queued command's,
a mailer's outbox on a database, and both sides of a data copy's — with a
`plug.QueueConfig` (its place: a directory, a `plug.Database`, memory; its
`QueuePolicyValue`; the app's clock as a `plug.Clock`, in standard-library
types, a test's manual clock included) and holds a `plug.Broker`: `Publish`,
`Consume` — the SDK's `queue.Consume`, run by `queuekit` over
`plug.ConsumeConfig` and `plug.DeliveryValue` —, `DeadLetters`, and the data
copy's sibling `MessageMover`. The mark of a delivery no retry helps is the
driver's (`DoNotRetry`, `NotRetryable`); the migration of a queue's table is
too (`TableMigration`, which `plug.TableSpec` no longer marks). The core still
encodes every message and holds every rule of ADR 0151 and 0178 — what a
publication in a transaction joins, the writer turn, the key of a queued
command, the catch-up of a watch —: a queue carries bytes. A mailer's sealed
outbox is `queuekit`'s, the sealing itself kit's, handed in `Seal` and
`Unseal`. The bytes at rest are unchanged: the same tables
(`kit_queue__…`), the same file queues, the same sealed boxes.

The start's check of §6 gains the queue row: a topic, a subscription — a
watch is one —, a queued command and a mailer whose outbox is on a database
are refused without `framework/kit/queue`, every declaration at once, with
`CodeSubsystemMissing`, never a command that is not queued
(`TestAQueueWithoutItsSubsystemIsRefused`); a queue opened without the
subsystem — Main's watch replay, a data copy — is refused with the import it
needs (`queuesMissing`).

`plug.HandleProvider` is off the queues: the core no longer asks a database
for the SDK's transaction manager or dialect, and `storekit`'s `Handle`
answers one handle only. It stays for the mail spool alone, until step 6: the
spool still in the core takes the SDK's types — a database's transaction
manager as its `Deferrer`, and now the SDK's broker behind the queue
subsystem's outbox as its `Broker`, which `queuekit`'s broker hands on
through the same interface (the mailer calls it inline) —, so its two
function-level ktn exclusions (`HandleProvider.Handle`,
`databaseRun.handle`) stay, their reason narrowed to the spool.

`initgraph_external_test.go` follows: `kitEngines` gains
`framework/internal/kit/queuekit` for `framework/kit/queue`; `throughStill`
reads the SDK package and its core and service halves, and a package the
smallest product reaches only through one still on the list may link its
inits alone (`beyondInits`) — in `coreprobe`, the SDK's `sql` and `queue`
link nothing but `init` (172 bytes for the queue's service half's
`regexp.MustCompile` and wake tables), the mail spool importing them, until
step 6; `TestAProductThatImportsTheStoresLinksThem` requires that
`storeprobe` link nothing of `queuekit` nor of the SDK's queue beyond its
init; and `TestAProductThatImportsTheQueuesLinksThem` builds
`testdata/queueprobe` (coreprobe with `framework/kit/queue`) and requires
`queuekit`'s and the SDK's queue's code linked, and the store subsystem's.

Measured as the steps before (Go 1.27.2, `-trimpath`, MB = 10⁶ bytes, text
symbols `T`/`t` by `go tool nm -size`), before on `origin/main` at 393a0faa:

| | coreprobe before | coreprobe after | storeprobe before | storeprobe after | queueprobe after | cliprobe before | cliprobe after |
|---|---|---|---|---|---|---|---|
| packages (`go list -deps`) | 359 | **357** | 364 | 363 | 366 | 365 | 367 |
| file | 14.792 | 14.781 | 18.209 | **17.783 (−0.43)** | 18.255 | 18.220 | 18.264 |
| `.text` | 4.635 | 4.634 | 5.830 | **5.661 (−0.17)** | 5.842 | 5.831 | 5.843 |
| text symbols | 4.482 | 4.481 | 5.641 | 5.476 | 5.653 | 5.642 | 5.654 |
| `.gopclntab` / debug | 3.527 / 3.684 | 3.528 / 3.687 | 4.411 / 4.425 | 4.310 / 4.344 | 4.424 / 4.436 | 4.415 / 4.431 | 4.427 / 4.441 |
| kit's core | 0.421 | 0.422 | 0.995 | 0.991 | 0.991 | 0.996 | 0.992 |
| `queuekit` | — | 0 | — | 0 | 0.017 | — | 0.017 |
| SDK `queue` (core and service) | 0.0002 (its init) | 0.0002 (its init) | 0.114 | **0.0002** | 0.114 | 0.114 | 0.114 |
| SDK `sql` | 0.0009 (inits) | 0.0009 (inits) | 0.041 | 0.041 | 0.041 | 0.041 | 0.041 |
| init lines (`inittrace`) | 147 | 145 | 150 | 149 | 151 | 151 | 152 |

**Reading.** On kit's core alone almost nothing moves, as expected: since
step 3b nothing but the databases and the watches' replay reached the
queues' brokers in a product that declares no queue, so the probe already
linked the SDK's queue's init alone; what goes is the import — two packages
fewer, `pkg/v1/data/queue` and `pkg/v1/data/sql`, two inits fewer. The SDK's
queue's two halves stay in the graph, imported by the mail spool, their
code unlinked: step 6's. The gain is a product that declares stores and no
queue: `storeprobe` no longer links the SDK's queue (0.114 MB of code, 0.43 MB
of file), which `storekit` brought with the migration of a consumer's queue's
table. A product of the facade links what it linked, plus `queuekit`'s
conversions (+0.012 MB of code, two packages more: `queue` and `queuekit`).
Not measured: the initialisation's time and the latency, which a product of
the facade leaves where they were.

### Step 5, done (2026-10-17)

The workflows' engine is `framework/internal/kit/workflowkit`, which
`framework/kit/workflow` plugs in (`plug.Workflows`) as it is imported, and
which `framework/kit` itself imports until step 10 (`subsystems.go`, Q8). Per
Q4, `framework/kit/workflow` imports `framework/kit/store`: a workflow runs
over a store; `workflowkit` imports `plug` and the SDK alone — the store is
the core's port —, and a program that imports `framework/kit/store` alone
links nothing of the workflows. kit's core imports none of
`pkg/v1/app/statemachine`: the initgraph test took it off its allow-list.

Per Q3, the SDK's statemachine is instantiated once, at
`plug.WorkflowEntityValue` — the product's entity held as `any`, and its
state spelled — and `string`, the state's spelling: `fmt.Sprint`, the name
the workflow's file and the Studio always wrote a state with.
`WorkflowService[E, S]` stays the typed facade, `svc.Workflow` and every
method unchanged: it declares the workflow with a `plug.WorkflowConfig` — its
transitions (`plug.TransitionSpec`), its `OnEnter` and `OnTransition` hooks,
its guards and instants, each wrapped once into a function of a
`WorkflowEntityValue`, the entity's state field set from the spelling the
engine set (`entityOf`) and spelled back from it (`entityValue`), so that an
`OnEnter` hook that changes its state is still refused —, opens it through
the hook and holds a `plug.WorkflowEngine`. The ports the engine runs on are
the core's and name no entity type: `plug.WorkflowStore`, over the workflow's
store (`workflowPort`), and `plug.WorkflowJournal`, over the workflow's file
or its table on a database (`workflowJournal`, `sqlJournal`, no longer
generic). The engine's failures stay the SDK's errors, their fields kept; the
core tells the ones it words apart through the driver's `Failed`
(`plug.WorkflowFailure`), never by matching a sentinel itself, and an
`OnTransition` hook a transaction held is reported without building one. The
bytes at rest are unchanged: the same workflow files
(`<service>/<workflow>.workflow.json`), the same tables
(`<service>__<workflow>__workflow`).

The start's check of §6 gains the workflow row: a workflow declared without
`framework/kit/workflow` is refused with `CodeSubsystemMissing`, every
declaration at once (`TestAWorkflowIsRefusedWithoutItsSubsystemAlone`), and a
workflow started without it — which the check makes unreachable — says the
import it needs (`workflowsMissing`).

`initgraph_external_test.go` follows: `stillInCore` loses `statemachine`,
`kitEngines` gains `framework/internal/kit/workflowkit` for
`framework/kit/workflow`; `coreprobe` links no symbol of either — not even
`statemachine`'s init, the package being out of its graph —, `storeprobe`
and `queueprobe` none either, and
`TestAProductThatImportsTheWorkflowsLinksThem` builds `testdata/workflowprobe`
(coreprobe with `framework/kit/workflow` and two workflows over entities and
states of distinct types) and requires `workflowkit`'s and the SDK's
statemachine's code linked, the store subsystem's, nothing of the queues',
the facade compiled at both workflows' types and the engine instantiated at
kit's entity alone. Since the engine knows a state by its spelling, a
workflow that declares two states spelled alike is refused at the start
(`workflow.state-spelled-twice`), never run as one state.

Measured as the steps before (Go 1.27.2, `-trimpath`, MB = 10⁶ bytes, text
symbols `T`/`t` by `go tool nm -size`, init medians of 50 runs at a 1-minute
load under 1.5), before on `origin/main` at 63ec721a:

| | coreprobe before | coreprobe after | storeprobe before | storeprobe after | queueprobe before | queueprobe after | workflowprobe after | cliprobe before | cliprobe after |
|---|---|---|---|---|---|---|---|---|---|
| packages (`go list -deps`) | 357 | **354** | 363 | 360 | 366 | 363 | 365 | 367 | 369 |
| file | 14.781 | 14.767 | 17.783 | 17.771 | 18.255 | 18.242 | 18.656 | 18.265 | **18.684 (+0.42)** |
| `.text` | 4.634 | 4.633 | 5.661 | 5.660 | 5.842 | 5.841 | 5.911 | 5.843 | 5.956 |
| text symbols | 4.481 | 4.480 | 5.476 | 5.475 | 5.653 | 5.652 | 5.714 | 5.654 | 5.760 |
| `.gopclntab` / debug | 3.528 / 3.687 | 3.528 / 3.685 | 4.310 / 4.344 | 4.310 / 4.344 | 4.424 / 4.436 | 4.424 / 4.436 | 4.544 / 4.494 | 4.427 / 4.441 | 4.540 / 4.518 |
| kit's core | 0.421 | 0.421 | 0.988 | 0.988 | 0.989 | 0.989 | 1.117 | 0.989 | 0.995 |
| `workflowkit` | — | 0 | — | 0 | — | 0 | 0.010 | — | 0.010 |
| SDK `statemachine` (core and service) | 0.001 (its inits) | **0** | 0.001 | **0** | 0.001 | **0** | 0.078 | 0.001 | 0.077 |
| init lines / median of 50 (`inittrace`) | 145 / 0.82 ms | 144 / 0.70 ms | 149 / 0.78 ms | 148 / 0.76 ms | 151 / 0.80 ms | 150 / 0.80 ms | 151 / 0.82 ms | 152 / 0.78 ms | 154 / 0.85 ms |

And a product that declares workflows — a throwaway one on the facade, built
in both trees and not kept, declaring 1 or 11 workflows over stores of
distinct entity and state types, each with an event, a timer, a deadline and
a guard, each called through `Start`, `Fire` and `Census`:

| | 1 workflow before | 1 workflow after | 11 workflows before | 11 workflows after |
|---|---|---|---|---|
| file | 18.971 | 19.007 | 22.883 | **21.194 (−1.69)** |
| text symbols | 5.849 | 5.859 | 7.158 | **6.540 (−0.62)** |
| SDK `statemachine` | 0.085 | 0.084 | 0.654 | **0.084** |
| kit's core | 1.078 | 1.078 | 1.799 | 1.743 |

**Reading.** On kit's core alone, three packages fewer —
`pkg/v1/app/statemachine` and its core and service halves —, one init fewer,
and the 0.001 MB of their inits; the kernel's heap stays in the graph, which
other code of the core imports. `storeprobe` and `queueprobe` lose the same.
The gain is a product with workflows: before, the SDK's engine was compiled
once per workflow's entity and state types, about **0.057 MB of code and
0.17 MB of file per workflow type** on top of its facade and its store; after,
it is compiled once — 0.084 MB of code with its loop — whatever the product
declares, a workflow type costing its facade and its store alone (0.068 MB of
code and 0.22 MB of file per type, against 0.131 MB and 0.39 MB before). The
price is on the facade until step 10: since the hook holds the engine, a
product of the facade links it whether it declares a workflow or not —
`cliprobe` +0.107 MB of code and +0.42 MB of file, two packages more
(`workflow` and `workflowkit`) —, what a product that declares one paid
already; a product that imports only the registration packages it needs, as
`kit gen` will write them (§5), pays it only with a workflow. The init medians
are single series of 50 at a 1-minute load under 1.5: within the noise, and
not claimed. Not measured: the latency of a fresh process.

### Step 6, done (2026-10-17)

The mailers' outbox and transports are `framework/internal/kit/mailkit`,
which `framework/kit/mail` plugs in (`plug.Mail`) as it is imported, and
which `framework/kit` itself imports until step 10 (`subsystems.go`, Q8). Per
Q4, `framework/kit/mail` imports `framework/kit/queue` and
`framework/kit/store`, and `mailkit` imports `queuekit` and `storekit`: a
mailer's outbox on a database is the queue subsystem's sealed broker
(`queuekit.BrokerOf`, typed), joined to the transaction manager of the
database the store subsystem opened (`storekit.SQLOf`, a `Deferrer` that is
an `Aborter` too), so that a mail sent in a transaction exists if and only if
it commits (ADR 0178 C1, C7 item 7); never the other way, and a program that
imports `framework/kit/store` or `framework/kit/queue` alone links nothing of
the mailers. kit's core imports no `pkg/v1/app/mail/spool`: the initgraph test
took it off its allow-list, and with it the SDK's `queue` and `sql`, which
only the spool still brought into the core's graph, leave it — not even their
inits remain.

§3's contract for the mail (`plug/mail.go`, `plug/mail_interface.go`): the
core opens every mailer's outbox with a `plug.MailSpoolConfig` — kit's
attempt (`Transport`, told a `MailAttemptValue`: the core's span, loop and
Studio simulation around the delivery), the app's `Clock`, what kit hears of
each mail (`Observe`, a `MailEventValue`), the backoff's bounds, the attempts,
and where it keeps its mails: the queue subsystem's `Broker` on a `Database`,
a directory, or memory — and holds a `plug.MailSpool` (`Send`, `SendWithID`,
`DeadLetters`, `Run`, `Close`). It hands a mail to a `plug.MailSink` the
driver builds — the SDK's SMTP transport, through `ParseSMTP` and `NewSMTP`,
or its capture transport (`Capture`), whose sibling `MailCaptureSource` gives
the Studio the composed message —, asks the SDK's verdict on a mail
(`Validate`), and tells the spool's and the URL's refusals apart by kind
(`MailRefusal`, through the driver's `Refused`), never matching a sentinel
itself. A mail crosses as a `MailValue`, the SDK's value held as `any`. The
mailer's refusals keep kit's codes, the core wrapping what the subsystem
returns where its open and send paths did (`CodeMailQueue` — `OUTBOX_OPEN`,
`MAIL_QUEUE` —, `CodeMailEncode`, `CodeMailConfig`, `CodeMailPanic`); the
sender a mailer declares (`From`) is judged by the SDK's rules at the start,
with the other mailer problems, no longer at the declaration, which a
package's init may run before the subsystem's. The bytes at rest are
unchanged: the same spool directories, the same sealed tables
(`kit_queue__<service>__<mailer>__outbox`).

What §Context and §1 said stays: `Mailer.Send` takes the SDK's
`mail.Message`, so `pkg/v1/app/mail` stays in the core's graph as a type —
and with it the SDK's mail service half and `net/smtp`, which that facade
imports. They are initialised and not linked: `coreprobe` links nothing of
either beyond their inits (`mailTransport`), where it linked 0.014 MB of the
SDK's mail code before — the verdict on a sender, now the subsystem's.

`plug.HandleProvider` is gone, and its two function-level ktn exclusions
(`HandleProvider.Handle`, `databaseRun.handle`) with it: the mail spool was
its last reason to exist, and a subsystem that needs another's SDK handle now
asks that subsystem's package, typed (`storekit.SQLOf`, `queuekit.BrokerOf`).
ktn-linter 1.46.0 passes without them.

The start's check of §6 gains the mail row: a mailer declared without
`framework/kit/mail` is refused with `CodeSubsystemMissing`, every mailer at
once; beside the queue row, a mailer whose outbox is on a database is refused
for both packages when neither is imported, the refusal naming both
(`TestAMailerWithoutItsSubsystemIsRefused`), and a mailer started without the
subsystem — which the check makes unreachable — says the import it needs
(`mailsMissing`).

`initgraph_external_test.go` follows: `stillInCore` loses `mail/spool` —
`scheduler` and `i18n` remain —, `kitEngines` gains
`framework/internal/kit/mailkit` for `framework/kit/mail`; `coreprobe` links
no symbol of the spool, of `mailkit`, nor of the SDK's queue and sql, and
nothing of the SDK's mail transports beyond their inits; `storeprobe`,
`queueprobe` and `workflowprobe` link nothing of the mail either
(`linksNoMail`), `storeprobe` and `workflowprobe` not even the SDK's queue's
inits; and `TestAProductThatImportsTheMailLinksIt` builds `testdata/mailprobe`
(coreprobe with `framework/kit/mail` and a mailer) and requires `mailkit`'s,
the spool's and the transports' code linked, the queue and store subsystems',
and nothing of the workflows'.

Measured as the steps before (Go 1.27.2, `-trimpath`, MB = 10⁶ bytes, text
symbols `T`/`t` by `go tool nm -size`, init medians of 50 runs at a 1-minute
load of about 2), before on `origin/main` at 9d459560 — `mailprobe` before is
the same program without the import, its mailer then in the core:

| | coreprobe before | coreprobe after | storeprobe before | storeprobe after | queueprobe before | queueprobe after | workflowprobe before | workflowprobe after |
|---|---|---|---|---|---|---|---|---|
| packages (`go list -deps`) | 354 | **346** | 360 | 354 | 363 | 360 | 365 | 360 |
| file | 14.767 | 14.711 | 17.771 | 17.727 | 18.246 | 18.209 | 18.656 | 18.612 |
| `.text` | 4.633 | 4.619 | 5.660 | 5.648 | 5.841 | 5.829 | 5.911 | 5.899 |
| text symbols | 4.480 | 4.466 | 5.475 | 5.463 | 5.652 | 5.640 | 5.714 | 5.702 |
| `.gopclntab` / debug | 3.528 / 3.685 | 3.518 / 3.671 | 4.310 / 4.344 | 4.302 / 4.333 | 4.424 / 4.436 | 4.416 / 4.430 | 4.544 / 4.494 | 4.535 / 4.482 |
| SDK mail, service half | 0.0137 | **0.0012 (its inits)** | 0.0137 | 0.0012 | 0.0137 | 0.0012 | 0.0137 | 0.0012 |
| SDK queue / SDK sql | 0.0002 / 0.0009 (inits) | **0 / 0** | 0.0002 / 0.041 | 0 / 0.041 | 0.113 / 0.041 | same | 0.0002 / 0.041 | 0 / 0.041 |
| SDK mail spool, `mailkit` | 0, — | 0, 0 | 0, — | 0, 0 | 0, — | 0, 0 | 0, — | 0, 0 |
| init lines / median of 50 | 144 / 0.89 ms | 140 / 0.93 ms | 148 / 0.94 ms | 146 / 0.90 ms | 150 / 0.96 ms | 149 / 0.80 ms | 151 / 1.00 ms | 149 / 0.90 ms |

| | mailprobe before | mailprobe after | cliprobe before | cliprobe after |
|---|---|---|---|---|
| packages (`go list -deps`) | 354 | 365 | 369 | 371 |
| file | 16.172 | 18.612 | 18.684 | **18.967 (+0.28)** |
| `.text` | 5.112 | 5.979 | 5.956 | 6.059 |
| text symbols | 4.943 | 5.785 | 5.760 | 5.860 |
| `.gopclntab` / debug | 3.896 / 4.011 | 4.519 / 4.523 | 4.540 / 4.518 | 4.614 / 4.581 |
| kit's core | 0.677 | 1.018 | 0.995 | 0.997 |
| `mailkit` | — | 0.009 | — | 0.007 |
| SDK mail spool / SDK mail, service half / `net/smtp` | 0.012 / 0.059 / 0.006 | 0.012 / 0.059 / 0.006 | 0 / 0.014 / 0 | 0.010 / 0.057 / 0.006 |
| SDK queue / SDK sql | 0.057 / 0.001 | 0.121 / 0.041 | 0.113 / 0.041 | 0.121 / 0.041 |
| `storekit` / `queuekit` | — | 0.046 / 0.017 | 0.046 / 0.017 | 0.046 / 0.017 |
| init lines / median of 50 | 144 / 0.99 ms | 152 / 0.96 ms | 154 / 0.97 ms | 156 / 0.98 ms |

**Reading.** On kit's core alone, eight packages fewer — `pkg/v1/app/mail/spool`
and its two halves, and the SDK's `queue` and `sql` with theirs, which only
the spool still imported —, four inits fewer, and 0.014 MB of code: the SDK's
verdict on a mail, which the subsystem now gives. `storeprobe`, `queueprobe`
and `workflowprobe` lose the same SDK mail code and two to six packages each.
The smallest product's file barely moves (−0.06 MB): since step 3b nothing of
the spool was reached in a product without a mailer. A product with a mailer
now links the stores and the queues with it (`mailprobe` +0.84 MB of code,
+2.44 MB of file): Q4's price — an outbox on a database is a queue of the
store subsystem's database, and a sealed one needs kit's store of data keys —,
which a product of the facade paid already. The price is on the facade until
step 10, as step 5 said of the workflows: the hook holds the spool and the
transports, so a product of the facade links them whether it declares a
mailer or not — `cliprobe` +0.10 MB of code and +0.28 MB of file, two packages
more (`mail` and `mailkit`) —; a product that imports only the registration
packages it needs, as `kit gen` will write them (§5), pays it only with a
mailer. The init medians are single series of 50 at a 1-minute load of about
2: within the noise, and not claimed. Not measured: the latency of a fresh
process.

### Step 7, done (2026-10-17)

The jobs' scheduler is `framework/internal/kit/jobkit`, which
`framework/kit/jobs` plugs in (`plug.Jobs`) as it is imported, and which
`framework/kit` itself imports until step 10 (`subsystems.go`, Q8).
`framework/kit/jobs` imports no other registration package — Q4 names none
for it: a job runs over nothing the stores, the queues, the workflows or the
mailers provide —, and `jobkit` imports `plug` and the SDK alone. kit's core
imports none of `pkg/v1/app/scheduler`: the initgraph test took it off its
allow-list, which names `i18n` alone now.

§3's contract for the jobs (`plug/jobs.go`, `plug/jobs_interface.go`): a job
keeps what the product declared — a period (`Every`) or a cron expression
(`Cron`) —, and the core reads it into a schedule at the start, through the
hook's `Every` and `Cron` (a `plug.JobScheduleFunc`, the SDK's `Schedule` as
it is), never at the declaration: a declaration made in a package-level
variable runs at its package's init, which Go may order before the
subsystem's (§6). The verdict on a period that is not positive or a cron
expression of no five fields is the SDK's, given at the start with the other
declaration problems (`job.period`, `job.cron`, at the declaration's
position, as before). The core opens the app's scheduler with a
`plug.JobSchedulerConfig` — the app's `Clock`, a test's manual clock
included, and what kit hears of each fire (`OnResult`, a
`plug.JobResultValue`) — and holds a `plug.JobScheduler`, which it adds each
job to (`plug.JobEntryValue`) and runs. What the scheduler decides about time
stays the SDK's: an overlapping fire skipped and counted, a missed deadline
counted, never caught up, a cron expression across a DST change as the SDK's
`cron` reads it. The scheduler's component, its loop and each job's loop, the
span of a run and the overlap with a run the Studio started
(`errOverlapped`, which the SDK reports unwrapped as the run's error) stay
the core's; so do the workflows' timers, which start with the scheduler's
component: an app that declares no job runs the core's `idleScheduler`, which
holds no entry and waits for its end as the SDK's does with none, so a daemon
without a job needs no subsystem and shows the same loop.

The start's check of §6 gains the jobs row: a job — an interval's or a
cron's — declared without `framework/kit/jobs` is refused with
`CodeSubsystemMissing`, every job at once
(`TestAJobWithoutItsSubsystemIsRefused`), a daemon without a job is not
(`TestADaemonWithoutJobsNeedsNoJobsSubsystem`), and a scheduler opened for
jobs without the subsystem — which the check makes unreachable — says the
import it needs (`jobsMissing`, `TestAJobIsJudgedAtTheStartThroughItsSubsystem`).

`initgraph_external_test.go` follows: `stillInCore` loses `scheduler`,
`kitEngines` gains `framework/internal/kit/jobkit` for `framework/kit/jobs`;
`coreprobe` links no symbol of either — not even the scheduler's inits, the
package being out of its graph —, `storeprobe`, `queueprobe`,
`workflowprobe` and `mailprobe` none either (`linksNoJobs`), and
`TestAProductThatImportsTheJobsLinksThem` builds `testdata/jobsprobe`
(coreprobe with `framework/kit/jobs`, a job every minute and one on a cron
expression) and requires `jobkit`'s and the SDK's scheduler's code linked,
beyond its inits, and nothing of the store, queue, workflow and mail
subsystems nor of kit's own store code.

Measured as the steps before (Go 1.27.2, `-trimpath`, MB = 10⁶ bytes, text
symbols `T`/`t` by `go tool nm -size`, init medians of 50 runs, at a
1-minute load of 2.8 to 4.3 — the machine shared), before on `origin/main` at
293a76b9 — `jobsprobe` before is the same program without the import, its
scheduler then in the core:

| | coreprobe before | coreprobe after | storeprobe before | storeprobe after | queueprobe before | queueprobe after | workflowprobe before | workflowprobe after | mailprobe before | mailprobe after |
|---|---|---|---|---|---|---|---|---|---|---|
| packages (`go list -deps`) | 346 | **343** | 354 | 351 | 360 | 357 | 360 | 357 | 365 | 362 |
| file | 14.711 | 14.684 | 17.728 | 17.695 | 18.210 | 18.181 | 18.607 | 18.573 | 18.613 | 18.588 |
| `.text` | 4.619 | 4.610 | 5.648 | 5.639 | 5.829 | 5.820 | 5.899 | 5.889 | 5.979 | 5.970 |
| text symbols | 4.466 | 4.457 | 5.463 | 5.454 | 5.640 | 5.631 | 5.702 | 5.693 | 5.785 | 5.777 |
| `.gopclntab` / debug | 3.518 / 3.671 | 3.513 / 3.667 | 4.302 / 4.334 | 4.296 / 4.329 | 4.416 / 4.432 | 4.410 / 4.426 | 4.535 / 4.481 | 4.529 / 4.475 | 4.519 / 4.524 | 4.513 / 4.518 |
| SDK scheduler (core and service), `jobkit` | 0.0095 (24 symbols), — | **0, 0** | 0.0095, — | 0, 0 | 0.0095, — | 0, 0 | 0.0095, — | 0, 0 | 0.0095, — | 0, 0 |
| init lines / median of 50 | 140 / 0.81 ms | 139 / 0.83 ms | 146 / 0.84 ms | 145 / 0.95 ms | 149 / 0.99 ms | 148 / 0.94 ms | 149 / 0.94 ms | 148 / 0.96 ms | 152 / 0.94 ms | 151 / 0.93 ms |

| | jobsprobe before | jobsprobe after | cliprobe before | cliprobe after |
|---|---|---|---|---|
| packages (`go list -deps`) | 346 | 348 | 371 | 373 |
| file | 14.760 | 14.778 | 18.967 | **19.030 (+0.06)** |
| `.text` | 4.634 | 4.639 | 6.059 | 6.075 |
| text symbols | 4.480 | 4.485 | 5.860 | 5.875 |
| `.gopclntab` / debug | 3.533 / 3.684 | 3.541 / 3.681 | 4.614 / 4.580 | 4.633 / 4.596 |
| kit's core | 0.425 | 0.427 | 0.997 | 0.997 |
| SDK scheduler (core and service) / `jobkit` | 0.0208 / — | 0.0208 / 0.0023 | 0.0095 / — | 0.0208 / 0.0024 |
| init lines / median of 50 | 140 / 0.78 ms | 141 / 0.87 ms | 156 / 0.93 ms | 157 / 0.98 ms |

**Reading.** On kit's core alone, three packages fewer —
`pkg/v1/app/scheduler` and its core and service halves —, one init fewer,
and the scheduler's 0.0095 MB of code: a product that declares no job
linked `scheduler.New` and its loop all the same, the core opening the
scheduler whether a job was declared or not. `storeprobe`, `queueprobe`,
`workflowprobe` and `mailprobe` lose the same. The gain is small, as step 0
said it would be (0.009 MB of code in the ceiling); what matters is that the
scheduler leaves the core's graph, which §1 required. A product that declares
jobs links what it linked, plus `jobkit`'s conversions (+0.005 MB of code,
two packages more: `jobs` and `jobkit`) — the SDK's scheduler unchanged at
0.021 MB, its whole engine once a job is added. The price is on the facade
until step 10, as steps 5 and 6 said of theirs: the hook holds the scheduler,
so a product of the facade links all of it whether it declares a job or not —
`cliprobe` +0.015 MB of code and +0.06 MB of file, two packages more —; a
product that imports only the registration packages it needs, as `kit gen`
will write them (§5), pays it only with a job. The status line declares two
jobs in its daemon role: it will import `framework/kit/jobs` at step 9. The
init medians are single series of 50 at a 1-minute load of 2.8 to 4.3:
within the noise, and not claimed. Not measured: the latency of a fresh
process.

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
- Nothing for kit's languages (Q6): only the Studio shows a language
  other than English, and a program that mounts the Studio imports
  `framework/kit/studio` already.
- `kit.Engine` keeps its home and returns `*sql.DB` still, but its dialect is
  kit's own type instead of the SDK's `sql.Dialect` (Q5): a connector author
  changes `Dialect`; the three in-tree connectors follow in the same change.
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
  every product changes for a check the start already makes. Refused with Q2,
  and not needed for workflows once Q3 instantiates `statemachine` once.

## Decisions of 16 October — the owner's answers to the eight questions

The owner retained the recommendation of each question as written.

**Q1 — Step 0: a gate, or a measurement?**
**Decision.** A measurement, not a gate: the ceiling is recorded and sets
§Consequences' targets, and the migration proceeds whatever it says — the
decision is architectural (one way to plug, a product links what it imports),
not only a latency.

**Q2 — Where a product declares a subsystem.**
**Decision.** A blank import, written by `kit gen`; the declaring methods of
`kit.Service` and `kit.App` stay unchanged — no call site of any product
changes, and the start already refuses a missing import.

**Q3 — Workflows.**
**Decision.** The SDK's `statemachine` instantiated once, over kit's document
and the state as its encoded value; `WorkflowService[E, S]` stays a typed
facade and `svc.Workflow` is unchanged — the same shape as the stores, so no
constructor form is needed (Q2).

**Q4 — Do the registration packages import each other?**
**Decision.** `queue`, `workflow` and `mail` import `store`: a workflow is
over a store, a sealed message or mail needs kit's store of data keys, and a
queue on a database joins the store package's transaction — the import graph
states what the start would otherwise refuse.

**Q5 — The database `Engine` and `database/sql`.**
**Decision.** `kit.Engine` stays in the core with kit's own dialect type,
still returning `*sql.DB`: `database/sql` stays in the core, the SDK's `sql`
leaves it, and the three connectors change — product code is unchanged, and
`database/sql` is revisited only if step 0 shows it matters.

**Q6 — kit's diagnostics in other languages.**
**Decision.** English is a table in kit's core, written from
`locales/en.json`; the other catalogues and the SDK's `i18n` are plugged in
by `framework/kit/studio`, their only reader, with
`TestKitSpeaksEveryLanguage` — no new import for any product, and the
subsystem sits where its reader is, as ADR 0172 §2 put the process sampler.

**Q7 — Where the contract lives.**
**Decision.** In `plug`, one file per subsystem, standard library and
`model` only — one package of hooks, as today; a contract package of its own
per subsystem only once one outgrows it.

**Q8 — How the break ships.**
**Decision.** Steps 1–9 non-breaking behind the facade's own blank imports,
the break alone in step 10, after `kit gen` and the products have the imports
— every product moves before anything refuses it, and the break is one
release note.

## Revision history

| Version | Date | What changed |
|---|---|---|
| Proposed | 2026-10-16 | The design, with eight questions for the owner |
| Accepted | the owner's decision, 2026-10-16 | The recommendation of each of the eight questions retained as written; each question replaced by its *Decision* line; the status, the header and the passages that depended on an open question (Q2, Q3, Q5, Q6) updated — `database/sql` stays in the core and leaves §7's refused list. No other text changed |
| Accepted, review | 2026-10-16 | Step 3 says what moves to `storekit` and that `kit.Engine` stays in the core (Q5); §7 keeps the two invariants and leaves the test's package list, allow-list and gate to `framework/kit/CLAUDE.md`; steps 1–9 non-breaking, as Q8 decided |
| Accepted, step 0 measured | 2026-10-16 | §Step 0, measured: the ceiling built in a throwaway copy — the probe's file −7.52 MB, its code −2.39 MB, its init ≈ −0.08 ms, its warm p50 about −0.3 ms; the status line's file −7.41 MB, its latency gain within the noise of its control; §Measurement's probe target set from the ceiling (at least 2.15 MB of code, 6.8 MB of file); §Context's estimate marked measured; §Context's file size written in MB like every other figure (22.06 MB, the 21.0 it gave being MiB) and the package counts reconciled. No decision changed |
| Accepted, steps 1 and 2 done | 2026-10-17 | The migration table marks step 1 done (#359) and step 2 done; §Step 2, done records the engine over documents — the probe's file −3.87 MB, its code −1.15 MB, kit's generic code 0.915 → 0.035 MB, an entity type's cost 0.41 → 0.011 MB of code —, §Context's per-type estimate measured, and the two parts of §3's contract left to step 3 (`plug.StoreConfig`, the refusals). No decision changed |
| Accepted, step 3 done | 2026-10-17 | The migration table marks step 3 done; §Step 3, done records the stores' engines in `storekit` behind `framework/kit/store`, the core importing neither the SDK's `docstore` nor its `sql`, the two parts of §3's contract step 2 left settled (`plug.StoreConfig` with `Held(ctx, key)`, the stamp and kit's transaction; kit's own refusal kinds), `kit.Engine`'s dialect kit's own type (Q5), the SDK's sql still reached through the queues until step 4, `kit.Migrations`' facade conversion and the open point it leaves for step 10, and the measure — on kit's core alone, `docstore` gone, 0.32 MB of code and 0.94 MB of file less —, which says that the core's own store code is still reached from `App.Main`: the cut of that reachability is the next change. No decision changed |
| Accepted, step 3b done | 2026-10-17 | The migration table gains step 3b, done; §Step 3b, done records kit's own store code reached only through `framework/kit/store` (`EnableStores`), the start's check extended to a node that seals at rest, Main's commands on the stores refused without it, and the measure — on kit's core alone, §Measurement's probe target met against step 0's base. No decision changed |
| Accepted, step 4 done | 2026-10-17 | The migration table marks step 4 done; §Step 4, done records the queues' brokers in `queuekit` behind `framework/kit/queue`, which imports `framework/kit/store` (Q4) — `queuekit` reaching a database through `storekit.SQLOf` —, kit's core importing neither the SDK's `queue` nor, through it, its `sql`, the queues' contract in `plug` (`Queues`, `QueueConfig`, `Broker`, `MessageMover`, `Clock`), the start's check gaining the queue row (a topic, a subscription, a watch, a queued command, a mailer whose outbox is on a database), `plug.HandleProvider` off the queues and kept for the mail spool alone until step 6, and the measure — the smallest product two packages fewer, a product with stores and no queue 0.43 MB of file and 0.17 MB of code less. No decision changed |
| Accepted, step 5 done | 2026-10-17 | The migration table marks step 5 done; §Step 5, done records the workflows' engine in `workflowkit` behind `framework/kit/workflow`, which imports `framework/kit/store` (Q4), the SDK's statemachine instantiated once at `plug.WorkflowEntityValue` and the state's spelling (Q3), `WorkflowService[E, S]` the typed facade over it with its ports (`plug.WorkflowStore`, `plug.WorkflowJournal`) in the core, kit's core importing none of the SDK's statemachine, the start's check gaining the workflow row, and the measure — the smallest product three packages fewer, a product of 11 workflows 0.62 MB of code and 1.69 MB of file less, the engine linked by a product of the facade until step 10 (+0.11 MB of code). No decision changed |
| Accepted, step 6 done | 2026-10-17 | The migration table marks step 6 done; §Step 6, done records the mailers' outbox and transports in `mailkit` behind `framework/kit/mail`, which imports `framework/kit/queue` and `framework/kit/store` (Q4) — `mailkit` reaching an outbox on a database through `queuekit.BrokerOf` and `storekit.SQLOf` —, kit's core importing no `mail/spool` and, through it, neither the SDK's `queue` nor its `sql`, `pkg/v1/app/mail` kept in its graph as the type `Mailer.Send` takes, its transports and `net/smtp` unlinked, the mail's contract in `plug` (`Mail`, `MailSpoolConfig`, `MailSpool`, `MailSink`, `MailCaptureSource`, `MailRefusal`), `plug.HandleProvider` and its two ktn exclusions gone, a mailer's sender judged at the start, the start's check gaining the mail row beside the queue row, and the measure — the smallest product eight packages fewer, a product of the facade +0.10 MB of code until step 10. No decision changed |
| Accepted, step 7 done | 2026-10-17 | The migration table marks step 7 done; §Step 7, done records the jobs' scheduler in `jobkit` behind `framework/kit/jobs`, which imports no other registration package, kit's core importing none of the SDK's scheduler, a job's schedule read from its declaration at the start through the hook (`Every`, `Cron`) — never at the declaration —, the jobs' contract in `plug` (`Jobs`, `JobScheduleFunc`, `JobSchedulerConfig`, `JobScheduler`, `JobEntryValue`, `JobResultValue`), the core's `idleScheduler` for an app that declares no job, the start's check gaining the jobs row, and the measure — the smallest product three packages fewer and 0.009 MB of code less, a product of the facade +0.015 MB of code until step 10. No decision changed |

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
