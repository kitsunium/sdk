# third-party/db/writer/mysql/

## Purpose

Registers the **"mysql"** writer factory (ADR 0015): a database log sink that
batches records into multi-row INSERTs over a MySQL **Unix-domain socket**
(local-protocol). Importing the package self-registers the factory (no `init()`),
so `writer.Open("mysql", writer.MySQLConfig{…})` resolves.

Dep-light: the `github.com/go-sql-driver/mysql` import is confined to `client.go`
and lives in the **root** module only, so `pkg/v1` consumers never pull a DB
driver into their graph (CI asserts `go list -deps ./pkg/v1/...` is driver-free).

## Contents

| File | Role |
|---|---|
| `mysql.go` | `Writer` singleton, `mysqlFactory` (`Name`/`Open`) — composes the dbsink chain |
| `mysqlsink.go` | `mysqlSink` wrapper that closes the `*sql.DB` after the chain drains |
| `client.go` | the ONLY driver-importing file: `newClient` (lazy `sql.Open`) + `buildDSN` + `buildInsert` + `validIdent` |
| `codes.go`, `errors.go` | sentinels — range 0.3.32.\* (service slot 0x20) |

## Table schema (operator-provided)

The destination table MUST exist with these columns:

```sql
CREATE TABLE app_logs (
  ts      DATETIME,
  level   VARCHAR(16),
  message TEXT
);
```

The table name is validated as a plain identifier (`[A-Za-z0-9_]+`) and
interpolated into the INSERT — it cannot be a bound parameter. Every **value**
is bound via `?` placeholders (no injection surface from record content).

## Credentials

`MySQLConfig.Credentials` is REQUIRED and supplied programmatically
(`CredentialProvider`); like the AWS writers, this factory has **no config-file
Decoder** because a live credential cannot be expressed safely in YAML.
`AccessKeyID()` maps to the MySQL username, `SecretAccessKey()` to the password
(the redacting `CredentialValue` keeps it out of `%v`/`%#v`). The DSN (which
embeds the password) is never echoed into an error.

## Behaviour

- `Open` builds a **lazy** `sql.Open` handle (no connection) and composes
  `dbsink.Compose` → `levelgate(async(dbSink))`; the first INSERT on the drainer
  goroutine establishes the connection. The producer never blocks on the DB.
- A batch is delivered as one `INSERT … VALUES (?,?,?),(?,?,?)…`. `Close` drains
  the chain then closes the connection pool.

## Error catalogue — range 0.3.32.\*

| Code | Sentinel | Trigger |
|---|---|---|
| 0.3.32.1 | `ClientInitFailed` | nil/unresolvable credentials, invalid table, or `sql.Open` failure (EX_IOERR) |
| 0.3.32.2 | `InsertFailed` | the multi-row INSERT returned an error (EX_IOERR) |

## Testing

Unit tests use a lazy offline handle + fake `CredentialProvider`; no MySQL server
is contacted (the connection is never established). A live integration test
against a real socket is a future addition behind a build tag (mirror s3's
`localstack_test.go`).

## Do NOT

- Import the driver outside `client.go`.
- Pass a consumer-controlled table name without the `validIdent` guard.
- Echo the DSN / credentials / record content into an error.

## Verification

```sh
bazel test --config=race //third-party/db/writer/mysql:mysql_test
# Fallback
go test -race ./third-party/db/writer/mysql/...
```
