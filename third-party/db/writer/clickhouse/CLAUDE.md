# third-party/db/writer/clickhouse/

## Purpose

Registers the **"clickhouse"** writer factory (ADR 0015): a database log sink
that batches records into multi-row INSERTs over the ClickHouse **native
protocol**. Importing the package self-registers the factory (no `init()`), so
`writer.Open("clickhouse", writer.ClickHouseConfig{…})` resolves.

Dep-light: the `github.com/ClickHouse/clickhouse-go/v2` import is confined to
`client.go` and lives in the **root** module only, so `pkg/v1` consumers never
pull the driver into their graph. The package uses the driver's `database/sql`
surface (`clickhouse.OpenDB`, lazy) so the code mirrors the mysql writer exactly.

## Contents

| File | Role |
|---|---|
| `clickhouse.go` | `Writer` singleton, `clickhouseFactory` (`Name`/`Open`) — composes the dbsink chain |
| `clickhousesink.go` | `chSink` wrapper that closes the `*sql.DB` after the chain drains |
| `client.go` | the ONLY driver-importing file: `newClient` (lazy `OpenDB`) + `resolveCreds` + `buildInsert` + `validIdent` |
| `codes.go`, `errors.go` | sentinels — range 0.3.33.\* (service slot 0x21) |

## Table schema (operator-provided)

```sql
CREATE TABLE app_logs (
  ts      DateTime,
  level   String,
  message String
) ENGINE = MergeTree ORDER BY ts;
```

The table name is validated as a plain identifier and interpolated into the
INSERT; every **value** is bound via placeholders (no injection from content).

## Credentials

`ClickHouseConfig.Credentials` is OPTIONAL (empty falls back to the default
user) and supplied programmatically; `AccessKeyID()` maps to the username and
`SecretAccessKey()` to the password (redacting `CredentialValue`, never echoed).
Like the AWS writers, there is **no config-file Decoder**.

## Behaviour

- `Open` builds a **lazy** `clickhouse.OpenDB` handle (no connection) and
  composes `dbsink.Compose` → `levelgate(async(dbSink))`; the first INSERT on the
  drainer goroutine connects. The producer never blocks on the DB.
- A batch is delivered as one `INSERT … VALUES (?,?,?)…`. `Close` drains the
  chain then closes the connection pool.

## Error catalogue — range 0.3.33.\*

| Code | Sentinel | Trigger |
|---|---|---|
| 0.3.33.1 | `ClientInitFailed` | invalid table or an unresolvable credential provider (EX_IOERR) |
| 0.3.33.2 | `InsertFailed` | the multi-row INSERT returned an error (EX_IOERR) |

## Do NOT

- Import the driver outside `client.go`.
- Echo the credentials / record content into an error.

## Verification

```sh
bazel test --config=race //third-party/db/writer/clickhouse:clickhouse_test
# Fallback
go test -race ./third-party/db/writer/clickhouse/...
```

### Opt-in integration test (no CI lane — run it by hand)

`clickhouse_integration_test.go` carries `//go:build integration`, so **no CI
lane runs it**: it needs a Docker-compatible runtime to spin up
`clickhouse/clickhouse-server:24-alpine` via testcontainers-go.
`TestIntegration_clickhouseFactory_roundtrip` drives the real registered factory
(`writer.Open("clickhouse", …)`) through Open → Write N → Flush → Close and reads
the rows back. It self-skips when no runtime is reachable, so the default lane
stays green either way. Per rule 12 this is a *declared* exemption, not an
oversight — run it before shipping a change to the factory or the batching chain:

```sh
GOWORK=off go test -tags integration -timeout 180s ./third-party/db/writer/clickhouse/...
```
