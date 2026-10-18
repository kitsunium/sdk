<!-- updated: 2026-10-02T19:57:17Z -->
# framework/connectors/mysql — kit's MySQL and MariaDB engine

The module `github.com/kitsunium/sdk/framework/connectors/mysql`: the one a
product's `main` imports to keep a database on MySQL or MariaDB
(`app.Database(name, mysql.Engine())`), and the only code of the product
that links a MySQL driver — go-sql-driver/mysql, through `database/sql`.
`connectors/CLAUDE.md` holds the rules every engine follows.

| File | Holds |
|---|---|
| `mysql.go` | `Engine()`: `Dialect` (`app.DialectMySQL`, kit's own type — ADR 0179 Q5); the blank import of `framework/app/store`, the stores a database keeps (ADR 0179 §4); `Describe` — the address (TCP, or a socket), the database, and the `tls` as the URL writes it, never a user or a password —; `Open` — the driver's connector, with a `BeforeConnect` that reads the URL again (`current`) —; `config` and `parse`, which read a `mysql://` or `mariadb://` URL (its query holds the driver's parameters; port 3306 when left out) or the driver's own DSN, drop the TLS configuration the DSN parser built for another address, and silence the driver's logger (`quiet`); `errMalformed`, which quotes nothing |
| `credentials.go` | `withCredentials`: the user and the password of the URL as it is now, given to the connection the driver is about to make |

Rules:

- Outside dev kit refuses a URL that reaches a server over the network
  without writing its `tls` (kit's `tlsLeft`): the driver's default is no TLS
  at all. `tls=false` is accepted when written; a socket needs none.
- A DDL statement commits by itself on MySQL: a migration holds one, or a
  failure half-way leaves the first applied (the SDK's ADR 0055, D8).
- The driver's logger says nothing (`quiet`): its lines would name the
  server, and a product logs through the SDK alone.
- `go.mod` requires the root module, `github.com/kitsunium/sdk` — kit and the
  framework's packages —, `internal` and the `pkg/v1` families its build
  reaches (ADR 0171), each replaced with its directory of this tree; a release
  that changes this module tags it and pins each at its version. It is a
  module of the SDK's `go.work`.

## Test

`go test -race ./...` here (`subsystems_test.go` imports
`framework/app/server`: the suite's apps are servers). `mysql_test.go` always runs: `Describe` over
URLs, a DSN and a socket, and malformed ones that leak nothing. The
end-to-end tests skip unless `KIT_TEST_MYSQL_URL` names a server where they
may create and drop the database `kit_test` and a user — an administrator's
URL, as CI's `mysql:8.4` service gives: `server_test.go` holds their constant
statements and the rotation's user, whose passwords are bound — the
administrator's pool interpolates them (`interpolateParams`), since MySQL
takes no parameter in a `CREATE USER` —, never written; `app_test.go` the
`ledger` product, in production on the default database, and a proxy;
`database_test.go` migrations — kit's own set, and the tables it makes, first
—, two processes migrating once, `migrate` status, up and down, readiness,
an unreachable server, credentials that never leak, a rotated password
reaching the next connection, and the stores' conformance suite
(`app/storetest`). ADR 0180's steps on a former release's layout:
`former_test.go` (the names at rest moved, step 5a), `former_keys_test.go`
(the data keys re-wrapped, step 5b) and `former_journal_test.go` (step
5c-1: a former journal written into app.privacy's table, its chain
computed as the journal documents it, closed as it is by the next entry,
`privacy journal -verify` holding across the two and breaking where a
former entry is changed).
