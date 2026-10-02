<!-- updated: 2026-10-02T19:57:17Z -->
# framework/connectors/postgres — kit's PostgreSQL engine

The module `github.com/kitsunium/sdk/framework/connectors/postgres`: the one a
product's `main` imports to keep a database on PostgreSQL
(`kit.Database(name, postgres.Engine())`), and the only code of the product
that links a PostgreSQL driver — pgx v5, through `database/sql` (`stdlib`).
`connectors/CLAUDE.md` holds the rules every engine follows.

| File | Holds |
|---|---|
| `postgres.go` | `Engine()`: `Dialect` (the SDK's `sql.DialectPostgres`); `Describe` — host:port or a socket's directory, the database, and the `sslmode` as the URL writes it (`sslmode` reads a URL's query or libpq's `key=value` form), never a user or a password —; `Open` — pgx's `stdlib.OpenDB`, with a before-connect hook that reads the URL again (`current`) —; `errMalformed`, the one error for a URL pgx cannot read, which quotes nothing |
| `credentials.go` | `withCredentials`: the user and the password of the URL as it is now, given to the connection pgx is about to make |

Rules:

- The URL is libpq's, in `<APP>_<NAME>_URL`. Outside dev kit refuses one that
  reaches a server over the network without writing its `sslmode` (kit's
  `tlsLeft`): pgx's default, `prefer`, falls back to plaintext.
  `sslmode=disable` is accepted when written; a socket needs none.
- A rotated password needs no restart: `Open`'s hook takes the credentials of
  the URL as it is before each new connection, and `<name>-max-lifetime`
  retires the connections the previous one opened.
- `go.mod` requires the framework at the last release and replaces it — and
  the SDK modules below it — with this tree; the release commit pins them
  (ADR 0147 §9). It is a module of the SDK's `go.work`.

## Test

`go test -race ./...` here (`subsystems_test.go` imports
`framework/kit/server`: the suite's apps are servers). `postgres_test.go` always runs: `Describe` over
URLs, `key=value` pairs and a socket, a malformed URL that leaks nothing, a
pool that opens with no server. The end-to-end tests skip unless
`KIT_TEST_POSTGRES_URL` names a server where they may create and drop the
database `kit_test` and roles — a superuser's URL, as CI's `postgres:17`
service gives: `server_test.go` holds their constant statements and the
rotation's two roles, whose passwords are bound through pgx's simple
protocol, never written; `app_test.go` the `ledger` product, in production on
the default database, and a proxy a test takes away and brings back;
`database_test.go` migrations — kit's own set, and the tables it makes, first
—, two processes migrating once, `migrate` status, up and down, readiness,
an unreachable server, credentials that never leak, a rotated credential
reaching the next connection — the two roles may create in `kit_test`'s
schema, where kit makes the store's table —, and the stores' conformance
suite (`kit/storetest`).
