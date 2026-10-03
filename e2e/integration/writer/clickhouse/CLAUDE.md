<!-- updated: 2026-10-03T13:06:04Z -->
# e2e/integration/writer/clickhouse/

## Purpose

The `"clickhouse"` logger writer (`third-party/db/writer/clickhouse`) against
a real ClickHouse server, through the path a consumer takes: the blank import
registers the factory, `writer.Open("clickhouse", …)` builds the sink, and a
raw `clickhouse-go` client reads the rows back. An `//go:build integration`
test in the auxiliary `e2e` module (ADR 0157) — see `e2e/integration/CLAUDE.md`
for why it lives here and how the integration suites run.

## Contents

| File | Case |
|---|---|
| `clickhouse_integration_test.go` | `TestIntegration_clickhouseFactory_roundtrip`: `clickhouse/clickhouse-server:24-alpine` started by the testcontainers ClickHouse module; the three-column table the writer documents created over a raw client; records written through the production chain (level gate → async ring → batcher), `Flush`, `Close`; every record read back exactly once, its message verbatim, its level as `Level.String()`, its time to the second (the column is `DateTime`) |

## Rules

- **Through the registered factory only.** The suite drives
  `internal/core/observe/logger/writer`'s registry exactly as a consumer's
  configuration does; constructing the sink directly would skip the path that
  ships.
- **The credentials are a `CredentialProvider`** handing the container's login
  to the production sink (`AccessKeyID` the user, `SecretAccessKey` the
  password), as an operator's provider does.
- **Sequential.** A container start is heavy; the case does not run in parallel.
- **No Docker, no failure**: the case skips when the container cannot start.

## Run

```sh
cd e2e && GOWORK=off go test -tags integration -timeout 180s ./integration/writer/clickhouse/
```

Run it before shipping a change to the writer's factory, batching or schema.
