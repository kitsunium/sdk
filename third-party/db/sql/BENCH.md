<!-- generated from third-party/db/sql/docstore_bench_test.go — run `GOWORK=off go test -tags integration -run '^$' -bench BenchmarkSQLStore -benchmem -benchtime=2s ./third-party/db/sql/` to refresh (Docker for PostgreSQL and MySQL); the pattern matches BenchmarkSQLStoreVersions too -->
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

## Versions (ADR 0143)

`BenchmarkSQLStoreVersions` times a store keeping ten former versions of each
of its 100 documents, every document already holding ten, so each write makes a
version and prunes one. It ran beside a fresh `BenchmarkSQLStore`, whose plain
rows it is read against.

| Dimension | Value |
|---|---|
| Machine and engines | as above |
| Git branch | `feat/docstore-versions` |
| Git commit | `cdcbe78f` (pre-commit: the tree these rows ship with) |
| Generated (UTC) | 2026-09-28, load average 2.1 / 2.3 / 3.6 |
| Command | `GOWORK=off go test -tags integration -run '^$' -bench 'BenchmarkSQLStoreVersions\|BenchmarkSQLStore$' -benchmem -benchtime=2s ./third-party/db/sql/` |

```
BenchmarkSQLStore/sqlite/Get-10                     227197       10496 ns/op      1379 B/op       37 allocs/op
BenchmarkSQLStore/sqlite/Put-10                      16806      143975 ns/op      6022 B/op      139 allocs/op
BenchmarkSQLStore/sqlite/Update-10                   15723      152502 ns/op      6956 B/op      160 allocs/op
BenchmarkSQLStore/postgres/Get-10                     8160      307611 ns/op      1688 B/op       35 allocs/op
BenchmarkSQLStore/postgres/Put-10                     1309     1857246 ns/op      7186 B/op      130 allocs/op
BenchmarkSQLStore/postgres/Update-10                  1072     2110047 ns/op      8243 B/op      150 allocs/op
BenchmarkSQLStore/mysql/Get-10                        4443      537232 ns/op      1254 B/op       37 allocs/op
BenchmarkSQLStore/mysql/Put-10                         873     2945267 ns/op      5797 B/op      120 allocs/op
BenchmarkSQLStore/mysql/Update-10                      678     3462856 ns/op      6955 B/op      149 allocs/op
BenchmarkSQLStoreVersions/sqlite/Put-10              10000      207049 ns/op     11106 B/op      270 allocs/op
BenchmarkSQLStoreVersions/sqlite/Update-10           10000      203025 ns/op     11289 B/op      271 allocs/op
BenchmarkSQLStoreVersions/sqlite/Versions-10         73472       32612 ns/op      6349 B/op      112 allocs/op
BenchmarkSQLStoreVersions/postgres/Put-10              757     3656566 ns/op     13435 B/op      251 allocs/op
BenchmarkSQLStoreVersions/postgres/Update-10           639     3787582 ns/op     13630 B/op      253 allocs/op
BenchmarkSQLStoreVersions/postgres/Versions-10        6441      363998 ns/op      6661 B/op      111 allocs/op
BenchmarkSQLStoreVersions/mysql/Put-10                 428     5599093 ns/op     11458 B/op      252 allocs/op
BenchmarkSQLStoreVersions/mysql/Update-10              466     5209521 ns/op     11436 B/op      248 allocs/op
BenchmarkSQLStoreVersions/mysql/Versions-10           4384      688977 ns/op      5349 B/op       98 allocs/op
```

### A versioned write costs its extra statements, and nothing else

`TestSQLVersionsRoundTrips` counts what a write adds when it makes a version
and prunes one: the current number, read locked; its row given the document it
held; the new row; the question of what to prune; the pruning — five
statements — and a `Put` one more, since its claim locks the row and the
document is then written under that lock. On PostgreSQL an indexed `Put` goes
from 1.86 ms to 3.66 ms, six round trips at 0.31 ms; an `Update` from 2.11 ms
to 3.79 ms, five. MySQL reads the same at about 0.55 ms a round trip. On
SQLite the statements share one commit, so a versioned write costs 1.4 times a
plain one (207 µs against 144, 203 against 153 for an `Update`), not twice.

Reading every version of a document is one statement whatever it holds: a
LEFT JOIN of the documents' table and the versions', 0.36 ms on PostgreSQL —
about a `Get` — and 33 µs on SQLite for eleven versions.
