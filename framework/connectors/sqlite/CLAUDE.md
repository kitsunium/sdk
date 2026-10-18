<!-- updated: 2026-10-02T19:57:17Z -->
# framework/connectors/sqlite — kit's SQLite engine

The module `github.com/kitsunium/sdk/framework/connectors/sqlite`: the one a
product's `main` imports to keep a database in a SQLite file
(`app.Database(name, sqlite.Engine())`), and the only code of the product
that links a SQLite driver — modernc.org/sqlite, which needs no cgo, so the
product stays one static binary (CI builds the module with `CGO_ENABLED=0`).
`connectors/CLAUDE.md` holds the rules every engine follows.

| File | Holds |
|---|---|
| `sqlite.go` | `Engine()`: `Dialect` (`app.DialectSQLite`, kit's own type — ADR 0179 Q5); the blank import of `framework/app/store`, the stores a database keeps (ADR 0179 §4); `Describe` — the file, and no address, user or password —; `Open` — the file, made if need be; `current` is never asked, a file has no credentials to rotate —; `OpenWaiting` — `app.LockWaiter`, the busy timeout kit gives —; `dsn` and `split`, which read a path or a `file:` URI and add the `defaults` the URL does not write under either spelling: `_journal_mode=WAL`, `_busy_timeout=5000` (or kit's wait), `_txlock=immediate` |
| `driver.go` / `driver_none.go` | the blank import of modernc.org/sqlite, on every GOOS it has a port to; on dragonfly, illumos and solaris (its libc excludes them) `driverLinked` is false and `Open` refuses with `proc.UnsupportedPlatform`, so the module still builds there (the cross-build lane) |

Rules:

- Without a URL in `<APP>_<NAME>_URL`, kit keeps the database in
  `<data>/<name>.sqlite`.
- The defaults let two writers queue rather than fail: WAL, so readers do not
  wait for a writer; a busy timeout, so a writer waits for another; and the
  write lock taken when a transaction begins. A parameter the URL writes
  wins — under either spelling the driver reads (`_busy_timeout` or
  `_timeout`, `_journal_mode` or `_journal`).
- The busy timeout is kit's (ADR 0178 C2): the engine is a `app.LockWaiter`,
  and kit opens it with `OpenWaiting` and `B = T − max(250ms, T/10)`, `T`
  being `<name>-timeout` — 4.5 s for the default 5 s —, so a lost race on the
  write lock comes back as SQLite's refusal before the call's deadline
  (`TestALostLockComesBackBeforeTheCallsDeadline`). kit refuses at start a
  `T` under 500 ms and a URL whose own busy timeout reaches `T`
  (`database.sqlite-busy-timeout`), and never rewrites it; `Open` alone
  keeps the engine's 5 s.
- kit's consumer queues live on the database that keeps their service
  (ADR 0178 C1): `queue_test.go` runs the outbox on a real file — a
  publication, a queued dispatch and a watch's notice written in the
  transaction, absent after a rollback, a refusal failing the transaction.
- `Records.Keys` and the watches' catch-up (ADR 0178 C6): `catchup_test.go`
  lists the keys a watch hears through the SDK's document store over SQL,
  hears after a stop that was not clean the memo whose notice was lost, and
  after a clean stop nothing again; `replay_test.go` runs `watch replay`
  through `Main` with the product stopped — the memos no watch heard queued
  in the watch's table and heard at the next start, a name that is no watch
  refused with 1.
- Migrations run — kit's own set, which makes the tables of the stores the
  database keeps, then the modules' and the product's — under the database
  file's own write lock, one transaction per run (the SDK's ADR 0140). A
  migration cannot run what SQLite refuses inside a transaction: `VACUUM`,
  `PRAGMA journal_mode`.
- `app.Keeps(app.Privacy)` on the file keeps kit's holds and journal there,
  and kit's data keys in the data directory: a key is written apart from
  every transaction, and the file's one writer may be held by the
  transaction of the write that needs it — it waited the busy timeout, then
  failed (kit's `placementOf`, the platform's ADR 0004 step 2 as built). A store the keys
  seal lives beside app.Privacy on the file.
- `go.mod` requires the root module, `github.com/kitsunium/sdk` — kit and the
  framework's packages —, `internal` and the `pkg/v1` families its build
  reaches (ADR 0171), each replaced with its directory of this tree; a release
  that changes this module tags it and pins each at its version. It is a
  module of the SDK's `go.work`.

## Test

`go test -race ./...` here — no server, so every test runs
(`filestore_test.go`'s `needsFileStore` skips an app with a data directory
on Windows and Plan 9, where `vfs.NewOS` refuses; `subsystems_test.go`
imports `framework/app/server`, since the suite's apps are servers).
`sqlite_internal_test.go` holds the URL's parameters winning over the
defaults; `sqlite_test.go` describes paths and URIs, has two pools write at
once, runs a product in production — its file beside the data, or where the
URL says —, its migrations and its store in the file, a store that seals —
its row holds boxes, reads back after a restart, and an erasure's committed
transaction destroys the key a backup's row was sealed under, one taken
back destroys nothing —, and the stores' conformance suite
(`app/storetest`). `keys_test.go` puts `app.Keeps(app.Privacy)` on the file:
the keys in the data directory, and each data key made without waiting for
the file's writer — a hold of a sealed record and a write of it, in their
own transactions; inside `app.Transact` on the file a case written and
held, a hold of kit's own, a sealed letter published and a sealed reminder
dispatched, delivered and opened after the commit. With the keys on the
file, each waited the 5 s busy timeout, then failed `SEAL_WRITE`.
`former_test.go` declares the product `testdata/former` was written by — a
release before ADR 0180 step 5, on SQLite (`former/sqlite`: `kit_*` tables, a
`kit.privacy` directory) and in the data directory alone (`former/files`),
with what it read then (`dump.json`, `journal.txt`, `holds.txt`); never
regenerate it with this release, whose names are the present ones.
`former_move_test.go` moves it and reads it back: every row and every file
byte for byte, the journal's chain and the holds as the former release
printed them, the cases, versions, former values, trash and a person's data
opened, the kept letter delivered, nothing moved by a second start; a
present name taken refuses the start and nothing moves; a renaming the
database refuses half-way rolls the move back and the next start resumes;
and the move is undone by renaming back — the procedure ADR 0180 gives —,
which restores the fixture byte for byte.
