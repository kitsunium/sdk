<!-- updated: 2026-10-17T12:00:00Z -->
# framework/internal/kit/plug — where kit's opt-in subsystems plug in

The hooks kit reads and a subsystem package sets as it is imported: the HTTP
engine, the frontends' file server and the process sampler (`serverkit`, set
by `framework/kit/server`), the Studio's event stream and profiler
(`studiokit`, set by `framework/kit/studio`), the stores and the databases
(`storekit`, set by `framework/kit/store`). kit imports none of the SDK
packages behind them — `pkg/v1/net/server`, `net/static`, `net/sse`,
`profiling`, `proc/process` (and through it `runtime/metrics` and the spawn
engine, ADR 0172), `data/docstore` and `data/sql` (ADR 0179) —, so a product
that does not import a subsystem neither links nor initialises them;
`framework/kit`'s initgraph tests hold the graph to it.

It is also the contract between kit's core and the subsystems ADR 0179 takes
out of it, one file per subsystem (Q7), as `database/sql/driver` is between
`database/sql` and a driver. The store's (`store.go`, `store_interface.go`,
`store_refusal.go`) is written since step 1, used by the core's engines since
step 2, and implemented by `storekit` since step 3: the core opens every
engine with a `StoreConfig` — a version's hold asked with the write's context
(`Held`), the write's stamp read from its context (`Stamp`), the core's
transaction an engine takes part in (`Transaction`), a call's context on a
database (`Call`) —, opens kit's scope on a database with a `DatabaseConfig`
and runs its migrations through it, and matches an engine's refusals by kind
(`Refusal`, `Refused`): the SDK's sentinels stay in `storekit`.

## Contents

| File | Holds |
|---|---|
| `plug.go` | the hooks (`NewHTTPServer`, `NewStaticFiles`, `SampleProcess`, `OpenEventStream`, `StudioProfiler`), what they take and return (`HTTPConfig`, `HTTPState`, `HTTPServer`, `EventStream`, `ProfilerConfig`, `SourceFunc`), the pprof labels kit and the profiler share (`LabelNode`, `LabelLoop`) |
| `store.go` | the store subsystem's values (ADR 0179 §2, §3): the hook `Stores` (`StoreDriverConfig`: `Open` a store's engine, `Database` kit's scope on a database's pool, `TableMigration` the migration of one of kit's tables); `DocumentValue` (the JSON as it rests and the entity decoded, `any`), `EntryValue`, `WriteMode` (`WriteUpsert`, `WriteInsert`, `WriteReplace`), `IndexSpec`, `StoreConfig`, `StampValue`, `DatabaseConfig`, `TableSpec`, `MigrationValue` and its `Step`, `VersionValue`, `VersionWeightValue`, `TombstoneValue` — named with the role suffixes ktn-linter asks for, as the SDK's core docstore names its own |
| `store_interface.go` | the store subsystem's interfaces: `Database` (the transactions, savepoints and held functions of `kit.Transact` on one database) and its siblings `Checker`, `MigratorProvider` (`Migrator`, `Executor`), `HandleProvider` (the SDK's handles the queues' SQL broker and the mail spool still in the core take, until ADR 0179's steps 4 and 6); `Transaction`, kit's transaction as an engine takes part in it, which the core implements; the engine `StoreEngine` over documents and its siblings `Folder`, `Keyser`, `EntrySource`, `Reindexer`, `VersionKeeper`, `VersionWeighter`, `Trasher`, `DocumentSource` (`Documents`, `DocumentSink`: what a data copy reads and writes) |
| `store_refusal.go` | an engine's refusals in kit's terms: the kinds (`Refusal`: `RefusalNotFound` … `RefusalOutOfOrder`), `RefusalError` (the kind and the SDK's error inside), `Refuse`, `Refused` (the kind anywhere in an error's tree, as `errors.Is` reads one) |
| `README.md` | written by `tools/genindex` from `docs/api` (`make docs-readme`, ADR 0167) |

## Rules

- It imports the standard library and `framework/model` only: kit and every subsystem import it. `framework/kit`'s `TestTheCoreImportsOnlyTheSubsystemsStillInIt` holds its direct imports to it.
- A hook is set once, by its subsystem's `Enable`; kit reads it where the subsystem is needed and says so when it is nil.
- A type here names no entity type and no SDK type: what a product declares on its own type is wrapped into a function of a `DocumentValue` at the declaration; an SDK value crosses as plug's own (`VersionValue`, `MigrationValue` on `Executor`, whose methods are `database/sql`'s), or — `HandleProvider` alone, until steps 4 and 6 — behind a pointer the SDK's own packages give the core.
- An engine's refusal reaches the core as a `RefusalError`; a new SDK sentinel the core must tell apart is a new kind, never an import of the SDK by the core.

## Verify

```sh
cd framework && GOWORK=off go vet ./internal/kit/plug/ && GOWORK=off go test ./kit/ -run 'TheCoreImportsOnly|TheSmallestProduct|ImportsTheStores'
```
