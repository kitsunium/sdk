<!-- updated: 2026-10-02T19:57:17Z -->
# framework/connectors/sqlite — kit's SQLite engine

The module `github.com/kitsunium/sdk/framework/connectors/sqlite`: the one a
product's `main` imports to keep a database in a SQLite file
(`kit.Database(name, sqlite.Engine())`), and the only code of the product
that links a SQLite driver — modernc.org/sqlite, which needs no cgo, so the
product stays one static binary (CI builds the module with `CGO_ENABLED=0`).
`connectors/CLAUDE.md` holds the rules every engine follows.

| File | Holds |
|---|---|
| `sqlite.go` | `Engine()`: `Dialect` (the SDK's `sql.DialectSQLite`); `Describe` — the file, and no address, user or password —; `Open` — the file, made if need be; `current` is never asked, a file has no credentials to rotate —; `dsn` and `split`, which read a path or a `file:` URI and add the `defaults` the URL does not write: `_journal_mode=WAL`, `_busy_timeout=5000`, `_txlock=immediate` |
| `driver.go` / `driver_none.go` | the blank import of modernc.org/sqlite, on every GOOS it has a port to; on dragonfly, illumos and solaris (its libc excludes them) `driverLinked` is false and `Open` refuses with `proc.UnsupportedPlatform`, so the module still builds there (the cross-build lane) |

Rules:

- Without a URL in `<APP>_<NAME>_URL`, kit keeps the database in
  `<data>/<name>.sqlite`.
- The defaults let two writers queue rather than fail: WAL, so readers do not
  wait for a writer; a busy timeout, so a writer waits for another; and the
  write lock taken when a transaction begins. A parameter the URL writes
  wins.
- Migrations run — kit's own set, which makes the tables of the stores the
  database keeps, then the modules' and the product's — under the database
  file's own write lock, one transaction per run (the SDK's ADR 0140). A
  migration cannot run what SQLite refuses inside a transaction: `VACUUM`,
  `PRAGMA journal_mode`.
- `kit.Keeps(kit.Privacy)` on the file keeps kit's holds and journal there,
  and kit's data keys in the data directory: a key is written apart from
  every transaction, and the file's one writer may be held by the
  transaction of the write that needs it — it waited the busy timeout, then
  failed (kit's `placementOf`, the platform's ADR 0004 step 2 as built). A store the keys
  seal lives beside kit.Privacy on the file.
- `go.mod` requires the SDK module, `github.com/kitsunium/sdk` — kit and the
  packages below it — and replaces it with this tree; a release that changes
  this module tags it and pins the SDK at that release (ADR 0162). It is a
  module of the SDK's `go.work`.

## Test

`go test -race ./...` here — no server, so every test runs
(`filestore_test.go`'s `needsFileStore` skips an app with a data directory
on Windows and Plan 9, where `vfs.NewOS` refuses; `subsystems_test.go`
imports `framework/kit/server`, since the suite's apps are servers).
`sqlite_internal_test.go` holds the URL's parameters winning over the
defaults; `sqlite_test.go` describes paths and URIs, has two pools write at
once, runs a product in production — its file beside the data, or where the
URL says —, its migrations and its store in the file, a store that seals —
its row holds boxes, reads back after a restart, and an erasure's committed
transaction destroys the key a backup's row was sealed under, one taken
back destroys nothing —, and the stores' conformance suite
(`kit/storetest`). `keys_test.go` puts `kit.Keeps(kit.Privacy)` on the file:
the keys in the data directory, and each data key made without waiting for
the file's writer — a hold of a sealed record and a write of it, in their
own transactions; inside `kit.Transact` on the file a case written and
held, a hold of kit's own, a sealed letter published and a sealed reminder
dispatched, delivered and opened after the commit. With the keys on the
file, each waited the 5 s busy timeout, then failed `SEAL_WRITE`.
