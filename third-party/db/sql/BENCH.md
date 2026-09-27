<!-- generated from third-party/db/sql/docstore_bench_test.go — run `GOWORK=off go test -tags integration -run '^$' -bench BenchmarkSQLStore -benchmem -benchtime=2s ./third-party/db/sql/` to refresh (Docker for PostgreSQL and MySQL) -->
# Benchmarks — the document store over SQL, on real engines

These numbers answer the question ADR 0139's cost table leaves open:

> **What does a call cost, once the statements it sends meet an engine?**

The table counts statements; these rows time them. Each call runs once per
iteration over a store of 1 000 documents with a unique and a multi-valued
index (`Put/no-indexes`: a store with none), through the public facade and a
real driver.

## Reproducibility envelope

> **Numbers vary across machines, and these were taken on a loaded one.**
> PostgreSQL and MySQL ran in Docker Desktop's Linux VM, so every round trip
> crosses the VM's network: that hop, not the engine and not the SDK, is most
> of every server row. The *shape* is what travels — a call costs its round
> trips — not the microseconds.

| Dimension | Value |
|---|---|
| CPU cores          | 10 (Apple M1 Pro) |
| RAM                | 16 GiB |
| OS / kernel        | macOS 26.6.2 (Darwin 25.6.0) |
| Architecture       | arm64 |
| Go toolchain       | go1.27.1 darwin/arm64 |
| Engines            | SQLite through `modernc.org/sqlite` v1.59.0 (a file, WAL, `busy_timeout(10000)`); `postgres:17` through pgx v5.11.0; `mysql:8.4` through go-sql-driver/mysql v1.10.0 — the servers in Docker Desktop 29.7.2 |
| Git branch         | `feat/docstore-on-sql` |
| Git commit         | `12539d5` (pre-commit) |
| Generated (UTC)    | 2026-09-27 |
| Load average       | 12.6 / 21.2 / 28.1 — the machine was building other branches |
| Bench wall-clock   | `-benchtime=2s`, single run, 57 s total |

## Results

```
BenchmarkSQLStore/sqlite/Get-10                     227582       10569 ns/op      1380 B/op       37 allocs/op
BenchmarkSQLStore/sqlite/Put-10                      15675      153015 ns/op      6008 B/op      139 allocs/op
BenchmarkSQLStore/sqlite/Put/joined-10               10000      222126 ns/op      6723 B/op      164 allocs/op
BenchmarkSQLStore/sqlite/Update-10                   14697      164776 ns/op      6935 B/op      160 allocs/op
BenchmarkSQLStore/sqlite/Put/no-indexes-10           33040       75379 ns/op      1656 B/op       41 allocs/op
BenchmarkSQLStore/postgres/Get-10                     7425      308379 ns/op      1681 B/op       35 allocs/op
BenchmarkSQLStore/postgres/Put-10                     1263     1806388 ns/op      7159 B/op      130 allocs/op
BenchmarkSQLStore/postgres/Put/joined-10              1250     2640417 ns/op      7818 B/op      150 allocs/op
BenchmarkSQLStore/postgres/Update-10                   952     2343974 ns/op      8249 B/op      150 allocs/op
BenchmarkSQLStore/postgres/Put/no-indexes-10          8991      354403 ns/op      1846 B/op       37 allocs/op
BenchmarkSQLStore/mysql/Get-10                        4020      596381 ns/op      1273 B/op       37 allocs/op
BenchmarkSQLStore/mysql/Put-10                         780     3133278 ns/op      5789 B/op      120 allocs/op
BenchmarkSQLStore/mysql/Put/joined-10                  682     3533352 ns/op      6239 B/op      137 allocs/op
BenchmarkSQLStore/mysql/Update-10                      660     3908395 ns/op      6968 B/op      149 allocs/op
BenchmarkSQLStore/mysql/Put/no-indexes-10             2314      918008 ns/op      1143 B/op       27 allocs/op
```

## What the numbers say

### On a server, a call costs its round trips

`Get` is one statement, so on PostgreSQL its 0.31 ms is one round trip across
the VM. The other rows are that unit times the statements ADR 0139's table
counts for them:

| call | statements | PostgreSQL | in round trips |
|---|---|---|---|
| `Get` | 1 | 0.31 ms | 1 |
| `Put`, no indexes | 1 | 0.35 ms | 1.1 |
| `Put`, indexed | 6 (BEGIN, upsert, check, deletion, rows, COMMIT) | 1.81 ms | 5.9 |
| `Update` | 7 | 2.34 ms | 7.6 |
| `Put` joined, one transaction per `Put` | 8 (the caller's BEGIN and COMMIT, the write's SAVEPOINT and RELEASE, its 4 statements) | 2.64 ms | 8.6 |

MySQL reads the same way at about 0.6 ms a round trip. So what decides the
cost of a write is how many statements it sends, and the design's levers are
the ones it pulls: a store without indexes writes in ONE statement and no
transaction; the unique check is skipped for a document with no unique key; a
document's index rows travel in one statement.

### Joining the caller's transaction costs two statements

`Put/joined` wraps each `Put` in a transaction of its own, so each iteration
pays the caller's BEGIN and COMMIT on top of the write's SAVEPOINT and
RELEASE: the difference with `Put` — 0.83 ms on PostgreSQL, 0.40 ms on MySQL —
is those two extra statements. A caller who writes several documents in one
transaction pays its BEGIN and COMMIT once, and each write its SAVEPOINT and
RELEASE: the price of a write that can fail alone and leave the caller's
transaction usable (ADR 0139 §D5).

### On SQLite, a commit costs a flush

SQLite has no network: `Get` is 11 µs. A write costs its commit — a WAL
append and its flush — so `Put` without indexes (one statement, autocommit) is
75 µs and an indexed `Put` (one transaction, four statements) 153 µs. There
the statements matter less than the one commit they share.

### What these rows do not isolate

The allocations — 37 for a `Get`, 130 to 160 for a write — are mostly
database/sql's and the driver's, and the rows do not separate the SDK's own
share of a call from theirs. Against a round trip of a few hundred
microseconds, it is not the number that decides anything.
