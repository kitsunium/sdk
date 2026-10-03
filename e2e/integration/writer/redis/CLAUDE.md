<!-- updated: 2026-10-03T13:06:04Z -->
# e2e/integration/writer/redis/

## Purpose

The `"redis"` logger writer (`third-party/db/writer/redis`) against a real
Redis server: records appended to a stream with `XADD` over the Unix socket,
read back with `XRANGE` over TCP. An `//go:build integration` test in the
auxiliary `e2e` module (ADR 0157) — see `e2e/integration/CLAUDE.md` for why it
lives here and how the integration suites run.

## Contents

| File | Cases |
|---|---|
| `redis_integration_test.go` | `Test_Integration_RedisWriter`, over one `redis:7-alpine` container listening on a bind-mounted Unix socket (the writer's) and TCP (the read-back client's): a batch of three records read back from the stream in order and verbatim; `MaxLen` bounding the stream; an unreachable socket surfacing `AddFailed` through the `OnError` hook; the registered factory resolving and building a sink that closes cleanly |

## Rules

- **The writer dials `net=unix` only**, so the host directory holding the
  socket is bind-mounted into the container (mode `0777`, so the server's user
  can create the socket there) and the suite waits for the socket file before
  the first `XADD`.
- **The sink is the production one**: built through the writer package's
  registered `Writer` with a `RedisStreamConfig`, never a test double.
- **No Docker, no failure**: every startup failure, and a socket that never
  appears, skips.

## Attention points

- `MaxLen` trims approximately (macro-node trimming), so the case asserts a
  bound rather than an exact length — and the server is free not to trim at
  all, so the case can fail against a conforming server. Open, and identical on
  the tree before ADR 0157 (`e2e/integration/CLAUDE.md`).
- On macOS the socket path under `$TMPDIR` can exceed the kernel's 104-byte
  `sun_path` (`connect: invalid argument`) — the same open issue as the mysql
  suite.

## Run

```sh
cd e2e && GOWORK=off go test -tags integration -timeout 180s ./integration/writer/redis/
```

Run it before shipping a change to the writer's factory, batching or trimming.
