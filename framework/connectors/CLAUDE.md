<!-- updated: 2026-10-03T03:30:00Z -->
# framework/connectors — the engines a database runs on, and the ssh identity

The platform's ADR 0004: a store is the port, a database's engine the adapter the app plugs
in. Each engine is its own Go module, requiring the SDK module (which holds
kit — ADR 0162) and one driver, and the only code of a product that imports
that driver: a product's `main` imports one to call
`kit.Database(name, <engine>.Engine())`. The SDK module imports no driver — a
driver its `go.mod` required would sit in every product's module graph and
`go.sum`.

| Module | Driver | URL |
|---|---|---|
| `postgres` | `github.com/jackc/pgx/v5` (`stdlib`) | libpq's: `postgres://…?sslmode=…` or `key=value` pairs |
| `mysql` | `github.com/go-sql-driver/mysql` | `mysql://` / `mariadb://` URL, or the driver's DSN; its `tls` parameter |
| `sqlite` | `modernc.org/sqlite` (no cgo) | the file's path or a `file:` URI; WAL, busy timeout, `_txlock=immediate` by default |

One connector is not a database engine: `ssh` (ADR 0158 §3) is the
`golang.org/x/crypto/ssh` implementation of `framework/entitlement.Identity`,
plus enrolment. It has the same shape for the same reason — the one vendor
dependency (and the `golang.org/x/sys` it brings) in a module of its own, so the
SDK module's `go.mod` never requires it — and the rules below that are about
`kit.Engine`, URLs and drivers do not apply to it; its own CLAUDE.md has its
rules. It was `third-party/entitlement`, in the root module.

Rules:

- An engine implements `kit.Engine` and nothing more: `Dialect`, `Describe`
  (where the URL points, its TLS mode *as written* — never a user, never a
  password) and `Open` (a `*sql.DB`; before each new connection it asks
  `current` for the URL as it is now and takes its credentials).
- No error an engine returns quotes the URL: a driver's parse error names
  the user and the host. kit shows no engine's or driver's text anyway.
- The MySQL driver's own logger is silenced: a product logs through the SDK.
- Each engine is a module of the SDK's workspace (`go.work`), tagged by a
  release only when it changed since its own last tag (ADR 0162): the tag
  carries the module's subdirectory (`framework/connectors/postgres/vX.Y.Z`)
  at the version of the SDK release that carries the change, and on it the
  `go.mod` requires the SDK module at that version. In development its
  `go.mod` requires `github.com/kitsunium/sdk` and replaces it with this tree.
- Its codes are layer 4, one `PP` each: `0.4.16.*` postgres, `0.4.17.*`
  mysql, `0.4.18.*` sqlite (`URL_MALFORMED`, `.1`). `ssh` keeps the range it
  had as `third-party/entitlement`, `0.3.65.*` (`ENROLMENT_FAILED`, `.1`): a
  code keeps its value when it moves (ADR 0160).

## Test

Each module is tested in its own directory, `go test -race ./...`. The
engines' unit tests always run; the end-to-end ones (a kit app on the
engine: migrations — kit's own set first, which makes the store's table —,
two processes migrating together, `migrate` status, up and down, readiness
through a proxy taken away and back, credentials that never leak, a rotated
credential used by the next connection once `<name>-max-lifetime` retires
the old one, and `kit/storetest`, the stores' conformance suite, each case
on a fresh `kit_test`) run when `KIT_TEST_POSTGRES_URL`
or `KIT_TEST_MYSQL_URL` names a server they may create and drop a database
and users on — CI's service containers. Every statement they send, and
every value they bind, is a constant (`server_test.go`): the database
`kit_test`, made afresh for each test, and the rotation's fixed users. SQL
built from a variable is flagged, even in a test, and rightly:

```sh
docker run -d -p 127.0.0.1:5432:5432 -e POSTGRES_HOST_AUTH_METHOD=trust postgres:17
docker run -d -p 127.0.0.1:3306:3306 -e MYSQL_ALLOW_EMPTY_PASSWORD=yes mysql:8.4
KIT_TEST_POSTGRES_URL='postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable' go test -race ./...   # in postgres/
KIT_TEST_MYSQL_URL='mysql://root@127.0.0.1:3306/mysql?tls=false' go test -race ./...                      # in mysql/
```

SQLite needs no server: its tests — the conformance suite among them —
always run. The suite runs a record's revisions too (the platform's ADR 0007 §3: kept,
pruned, restored, rolled back) on every engine, and
`sqlite/revisions_test.go` runs them end to end on a file: versions sealed in
`<table>___vs`, a hold on the same file read in the write's transaction, an
erasure, a restart.
