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
- No migrations yet: kit refuses a SQLite database that declares some. The
  SDK's migrator needs an advisory lock SQLite does not have, and a migrator
  on the file's own lock is an SDK change to come.
- `go.mod` requires the framework at the last release and replaces it — and
  the SDK modules below it — with this tree; the release commit pins them
  (ADR 0147 §9). It is a module of the SDK's `go.work`.

## Test

`go test -race ./...` here — no server, so every test runs.
`sqlite_internal_test.go` holds the URL's parameters winning over the
defaults; `sqlite_test.go` describes paths and URIs, has two pools write at
once, runs a product in production — its file beside the data, or where the
URL says — and holds the refusal of migrations.
