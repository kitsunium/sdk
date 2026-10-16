<!-- updated: 2026-10-17T00:00:00Z -->
# framework/internal/kit/plug — where kit's opt-in subsystems plug in

The hooks kit reads and a subsystem package sets as it is imported: the HTTP
engine, the frontends' file server and the process sampler (`serverkit`, set
by `framework/kit/server`), the Studio's event stream and profiler
(`studiokit`, set by `framework/kit/studio`). kit imports none of the SDK
packages behind them — `pkg/v1/net/server`, `net/static`, `net/sse`,
`profiling`, `proc/process` (and through it `runtime/metrics` and the spawn
engine, ADR 0172) —, so a product that does not import a subsystem neither
links nor initialises them; `framework/kit`'s `TestTheFacadeImportsNoOptInSubsystem`
holds the graph to it.

It is also the contract between kit's core and the subsystems ADR 0179 takes
out of it, one file per subsystem (Q7), as `database/sql/driver` is between
`database/sql` and a driver. Step 1 wrote the store's (`store.go`, `store_interface.go`); nothing
sets `Stores` yet and the core does not read it: step 2 makes the core's
store engine non-generic on these types, step 3 moves it to `storekit`,
behind `framework/kit/store`, which sets the hook.

## Contents

| File | Holds |
|---|---|
| `plug.go` | the hooks (`NewHTTPServer`, `NewStaticFiles`, `SampleProcess`, `OpenEventStream`, `StudioProfiler`), what they take and return (`HTTPConfig`, `HTTPState`, `HTTPServer`, `EventStream`, `ProfilerConfig`, `SourceFunc`), the pprof labels kit and the profiler share (`LabelNode`, `LabelLoop`) |
| `store.go` | the store subsystem's values (ADR 0179 §2, §3): the hook `Stores` (`StoreDriverConfig`: `Open` a store's engine, `Database` kit's scope on a database's pool); `DocumentValue` (the JSON as it rests and the entity decoded, `any`), `WriteMode` (`WriteUpsert`, `WriteInsert`, `WriteReplace`), `IndexSpec`, `StoreConfig`, `DatabaseConfig`, `VersionValue`, `VersionWeightValue`, `TombstoneValue` — named with the role suffixes ktn-linter asks for, as the SDK's core docstore names its own (`VersionValue`, `TombstoneValue`) |
| `store_interface.go` | the store subsystem's interfaces: the engine `StoreEngine` over documents and its siblings `Folder`, `Keyser`, `VersionKeeper`, `VersionWeighter`, `Trasher` — the non-generic forms of the core's `storeEngine[T]`, `folder`, `keyser`, `versionKeeper`, `versionWeighter`, `trasher` —; `Database`, the transactions, savepoints and held functions (`Defer`, `OnAbort`) of `kit.Transact` on one database |
| `README.md` | written by `tools/genindex` from `docs/api` (`make docs-readme`, ADR 0167) |

## Rules

- It imports the standard library and `framework/model` only: kit and every subsystem import it. `framework/kit`'s `TestTheCoreImportsOnlyTheSubsystemsStillInIt` holds its direct imports to it.
- A hook is set once, by its subsystem's `Enable`; kit reads it where the subsystem is needed and says so when it is nil.
- A type here names no entity type and no SDK type: what a product declares on its own type is wrapped into a function of a `Document` at the declaration.

## Verify

```sh
cd framework && GOWORK=off go vet ./internal/kit/plug/ && GOWORK=off go list -deps ./internal/kit/ | grep -E 'pkg/v1/(net/server|net/static|net/sse|observe/profiling|proc/process)$|^runtime/metrics$' ; test $? -eq 1
```
