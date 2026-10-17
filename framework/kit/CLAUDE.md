<!-- updated: 2026-10-17T22:00:00Z -->
# framework/kit — the facade a product imports

The framework in which every product is its own diagram, as a product sees
it. Every name here is a type alias, a typed constant or variable, or a
forwarder over `framework/internal/kit`, which holds the implementation and
its `CLAUDE.md` (the files, the rules, the invariants, the error codes).
Nothing is implemented here: a change of behaviour is made there.

## Contents

| File | Holds |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) — that `go doc` prints: a product's building blocks, the diagram, the app's databases, modules, and what `Main` understands |
| one file per file of the implementation | its exported names: aliases (`type Store[T any] = kit.StoreService[T]`), constants, variables, and forwarders (`//:` comment; `//go:noinline` where the implementation is — a position is recorded by the caller of the facade, which `callerFrame` reaches past this package) |
| `facade_external_test.go` | the declarations' positions, taken through the facade, point at the product's lines; `kit.Only` through the facade — a mount for another environment left out, its refusal at the product's line |
| `initgraph_external_test.go` | `TestTheFacadeImportsNoOptInSubsystem`: `go list -deps` of this package holds none of the packages an opt-in subsystem brings — the server engine, static files, the process sampler (`proc/process`, its spawn engine, `runtime/metrics`), the Studio's sse and profiling (ADR 0172). It skips without a go toolchain and under Bazel (`TEST_SRCDIR`), whose sandbox holds no module even where the runner put `go` on PATH; its gate is CI's `test-386` job, which runs `go test` over every module with the toolchain on PATH (rule 12). **ADR 0179** (since step 1), under the same skips and gate: `TestTheCoreImportsOnlyTheSubsystemsStillInIt` refuses `pkg/v1/data/docstore`, `pkg/v1/data/sql`, `pkg/v1/data/queue`, `pkg/v1/app/statemachine`, `pkg/v1/app/mail/spool`, `pkg/v1/app/scheduler` and `pkg/v1/app/i18n` (`leaving`, each with its registration package and its step) as a direct import of `framework/internal/kit` but those the allow-list `stillInCore` names, and in its graph but those it names or reaches only through one it names (`throughStill`, which reads the SDK package and its core and service halves: the SDK's `sql` and `queue` under the mail spool, until step 6) — `database/sql` stays, `kit.Engine` returning `*sql.DB` (Q5) —, held tight both ways (a package listed that the core no longer imports fails too); refuses the core importing a subsystem's implementation (`kitEngines`: `framework/internal/kit/storekit` for `framework/kit/store`, `framework/internal/kit/queuekit` for `framework/kit/queue`) and the facade not importing its registration package before step 10; refuses `framework/kit` bringing one without its registration package; and holds `plug`'s direct imports to the standard library and `framework/model`. `TestTheSmallestProductLinksOnlyTheSubsystemsStillInTheCore` builds `testdata/coreprobe` — cliprobe on `framework/internal/kit` alone, since the facade imports every registration package until step 10 — and refuses a text symbol (`go tool nm`) of those packages' core and service halves, or of a left subsystem's implementation, that the allow-list does not name nor reaches, logging those it does — and of a package reached only through one it names, anything beyond that package's inits (`beyondInits`: in coreprobe the SDK's `sql` and `queue` link their `init` alone, the mail spool importing them) —, and any of `kitStoreCode` — one function of each part of kit's own store code, which stays in the core and is reached only through `framework/kit/store` (`ikit.EnableStores`, step 3b): the privacy service and its journal, sealing, the engines over documents and their history, the databases, their migrations and checks, the retention, the trash, the commands on the stores; `TestAProductThatImportsTheStoresLinksThem`, the other half of §7, builds `testdata/storeprobe` (coreprobe with `framework/kit/store`) and requires `storekit`'s, `docstore`'s and `sql`'s symbols linked, every one of `kitStoreCode`, and none of `queuekit`'s nor of the SDK's queue beyond its inits (the stores do not bring the queues, Q4); `TestAProductThatImportsTheQueuesLinksThem` builds `testdata/queueprobe` (coreprobe with `framework/kit/queue`) and requires `queuekit`'s and the SDK's queue's symbols linked — its code, not its init alone —, and the store subsystem's, which the queue package imports. Step 2 (2026-10-17) took kit's generic store engines out — one non-generic engine over documents —, step 3 (2026-10-17) `docstore` and `sql`, step 3b (2026-10-17) kit's own store code from what `App.Main` reaches, step 4 (2026-10-17) `queue`; step 5 takes `statemachine`, step 6 `mail/spool`, step 7 `scheduler`, step 8 `i18n`, each in the change that moves it, and step 10 deletes the list |
| `fresh_process_bench_test.go`, `testdata/cliprobe`, `BENCH.md` | a fresh process of the smallest product (one default fail-safe command), built and exec'd: what a status line pays per render when nothing is warm |
| `server/`, `studio/`, `store/`, `queue/`, `config/yaml/`, `config/toml/` | the subsystems, each enabled by a blank import: HTTP in the server profile, the Studio's dev API, the stores and the databases (ADR 0179 step 3), the queues — topics, subscriptions, watches, queued commands, a mailer's outbox on a database (step 4; it imports `store/`, Q4) —, YAML and TOML configuration files. A product links only those it imports; the behaviour is in `framework/internal/kit` (see each `CLAUDE.md`) |
| `subsystems.go` | the facade's own blank import of the registration packages ADR 0179 takes out of the core — `framework/kit/store` since step 3, `framework/kit/queue` since step 4 —, until its step 10 (Q8): a product that imports `framework/kit` keeps every subsystem, and no product changes before `kit gen` writes its imports |
| `testdata/coreprobe`, `testdata/storeprobe`, `testdata/queueprobe` | the initgraph test's three programs: cliprobe's service and command on `framework/internal/kit` alone, the same with `framework/kit/store`, and with `framework/kit/queue` (see each `CLAUDE.md`) |
| `kittest/` | a product's tests on disk (ADR 0175): `NewDisk(t)` — a data directory of the test's own, the secrets kit keeps beside it, and the option that runs an app there, which wins over a `kit.InMemory()` given before it —, `Disk.Holding`, the files that keep a word in clear; it skips where the SDK's disk file system refuses the platform (Windows) |
| `storetest/` | the conformance suite of kit's stores (`storetest.Run`, `BackendConfig`), run by kit's tests and by each engine module's on its engine |
| `facade_generics_external_test.go` | no function of the facade names a generic alias of the facade in its signature: `Bind`, `Fallback`, `WakeOn`, `Replace` take `ikit.PortService`, `ikit.Operation`, `ikit.TopicService` — an instance of a generic alias read from export data races in go/types (golang/go#79035) |
| `facade_sync_external_test.go` | every exported name of the implementation is here, under its product-facing name, and nothing else is: the rename table (`StoreService` → `Store`, `AppConfigurer` → `AppOption`…) and the constructors left out (`NewStoreService`…) |

## Rules

- A name added to, renamed in or removed from `framework/internal/kit` is
  added, renamed or removed here in the same commit; the implementation keeps
  the role-suffixed name, the facade the name a product writes (the table is
  in `framework/internal/kit/CLAUDE.md`).
- A forwarder only forwards: no logic, no check, no extra parameter — but
  `Migrations` (`database.go`), which hands the core the SDK's
  `sql.Migration`s as `plug.MigrationValue`s, the same versions, names and
  steps: the core no longer names the SDK's sql (ADR 0179 step 3, Q5).
- A forwarder of a function that records a position is `//go:noinline`.
- A signature names a generic type by its internal name (`ikit.PortService`),
  never by the facade's generic alias; a doc link to a member is
  `[Type].Member`, since every facade type is an alias (ADR 0138).

## Verify

```sh
cd framework && GOWORK=off go vet ./kit/ && GOWORK=off go test ./kit/
bazel test //framework/kit:kit_test && bazel shutdown
```
