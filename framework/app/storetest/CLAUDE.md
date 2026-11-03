<!-- updated: 2026-11-02T00:00:00Z -->
# framework/app/storetest — the conformance suite of kit's stores

What a store does on every backend (the platform's ADR 0004): run by
`framework/internal/app`'s tests on memory, files and the fake database
(`storetest_test.go`), and by each engine module's tests on its engine
(`connectors/{sqlite,postgres,mysql}`). It keeps the test double honest: a
product tested in memory sees what its database does.

## Contents

| File | Holds |
|---|---|
| `storetest.go` | `Run` and `BackendConfig` — where the stores run, the options that place them —, `CodeRolledBack` (`0.4.4.1`, the code of `errRolled`, the error a case returns to roll its transaction back), and the cases: write modes, one insert of a key winning, atomic updates, a unique index naming itself, key order, keys as bytes, documents as written, transactions and savepoints, the options of a transaction (`transaction-options`, #1140: a SERIALIZABLE and a REPEATABLE READ transaction commit, a read-only one refuses a store's write with `CodeTransactionReadOnly`, a nested Transact given an option is refused with `CodeTransactionNested`), a publish and a write's hooks held until the commit, revisions (kept, pruned, stamped, diffed, restored, rolled back), and the trash of a store with revisions (ADR 0178 C4: a deletion leaves every read and frees its unique keys, `Untrash` brings it back with its versions, kit purges it 30 days after on the app's clock and not a second before, a store without revisions keeps none) |
| `subsystems.go` | #998's cases of what ADR 0178 placed beside the stores, on one run: `records-keys` (a watch lists, through `Records.Keys`, the 300 memos of its store — more than the 256 keys a SQL store reads per chunk — and the one that asked, in key order, under `<name>-timeout`) and `outbox` (a mail sent in a transaction that rolls back is never delivered, one that commits is, through dev's capture transport; on a database the graph names the outbox `sql`); `memo` (a field `app:"moderated"` a watch hears) and the watch's `listing` |
| `durable.go` | #998's cases that stop the product and start it again on what it left — `build` on the case's placement, the backend's `Options` built once per case and given to every app of it (`again`), the same clock —, skipped on memory (`durable`) or without a database (`onDatabase`, the graph's backend): `watch-catch-up` (memos no watch heard are queued again by `watch replay` and heard at the next start; after a clean stop nothing is heard again), `workflow-journal` (a timer armed before a stop fires after the start, at its instant on the app's clock and not a second before), `data-copy` (a product on files copies its memos into the database with `data copy --to database`, reads them back there, and a second copy is refused) and `otlp-tables` (otlp-database: a batch a collector that is down refused waits in the export's table, the runtime state saying so, and the next start sends it) |
| `README.md` | written by `tools/genindex` from `docs/api` (`make docs-readme`, ADR 0167) |

## Rules

- A case declares a service of its own, named after the case, so the cases of one run can share one database: their tables never meet.
- A case uses the facade (`framework/app`) only: it sees the stores as a product does.
- The suite imports the registration packages of what its cases declare — `framework/app/queue` and `framework/app/workflow`, which bring `framework/app/store` —, as a product does since ADR 0179's step 10, when the facade stopped importing them: a caller needs no import of its own for the suite's sake. A case that declares another subsystem adds its import here.
- A case that waits on time says so (`clocked` in `cases`): it runs on a manual clock at `epoch` (`suite.clock`), never on the wall clock; the others run on the wall clock, as before.
- A new case is added to `cases`, and runs on every backend of every caller.
- A case that needs nodes the others do not — a queued command, a topic for later or once — declares them in `declarations`, keyed by its name, so the other cases' services stay as they were (#998). `queued-command` (a dispatch inside a transaction exists if and only if it commits, `DispatchAt` held to its instant on the manual clock, a key that waits not queued again) and `topic-later` (`PublishAt` held to its instant, `Once` delivering a key once within its window) declare `declareQueues`'s: on a database each queue is a table of the SDK's SQL broker, leased with `SKIP LOCKED`, so they run the broker's dialect on sqlite, postgres and mysql. The fake database's queue (`framework/internal/app/fakequeue_internal_test.go`) answers PublishOnce's reservations for them.
- #998's six other cases — `records-keys`, `outbox` (`subsystems.go`), `watch-catch-up`, `workflow-journal`, `data-copy`, `otlp-tables` (`durable.go`) — are in the suite, so they run on sqlite, postgres and mysql through `connectors/*`'s `TestStoresConform` and the existing `server(t)` lanes. The suite imports `framework/app/mail` and `framework/app/otlp` for them. What they leave to the engines' own tests: the running mark of a stop that was not clean (the suite cannot reach the data directory a backend's `Options` chose), the outbox's sealed rows and a dead letter's reason (the facade reads neither), and `otlp dead-letters`/`replay`. `queued-command` waits for a dispatch due now after the one for later, so the one for later is acknowledged before the clock jumps two hours — its lease lapsing under the jump made it handled twice under load.
- A restart case gives the same option values to several apps of one case, one after the other: an `app.Database` declaration and its engine are reused, so a backend's `Options` must return a database that keeps what a stopped app wrote (a fake database instance, a file, a fresh server database set once in the environment).
- An error the suite returns itself is typed (rule 2: no `errors.New`, which `make guard` fails on) in this package's own range, `0.4.4.*` — layer 4, PP 4, recorded in `codeRangeOwners`; the package is in `//:audit_sources`.

## Verify

```sh
cd framework && GOWORK=off go test -race -run TestStoresConform ./internal/app/ && (cd connectors/sqlite && GOWORK=off go test -race -run TestStoresConform ./...)
```
