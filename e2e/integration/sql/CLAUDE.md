# e2e/integration/sql/

## Purpose

The SDK's SQL mechanisms on the three real engines, through real drivers: the
document store over SQL (`pkg/v1/data/docstore`, ADR 0139) with its versions
(ADR 0143), the queue's SQL broker (`pkg/v1/data/queue`, ADR 0151), and the
transactor's and migration runner's contracts (`pkg/v1/data/sql`), SQLite's file
lock among them (ADR 0140). It is a suite, not a package: every file is an
`//go:build integration` test, and there is no production code.

It lives in the auxiliary `e2e` module, outside `go.work`, because a driver is
a connector (ADR 0055 §D2) and testcontainers starts the servers: neither may
reach a module a consumer requires. It was `third-party/db/sql/` in the root
module until ADR 0157 made every vendor a module of its own and moved the
Docker-backed suites here. The workspace modules import no driver, so their
default suites run the same contracts over scripted drivers; this is where the
SQL meets an engine that parses it.

| Engine | Driver | Needs |
|---|---|---|
| SQLite | `modernc.org/sqlite` (no cgo) | nothing — a file in a temporary directory per case |
| PostgreSQL 17 | `github.com/jackc/pgx/v5/stdlib` | Docker: `postgres:17`, started once by testcontainers on a free port |
| MySQL 8.4 | `github.com/go-sql-driver/mysql` | Docker: `mysql:8.4`, the same |

`TestMain` removes every container it started, whatever the verdict. Without
Docker, the PostgreSQL and MySQL cases skip and SQLite's still run.

## Contents

| File | Cases |
|---|---|
| `engines_integration_test.go` | the three engines, started once and shared by the cases; each case its own tables |
| `docstore_integration_test.go` | the write modes; a document read back byte for byte; `a`/`A`, `a`/`a␠`, `é`/`e`; `Keys` over more than two pages, in Go's order, resumed after a deleted key; the indexes, hashed; thirty-two writers of one document and of one unique key; the caller's transaction joined, rolled back, and a refused write inside it; `STATEMENT_FAILED` withholding a real driver's text; `Reindex` |
| `docstore_versions_integration_test.go` | a document's versions (ADR 0143): the versions table `SQLVersionsMigration` creates, numbered and stamped versions pruned by the write that makes a newer one, a write that cannot keep its versions writing nothing, sixteen writers of one document leaving consecutive numbers, a hold read in the write's own transaction, the erasure's rewrite, a document stored before versions were kept |
| `docstore_trash_integration_test.go` | a document's trash (ADR 0143, amended for ADR 0178): the version row numbered 0 each engine parses, writes and reads back, a trashed document's unique key taken and `Restore` refused, the numbering continued by a write under its key, a purge leaving no row, a `Trash` undone by the caller's rollback; and the trash's index (ADR 0143, amended 2026-11-02): `SQLVersionsTrashIndexMigration` over a table that already holds a trash, run twice up and twice down, each engine's own plan for the `Tombstones` statement naming `<table>___tr` after it and not before (`EXPLAIN QUERY PLAN`, `EXPLAIN` with sequential scans off, `EXPLAIN`) |
| `docstore_weight_integration_test.go` | what the versions weigh (ADR 0143, amended for kit's Studio): `VersionWeight`'s one aggregate each engine parses and answers, counting bytes and never characters (two-byte UTF-8 names), equal to a recount through `Versions`, pruned and trashed versions included, and a caller's uncommitted version counted inside its transaction only |
| `docstore_changes_integration_test.go` | a document store's change feed (kitsunium/sdk#831) on the stream's SQL engine over the store's own transactor: a write in a transaction that rolls back leaving no record, two committed writes one record each, numbered 1 and 2, the second carrying its document, read by a consumer group through `Subscribe` |
| `queue_integration_test.go` | the queue's SQL broker (ADR 0151), every statement it sends on a real engine: one message through every state — leased, retried on the growing delay, redelivered after a lapsed lease, dead-lettered with its cause, replayed, rejected, deleted — its payload's bytes unchanged; an extended lease and an empty payload; a released lease giving its delivery back, never dead-lettered (kitsunium/sdk#474); a message published for later leased by no Receive before its instant, and a nack after the delay it names (ADR 0183); sixteen producers racing on one key publishing one message between them, the reservation rolled back with the caller's transaction, and passed reservations swept (`PublishOnce`, ADR 0183); lapsed leases with no attempt left buried by the next read, a batch that does not fill, publication order and a single retry stepping aside; a publication inside the caller's transaction, rolled back and committed, and an idle consumer woken by its commit; sixteen consumers draining two hundred messages, each delivered once (SKIP LOCKED, or SQLite's write lock); `Consume` rejecting a `DoNotRetry` failure; `QUEUE_BACKEND_FAILED` withholding a real driver's text |
| `queue_trail_integration_test.go` | a dead letter's trail (kitsunium/sdk#500): `SQLTrailMigration` run before `SQLMigration`'s table, then its Down and its Up twice over it — the column dropped and added on each engine's catalogue —; with `SQLConfig.Trail`, a rejection and a last nack keeping their trail, a lapsed lease none, a written dead letter's sixteen codes read back, a replayed message dying again with nothing of its first death; a broker without `Trail` over the same table reading every dead letter with its cause and no trail |
| `queue_priority_integration_test.go` | a message's level (kitsunium/sdk#1045): `SQLPriorityMigration` run before `SQLMigration`'s table — nothing to do —, then over a table a broker without `Priority` filled with a queued row and a dead one, down and up twice; with `SQLConfig.Priority`, the old rows read at the normal level, the extreme levels published, a copy at its level, the old dead letter replayed, and the highest level received first, the old rows in their order, the lowest last |
| `stream_integration_test.go` | the stream's SQL engine (ADR 0231, kitsunium/sdk#822), reached at `internal/service/data/stream` until its facade exists (#825): every statement once — keyed and unkeyed batches, 450 records in three INSERTs, binary keys and payloads read back, an empty payload, `Bounds`, `OffsetAt`, `Trim` and `OFFSET_TRIMMED` / `OFFSET_AHEAD`, a cursor moved back; an Append joined to the caller's transaction, rolled back and committed, the wake on the commit; a group's offset committed in the handler's transaction; sixteen appenders, one transaction in five rolled back, followed by a reader that never meets a hole |
| `forward_integration_test.go` | store and forward (ADR 0185) over the SQL broker: three batches kept by one forwarder counted by the next over the same table, a retryable failure retried on the forwarder's ManualClock, a refused batch in the table's dead letters with the collector's verdict |
| `migrate_integration_test.go` | two runners applying a migration once on every engine; SQLite's run holding the file's write lock (another connection's `BEGIN IMMEDIATE` is refused busy); a failing SQLite migration keeping the ones before it |
| `docstore_bench_test.go` + `BENCH.md` | what each call costs on each engine: its round trips on a server, its commit's flush on SQLite (rule 9) |
| `queue_bench_test.go` | the SQL queue broker's `Publish`, `Receive` and `Ack` on each engine (kitsunium/sdk#1048), rows in the same `BENCH.md` — SQLite measured, PostgreSQL and MySQL waiting for a run with Docker |

Run on 2026-11-13 on a Linux host (Debian 13, kernel 6.12 amd64, Docker 29.7.2), `cd e2e && GOWORK=off go test -tags integration -race -run 'TheSQLStreamOnEveryEngine|AnAppendJoinsTheCallers|ACommitInTheHandlersTransaction|ConcurrentAppendersCommitInOrder' ./integration/sql/`: the four stream tests pass on SQLite, PostgreSQL 17 and MySQL 8.4 (13.7 s and 24 s for the container starts), none skipped.

## Run procedure (rule 12)

These tests are excluded from `bazel test //...` twice over — `e2e/` is in
`.bazelignore`, and no `integration` file is read by gazelle — and from a plain
`go test ./...` by their build tag, so the census lanes that loop over `e2e`
(ADR 0137) compile none of it. They run here, from the `e2e` module:

```sh
cd e2e
GOWORK=off go test -tags integration -race ./integration/sql/...
# the benchmarks, without -race, to refresh BENCH.md
GOWORK=off go test -tags integration -run '^$' -bench BenchmarkSQLStore -benchmem -benchtime=2s ./integration/sql/
```

They are a named, manual lane, as the database writers' suites beside them
are (`e2e/integration/CLAUDE.md`): CI's runners do not start the containers.
Run them on any change to `internal/service/data/sql`, `internal/core/data/sql`, the SQL
engine of `internal/service/data/docstore`, the SQL broker of
`internal/service/data/queue`, or the SQL engine of
`internal/service/data/stream`.

## Do NOT

- Add production code here: a driver imported by a package would reach
  whoever imports it.
- Share a table between cases: the servers are shared, the tables are not.
- Leave a container behind: start one through `postgresEngine` /
  `mysqlEngine`, which register it for `TestMain`.
