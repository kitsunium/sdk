# third-party/db/sql/

## Purpose

The SDK's SQL mechanisms on the three real engines, through real drivers: the
document store over SQL (`pkg/v1/docstore`, ADR 0139) with its versions
(ADR 0143), the queue's SQL broker (`pkg/v1/queue`, ADR 0151), and the
transactor's and migration runner's contracts (`pkg/v1/sql`), SQLite's file
lock among them (ADR 0140). It is a suite, not a package: every file is an
`//go:build integration` test, and there is no production code.

It lives in the ROOT module because that is where drivers may be imported
(ADR 0055 §D2 — a driver is a connector, and connectors live under
`third-party/`). The workspace modules import none, so their default suites
run the same contracts over scripted drivers; this is where the SQL meets an
engine that parses it.

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
| `docstore_integration_test.go` | the write modes; a document read back byte for byte; `a`/`A`, `a`/`a␠`, `é`/`e`; the indexes, hashed; thirty-two writers of one document and of one unique key; the caller's transaction joined, rolled back, and a refused write inside it; `STATEMENT_FAILED` withholding a real driver's text; `Reindex` |
| `docstore_versions_integration_test.go` | a document's versions (ADR 0143): the versions table `SQLVersionsMigration` creates, numbered and stamped versions pruned by the write that makes a newer one, a write that cannot keep its versions writing nothing, sixteen writers of one document leaving consecutive numbers, a hold read in the write's own transaction, the erasure's rewrite, a document stored before versions were kept |
| `queue_integration_test.go` | the queue's SQL broker (ADR 0151): one message through every state — leased, retried on the growing delay, redelivered after a lapsed lease, dead-lettered with its cause, replayed, rejected, deleted — its payload's bytes unchanged; an extended lease and an empty payload; a publication inside the caller's transaction, rolled back and committed; sixteen consumers draining two hundred messages, each delivered once (SKIP LOCKED, or SQLite's write lock); `Consume` rejecting a `DoNotRetry` failure; `QUEUE_BACKEND_FAILED` withholding a real driver's text |
| `migrate_integration_test.go` | two runners applying a migration once on every engine; SQLite's run holding the file's write lock (another connection's `BEGIN IMMEDIATE` is refused busy); a failing SQLite migration keeping the ones before it |
| `docstore_bench_test.go` + `BENCH.md` | what each call costs on each engine: its round trips on a server, its commit's flush on SQLite (rule 9) |

## Run procedure (rule 12)

These tests are excluded from `bazel test //...` and from a plain
`go test ./...` by their build tag — gazelle reads no `integration` file, so
this directory has no BUILD file — and they run here:

```sh
GOWORK=off go test -tags integration -race ./third-party/db/sql/...
# the benchmarks, without -race, to refresh BENCH.md
GOWORK=off go test -tags integration -run '^$' -bench BenchmarkSQLStore -benchmem -benchtime=2s ./third-party/db/sql/
```

They are a named, manual lane, as `third-party/db/writer/mysql`'s are: CI's
runners do not start the containers. Run them on any change to
`internal/service/sql`, `internal/core/sql`, the SQL engine of
`internal/service/docstore`, or the SQL broker of `internal/service/queue`.

## Do NOT

- Add production code here: a driver imported by a package would reach
  whoever imports it.
- Share a table between cases: the servers are shared, the tables are not.
- Leave a container behind: start one through `postgresEngine` /
  `mysqlEngine`, which register it for `TestMain`.
