<!-- updated: 2026-10-17T22:00:00Z -->
# framework/internal/app/storeengine — the stores' engines, kit's scope on a database

What `framework/app/store` plugs into kit (`plug.Stores`, ADR 0179 §1, step
3): the engines kit's stores run on — the SDK's document store in memory, in
the data directory and over SQL —, kit's scope on a database — the SDK's
transaction manager, its check, its migrator — and the migrations of kit's
tables. kit's core imports neither the SDK's `docstore` nor its `sql`: it
opens every engine and every database through `plug`, and a program that does
not import `framework/app/store` links none of this package (`framework/app`'s
`TestTheSmallestProductLinksOnlyTheSubsystemsStillInTheCore`) and is refused
at the start for a store or a database it declares (`CodeSubsystemMissing`).

## Contents

| File | Holds |
|---|---|
| `storeengine.go` | `Enable`, which sets `plug.Stores` (`open`, `openDatabase`, `tableMigration`); `open` — on the config's `Database`, the SQL engine, else the file engine |
| `document.go` | `docRecord`, the one record type the SDK's document store is instantiated at (the JSON as it rests, the entity decoded once beside it, `openedDoc`); `documents`, a store's `plug.StoreConfig` as its engines read it — the key (`KeyAtRest` first), the decoding, the indexes as the SDK takes them (`specs`), the stamp of a version, `docLoad` (the first record a load could not decode, which refuses the opening); the conversions between the SDK's values and plug's (versions, tombstones, entries, weight); `clockFunc`, the app's clock as the SDK reads it |
| `file.go` | `fileEngine`, the SDK's document store in memory and in the data directory (`openFile`): every `plug.StoreEngine` call and its siblings (`Folder`, `Keyser`, `EntrySource`, `VersionKeeper`, `VersionWeighter`, `Trasher`, `DocumentSource`, `Documents`, `DocumentSink`); in a transaction of the data's, what a write replaced is kept for a rollback (`before`, `uncreate`, `forget`) and its hooks wait for the commit (`after`) — through `plug.Transaction`, kit's |
| `sql.go` | `sqlEngine`, the SDK's document store over SQL (`openSQL`): each call on the context `plug.StoreConfig.Call` gives — kit's transaction on the database, else the pool, within `<name>-timeout` —, a rolled-back write told again (`retold`, `plug.Transaction.Retell`), `Reindex`, keys listed `sqlKeysChunk` at a time, the data copy's reads and writes |
| `database.go` | `database`, kit's scope on one database (`openDatabase`): the SDK's `Transactor`, `Checker` and `Migrator` over the pool kit opened, `plug.Database` and its siblings `Checker`, `MigratorProvider`; `SQLOf`, the SDK's transaction manager and dialect of a database this package opened, which `queueengine`'s SQL broker runs on (step 4) and `mailengine`'s spool defers on (step 6); `migrator`; the conversions between plug's migrations and the SDK's; `tableMigration` — the document store's table and its versions' table (a consumer's queue's is `queueengine`'s since step 4) |
| `refusal.go` | `said`: every error the package returns, its SDK sentinel told by the kind kit words it by (`plug.Refusal`), the SDK's error kept inside — a key too long with the columns' bound in its `max` field |
| `storeengine_compliance.go` | the compile-time assertions of every plug interface the engines and the scope implement |
| `README.md` | written by `tools/genindex` from `docs/api` (`make docs-readme`, ADR 0167) |

## Rules

- It imports `plug` and the SDK — never the SDK's queue, which is `queueengine`'s —, never kit's core: the core imports no `*kit` package (ADR 0179 §3), and kit's own suites plug it in through `framework/app/store` (`framework/internal/app/subsystems_test.go`).
- Nothing it returns reaches the core as an SDK sentinel the core would match: `said` wraps it in a `plug.RefusalError`. A new sentinel the core must tell is a new `plug.Refusal` kind and a row of `sentinels`.
- The bytes at rest are the core's: an engine writes the JSON `plug.DocumentValue` carries as it is, and the SQL tables keep their names and shapes (ADR 0179 §Consequences).

## Verify

```sh
cd framework && GOWORK=off go vet ./internal/app/storeengine/ && GOWORK=off go test -race ./internal/app/ ./app/ -run 'Store|Database|Transact|DataCopy|Revision|Trash|Initgraph|TheCore|TheSmallest|ImportsTheStores'
```
