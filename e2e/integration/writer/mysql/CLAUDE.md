<!-- updated: 2026-10-03T13:06:04Z -->
# e2e/integration/writer/mysql/

## Purpose

The `"mysql"` logger writer (`third-party/db/writer/mysql`) against a real
MySQL 8 server: the production sink built through its registered factory
(`writer.Open`), records written and drained, and the rows read back with a raw
`*sql.DB`. An `//go:build integration` test in the auxiliary `e2e` module
(ADR 0157) — see `e2e/integration/CLAUDE.md` for why it lives here and how the
integration suites run.

## Contents

| File | Case |
|---|---|
| `mysql_integration_test.go` | `TestMySQLIntegration_WriteAndRead`: `mysql:8` started by the testcontainers MySQL module with its socket directory bind-mounted to a host directory; the `(ts, level, message)` table the writer documents created over a raw handle; three records written, the partial batch flushed, the sink closed; all three rows read back, the first one's level and message verbatim |

## Rules

- **The writer speaks the Unix socket only** (`MySQLConfig` exposes no TCP host
  or port — the local-protocol policy), so the suite bind-mounts
  `/var/run/mysqld` onto a world-writable host directory and waits for the
  socket file to appear before the first connection. The read-back handle uses
  the same socket, never TCP.
- **Through the registered factory only**, with a `CredentialProvider` handing
  the container's root login to the sink, as an operator's provider does.
- **No Docker, no failure**: the case skips when the container cannot start or
  the socket never appears (a bind mount the runtime does not support).

## Attention point

On macOS the bind-mounted socket path under `$TMPDIR` exceeds the kernel's
104-byte `sun_path`, and the connection fails with `invalid argument`. It
reproduces identically on the tree before ADR 0157 — open, not caused by the
move (`e2e/integration/CLAUDE.md`).

## Run

```sh
cd e2e && GOWORK=off go test -tags integration ./integration/writer/mysql/
```

Run it before shipping a change to the writer's factory, batching or schema.
