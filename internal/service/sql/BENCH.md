<!-- generated from internal/service/sql/sql_bench_test.go — refresh with `cd internal/service && GOWORK=off go test -run '^$' -bench=. -benchmem -count=5 ./sql/` -->
# Benchmarks — `internal/service/sql`

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU               | AMD EPYC 7351P 16-Core, 8 vCPU visible |
| RAM               | 15 GiB |
| OS / kernel       | Linux 6.12.101+deb13-amd64 (Debian GNU/Linux 13, trixie) |
| Architecture      | amd64 |
| Go toolchain      | go1.27.1 linux/amd64 |
| Git branch        | `agent-a96ced10f61bb1f1a` |
| Git commit        | `e01714c` (the tree this domain was added to) |
| Generated (UTC)   | 2026-09-10 |
| Bench wall-clock  | `-benchtime=1s -count=5`, median quoted |
| Race detector     | **off** — a race build changes allocation behaviour |

## What is measured, and what is NOT

Every benchmark runs against the scripted `driver.Driver` in
`harness_external_test.go`. **No number below includes a network, a server, a
SQL parser or a disk.** That is deliberate: what this package *costs* is the
transaction bookkeeping it adds on top of `database/sql`, and the only way to
see it is to hold everything else at zero.

The consequence is stated rather than implied: **these are not numbers for
"how fast is a transaction".** A real `BEGIN`/`COMMIT` pair against PostgreSQL
is two network round trips — on a same-AZ link, 200 µs to 1 ms, which is two
to three orders of magnitude more than anything here. Reading
`TransactCommit = 3.9 µs` as a transaction's cost would be wrong by a factor
of about a hundred.

The figure that *is* actionable is the **delta against the `Std*` baselines**,
which drive the identical driver calls through `database/sql` alone. That
delta is the SDK's whole price.

## Results

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `TransactCommit` — open, one statement, commit | **3 855** | 592 | 13 |
| `StdTransactCommit` — the same three driver calls, no SDK | **3 140** | 440 | 9 |
| `TransactRollback` — the failing path (`ROLLBACK`, no commit) | **3 073** | 472 | 12 |
| `TransactNested1` — root + **1** savepoint, 2 statements | **5 970** | 984 | 29 |
| `TransactNested8` — root + **8** savepoints, 9 statements | **19 074** | 3 560 | 134 |
| `ScopedExecutorExec` — one statement through the guard | **281** | 48 | 2 |
| `StdTxExec` — the same statement straight on `*sql.Tx` | **257** | 48 | 2 |
| `NewMigrator` — 64 migrations: clone, sort, validate, pool policy | **4 170** | 2 849 | 3 |
| `PlanEmptyHistory` — dry run, 64 pending, empty version table | **7 515** | 3 472 | 16 |
| `PlanFullHistory` — dry run, 64 recorded rows scanned, none pending | **36 453** | 11 402 | 155 |
| `ParseDialect` — accepted name | **16.2** | 0 | 0 |
| `MigrationValidate` — one migration's construction gate | **3.9** | 0 | 0 |

## How to read this

- **Transaction ownership costs 715 ns and 4 allocations.** `TransactCommit`
  minus `StdTransactCommit` — 3 855 − 3 140 ns, 13 − 9 allocs, 152 B. That is
  the price of: the `resolved` config read, the `txState`, the `scopedExecutor`
  and its atomic, one `context.WithValue` for the scope, the `settled` flag and
  the deferred panic guard.

  Against a real database this is **noise**. A same-AZ PostgreSQL round trip is
  ~200 µs and a transaction is two of them, so 715 ns is roughly **0.18 %** of
  the cheapest possible real transaction. The SDK is not what makes a
  transaction slow, and this number exists so nobody has to take that on faith.

- **The `Executor` guard costs 25 ns and ZERO allocations per statement.**
  `ScopedExecutorExec` minus `StdTxExec` — 281 − 257 ns, identical 48 B / 2
  allocs. The guard is one `atomic.Bool.Load` plus one **read-locked** look at
  the poison flag; the 48 B and 2 allocations are `database/sql`'s own, on both
  sides.

  The `RWMutex` is why it is 25 ns rather than more: the poison flag is read on
  every statement and written at most once per transaction, so the read side is
  the hot one and must not serialise two goroutines sharing a transaction.

  This matters more than the per-transaction figure, because it is charged on
  **every statement** rather than once per unit of work. 25 ns is what
  "a callee cannot commit the transaction it was lent, and cannot use a stale
  handle" costs at runtime — the rest of that guarantee is paid at compile
  time, which is free.

- **One nesting level costs ~1.9 µs and 15 allocations**, and two fifths of it
  is the wire. `(19 074 − 5 970) / 7 = 1 872 ns` per level, `(134 − 29) / 7 =
  15` allocations, 368 B. Of that, **three driver `Exec` calls** — `SAVEPOINT`,
  the caller's own statement, and `RELEASE SAVEPOINT` — account for `3 × 257 =
  771 ns` at the measured `StdTxExec` rate, and against a real database they
  would account for essentially all of it: two extra round trips per nested
  scope.

  **The actionable fact is the round trips, not the nanoseconds.** A savepoint
  is two additional statements on the wire, every time. `TransactNested8`
  exists to make that visible: eight levels is nine statements of work and
  sixteen statements of bookkeeping. Nesting is correct and it is not free, and
  a caller who nests eight deep on a hot path should know that before
  production tells them.

- **The `scopeFor` chain walk is not the cost.** Depth 8 walks up to eight
  links per `Transact` to find its own manager, and the per-level cost is flat
  within measurement noise — the walk is a pointer chase over a list that is
  never longer than the caller's own nesting. Two managers over two databases
  interleaved is the case it exists for (`TestTwoManagersDoNotNestInEachOther`)
  and it is not on any budget worth defending.

- **`NewMigrator` is 4.2 µs for 64 migrations and allocates 3 times.** Clone,
  sort, then `Validate` on every entry, plus installing the pool policy. Three
  allocations for 64 migrations means `Validate` allocates **nothing** on the
  accepting path — it is five comparisons — and the sort is in place on the
  clone. This runs once at start-up; it is measured to confirm it belongs
  there rather than to defend a budget.

- **`ParseDialect` is 16.2 ns and `MigrationValidate` is 3.9 ns, both with zero
  allocations.** Neither is on any hot path — one runs once at wiring, the
  other once per migration at construction. They are measured so the claim
  "these are construction-time gates, not runtime costs" is a number rather
  than an assurance.

- **Scanning the version table costs ~450 ns and ~2.2 allocations per row.**
  `PlanFullHistory` minus `PlanEmptyHistory` is 28 938 ns for 64 rows, 7 930 B,
  139 allocations. That is `rows.Scan` into an `int64` plus one map insert per
  row, and it is `database/sql`'s row machinery rather than this package's:
  the set-difference on top of it is a map lookup per migration.

  **Why it is worth knowing:** `Plan` runs on every `Up`, and `Up` runs on
  every deployment of every replica. A schema with a thousand migrations
  applied would spend ~0.45 ms scanning its own history before it discovers
  there is nothing to do. That is fine, and it is linear, and it is stated so
  nobody has to discover the shape empirically at ten thousand.

- **The failing path is CHEAPER than the succeeding one** — `TransactRollback`
  3 073 ns against `TransactCommit` 3 855 ns — because it runs no statement of
  its own and builds no error: the caller's error travels verbatim
  (`errors.Join` with a nil rollback verdict returns the cause unchanged). A
  rollback is not a slow path, and nothing in this package makes failure
  expensive.

## What is deliberately not benchmarked

- **Anything that needs a real engine.** Whether PostgreSQL *accepts*
  `RELEASE SAVEPOINT ktn_sp_1`, what MySQL's implicit DDL commit does to a
  migration transaction, how long `pg_advisory_lock` takes under contention —
  all conformance questions, all belonging to `e2e/`, none answerable by a
  scripted driver. Measuring them here would produce a number that looks like
  an answer and is not one.
- **The migration `Up` path end to end.** It is dominated by the caller's own
  DDL, which the SDK neither writes nor bounds. `Plan` is measured because it
  is the part the SDK owns.
- **Concurrency.** The interesting contention is between *processes* racing for
  the advisory lock, which is a property of the database server. In-process
  contention on `txState.mu` is one mutex per transaction, held for a counter
  increment.

## Gates

`sql_bench_test.go` carries no build tag and no `gazelle:excluded`, so it is
discovered by both build systems and compiled by every `go test` and every
`bazel test` run of this package (CLAUDE.md rule 12 — nothing here is hidden
from normal discovery, so no compensating lane is owed). Benchmarks do not
*execute* under `go test` without `-bench`, which is the stdlib's own
behaviour and not an exclusion.
