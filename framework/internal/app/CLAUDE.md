<!-- updated: 2026-10-22T00:00:00Z -->
# framework/internal/app — the implementation of framework/app

Declarations (`NewService`, the generic methods of `*Service`), the runtime
(`App`), and the product's self-description (`App.Graph`, the read-only
`/_app/api` surface). Moved from `kitsunium/platform/kit` into the SDK's
framework (ADR 0147), a part of the SDK module since ADR 0162, and named
`framework/internal/app` since ADR 0180 step 3 (`framework/internal/kit`
before it, its engines `<name>kit`): the platform keeps a deprecated facade of aliases
over the SDK's, and the tools a product runs but does not link — the
Studio's pages, the analyzer, the generator. The platform's root `CLAUDE.md`
holds the invariants; this file maps them to files. Every one still holds
here, but invariant 4's Studio is now the kit tool's: the product serves the
API it reads, never its pages, and no route that acts (D13). An ADR
numbered below 0100 here is the platform's (`kitsunium/platform/docs/adr/`:
0003 settings, 0004 stores and databases, 0005 CQRS, 0006 classification,
0007 versions, 0008 modules, 0010 design-first); 0104, 0138, 0147, 0148 and 0149
are the SDK's.

## The facade

A product imports `framework/app`, never this package (Go refuses an
`internal/` import from outside the module). `framework/app` holds one file
per file of this package that exports something: type aliases, typed
constants and variables, and forwarders marked `//:` (and `//go:noinline`
where the implementation is). The SDK's convention (`pkg/v1/*` over
`internal/`) applies: here, a type's name says its role; the facade keeps the
name a product writes.

| Here | framework/app |
|---|---|
| `StoreService`, `TopicService`, `EndpointService`, `PortService`, `SettingService`, `WorkflowService`, `RecordsService`, `PasswordPolicyService` | `Store`, `Topic`, `Endpoint`, `Port`, `Setting`, `Workflow`, `Records`, `PasswordPolicy` |
| `SubscriptionWorker`, `AuthenticatorHandler` | `Subscription`, `Authenticator` |
| `StdioValue`, `DatabaseURLValue`, `EmptyValue`, `FieldRefValue` | `Stdio`, `DatabaseURL`, `Empty`, `FieldRef` |
| `ViolationMessage`, `WakeEvent`, `WrittenEvent`, `ChangeEvent`, `RevisionEvent` | `Violation`, `Wake`, `Written`, `Change`, `Revision` |
| `<X>Configurer` (method `<x>Configure`), for App, Command, Database, Endpoint, Expose, Listener, Loop, Mailer, Mount, Password, Port, Query, Secret, Setting, Static, Store, Subscription | `<X>Option` |
| `ModuleConfigurer`, `Keeper` (method `keep`), `AttrsProvider` | `ModulePart`, `Keepable`, `Principal` |
| `CommandLineConfigurer` (method `commandLineConfigure`), `ScopeValue`, `ActivityHandler` | `CLIOption`, `Scope`, `Activity` |
| `OpenTelemetryConfigurer` (method `openTelemetryConfigure`), `OTLPBatchValue` | `OTLPOption`, `OTLPBatch` |

The `New*` constructors the declaring methods call (`NewStoreService`,
`NewEndpointService`, `NewError`…) and the hooks a subsystem package calls
as it is imported (`RegisterConfigFormat`, `EnableServer`, `EnableStudio`, `EnableStores`)
are this package's only: the facade does not export them. `framework/app/facade_sync_external_test.go` fails when a
name is added to one side and not the other — its two tables are the list
above; `facade_external_test.go` proves the positions a declaration records
through the facade (`callerFrame` skips the facade's frames:
`facadePackage` in `node.go`). A facade signature never names a generic alias
of the facade — `Bind` takes `*iapp.PortService[Req, Resp]`, not
`*Port[Req, Resp]` —: an instance of a generic alias read from export data
races in go/types (golang/go#79035) for every tool that type-checks a
product's packages at once; `facade_generics_external_test.go` pins it. A
facade doc link to a member is `[Type].Member` (ADR 0138).

The file map, every invariant in full, the error codes and the test suite
are `NOTES.md`, same section titles (ADR 0253): read what a change touches.

## Files

What each file holds, file by file: `NOTES.md` §Files. A new file adds
its row there in the same change.

## Rules specific to this package

- A diagnostic is a sentence of the catalogues, never a format string:
  `s.problem(at, node, "key", "name", value…)`, `a.problem(node, say(…))`,
  `diagnosticOf(severity, node, source, phrase)`. A new one adds its key to
  `locales/en.json` AND `studioengine/locales/fr.json` (same placeholders);
  `TestKitSpeaksEveryLanguage` refuses a key the code says and the catalogues
  lack, or the reverse. English stays what the terminal, the logs, an error
  and the tests read, whatever a program imports; French is the Studio's
  (`framework/app/studio`, ADR 0179 Q6).
  Wire errors (`describe`, `Invalid`…) are the product's API and stay English.

- A new constructor that records a position is `//go:noinline` and gets a line
  in `TestSourcesPointAtTheDeclarations`.
- Everything a span, a response or the event stream carries about an error
  comes from `describe(err)`; caller text quoted in a message goes through
  `clip`. One exception, documented: a mail's status in the mailbox carries
  `publicText(err)` — a kit error's message or the SDK's `Public` text, both
  wire-safe by contract — so a developer reads "The mail server could not be
  reached" rather than "internal error". Never `err.Error()`.
- A goroutine or timer started by a run is joined by `Stop` (`a.wg`, the
  `stopping` flag); `TestGraphNoticeDoesNotOutliveItsRun` shows how to prove it.
  Loops, the outbox and consumers are joined by their component's `stop`,
  bounded by the stop context: a product function that ignores its context is
  reported by a failed stop, not waited for forever.
  A queue's consumer — a subscription's, a watch's, a queued command's — runs
  under the SDK's `lifecycle.Supervisor` (ADR 0112), as `Service.Go` does
  (`consumer.supervise`): its queue's `Consume` — the SDK's, run by queueengine — returns when its storage
  fails, and the consumer comes back after kit's backoff (1 s to 1 min), its
  loop showing the failure (`consumer.failed`) and counting the restart; a
  test makes a queue fail through `App.wrapQueue`, nil in a product's app
  (`consumer_supervised_internal_test.go`). Neither restarts forever: past
  its `GiveUpAfter` — 10 early ends within 10 minutes by default, settings
  of its service — the supervisor gives up and the product is no longer live
  (`supervised:<node>`, `supervision.go`); a declared loop gives up the same
  way on its failed runs.
  A goroutine the framework starts on its own — a listener's accept loop, the
  idle watcher — goes through `goTracked`.

## Invariants of kit v2

One line each; where each is implemented and the test that proves it
is `NOTES.md` §Invariants of kit v2 — read it before bending one, and
add a new invariant to both.

- **Auth spans carry no edge.**
- **A 429 says when to retry; a caller that left is no failure.**
- **Auth runs before decoding.**
- **An in-process call is not authenticated again.**
- **Credentials never leave the mailer.**
- **A unique index never lies.**
- **Loops wait on the app clock.**
- **A workflow never polls.**
- **One entity, one transition at a time.**
- **One run at a time, a burst is one run.**
- **A delivered mail is not sent twice by this process.**
- **Labels are restored in `end()`.**
- **Dev tools exist in dev only, and they only read.**
- **Every Studio route** goes through `api(…)` in `studioengine`'s `mount` — behind its `guard` (ADR 0180 step 1).
- **The runtime names carry no trace of the tool** (ADR 0180 §3, step 4).
- **Nothing a product links names the tool, held by a guard** (ADR 0180 §6, step 6).
- **The names at rest (ADR 0180 step 5a).** What a former release kept under kit's name is moved, once, never copied and never replacing (`renamed_rest.go`).
- **The secrets and data-key's label (ADR 0180 step 5b).** The framework's own secrets are kept as `app-…`; `kit-…` is read, never written, and both prefixes are reserved to the framework.
- **The journal closed and continued (ADR 0180 step 5c-1).** The journal's chain is never rewritten: a former release's journal is continued, preceded by a `model.JournalContinue` entry.
- **The references at rest, sealed again (ADR 0180 step 5c-2).** Every reference is made under `presentLabels`.
- **The core reads none of the tool's variables.**
- **What the Studio shows is redacted.**
- **A port calls what the start bound.**
- **A port's policies compose in one order, and replay only the dependency's failures.**
- **A consumer's stop is a drain, and no failure.**
- **A replacement is a test's, and replaces code, not mechanics.**
- **`With` copies.**
- **Personal data never shows** (ADR 0006).
- **What kit keeps is sealed at rest** (ADR 0006 §4).
- **A retention never polls.**
- **A hold fails closed.**
- **The journal is append-only and chained.**
- **The journal seals under a month's key, and no erasure reaches it.**
- **A history is written before its record** (ADR 0007), inside the engine's `Update`: a crash between the two leaves the current value at the head of the history, which kit reads past, never a former value lost; a refused record puts the history back.
- **A version is written in its record's write** (ADR 0007 §3): the SDK's document store keeps it in the same overlay entry or the same transaction, stamped with the caller's context — its user, the command its pipeline marked —, never the transaction's.
- **What the product deletes from a store with revisions goes to its trash, and what privacy deletes never does** (ADR 0178 C4).
- **A password is only ever a hash, and its former hashes stay in kit.**
- **A module qualifies by construction.**
- **A module's services are its Go module's.**
- **One app runs a module at a time**, as its services: `Module.Prefix` is set once the services are mounted (`mountModules`) and taken back only by the app that set it.
- **An exposure says who may run it** (issue #34).
- **A command is checked alike wherever it comes from.**
- **A product without commands pays nothing.**
- **A write tells its watches where kit confirms it** (ADR 0008).
- **A transaction writes one database** (ADR 0004).
- **The writer turn is a write's outermost lock.**
- **A loop's transition is never decided on a rolled back write.**
- **A watch that hears its own stores never hears itself.**
- **Marks cost nothing without a watch.**

## Hooks for the runtime half

| Hook | Where | Contract |
|---|---|---|
| `func (l *Loop) Nudge()` | `loop.go` | a product's own wake — never the Studio's (D13); reason `manual`; skips a failure backoff |
| `mailbox` (`mails(limit)`, `mail(id)`) | `mailer.go` | implemented by `*Mailer`; newest first; SMTP keeps summaries only |
| `a.frameworkLoop(...)` | `loop.go` | registers a loop entry with provenance and library, filling only what `a.loop` left empty |

## Error codes (`0.4.2.*`)

The serials by area are the table in `NOTES.md` §Error codes. A new code
takes a serial no row holds and adds its row there in the same change.

## Test

`go test -race ./app/`. The product under test, the start helpers and the
timing rules a test that drives a clock must follow: `NOTES.md` §Test.

## Learned conventions

- **A generic engine is compiled once per record shape.** Every record type
  a store was declared over instantiated the whole generic chain beneath it —
  `StoreService`, the document and SQL engines, the SDK's `docstore.Store`
  and `SQLStore`, `historied`, `sealedEngine` —, and the test binary declares
  dozens: the kit_test compile reached 5 GB when a sealing store's documents
  were `sealedDoc[T]`, and the smallest product linked 0.9 MB of it (ADR
  0179). Since ADR 0179's step 2 the engines are one, over documents
  (`plug.DocumentValue`; since step 3 the SDK's document store is
  instantiated once, at storeengine's `docRecord`), and so are the retention, the trash
  and the privacy declaration; what stays generic is the typed facade —
  `StoreService[T]`'s exported methods and declarations, a few lines each —
  and the wrappers made once at a declaration (`entityOf`,
  `privacyFuncsOf`, an index's key function). A method of the store that
  names no `T` belongs on `storeCore`; an entity crosses it as an `any`
  holding a `T`, never a `*T` — `addressable` is the settable copy
  reflection changes.

- **An in-package test says so in its name**: a test file that reaches the
  package's unexported names is `package app` and ends in `_internal_test.go`;
  every other one is `package app_test`, which sees the package as a product
  does (41 and 65 files). In an external test the package is `app`, so a
  local variable never takes that name: the running app is `a` (or `prod`
  where `a` is taken), as ADR 0180 step 3 renamed every one.
