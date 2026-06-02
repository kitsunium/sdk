# third-party/db/writer/redis/

## Purpose

Registers the **"redis"** writer factory (ADR 0015): a log sink that appends
records to a **Redis Stream** via pipelined `XADD` over a Redis **Unix-domain
socket** (local-protocol). Importing the package self-registers the factory (no
`init()`), so `writer.Open("redis", writer.RedisStreamConfig{…})` resolves.

Dep-light: the `github.com/redis/go-redis/v9` import is confined to `client.go`
and lives in the **root** module only, so `pkg/v1` consumers never pull the
driver into their graph. The driver's `*redis.Client` never leaves `client.go`:
`newClient` returns a `closeFn` closure instead, so `redissink.go` holds no
driver type.

## Contents

| File | Role |
|---|---|
| `redis.go` | `Writer` singleton, `redisFactory` (`Name`/`Open`) — composes the dbsink chain |
| `redissink.go` | `redisSink` wrapper that calls the client `closeFn` after the chain drains |
| `client.go` | the ONLY driver-importing file: `newClient` + `resolveCreds` + `buildValues` |
| `codes.go`, `errors.go` | sentinels — range 0.3.34.\* (service slot 0x22) |

## Behaviour

- `Open` builds a **lazy** `go-redis` client (no connection) and composes
  `dbsink.Compose` → `levelgate(async(dbSink))`; the first XADD on the drainer
  goroutine connects. The producer never blocks on Redis.
- A batch is delivered as one **pipelined** run of `XADD` (one round-trip). Each
  entry carries `ts` (UnixNano), `level`, `message`. When `MaxLen > 0`, `XADD`
  uses `MAXLEN ~` (approximate trim, whole macro-nodes) so the stream is bounded.
- `Close` drains the chain then releases the client.

## Credentials

`RedisStreamConfig.Credentials` is OPTIONAL (a socket-local Redis may have no
AUTH). When set, `AccessKeyID()` maps to the ACL username and `SecretAccessKey()`
to the password (redacting `CredentialValue`, never echoed). Like the AWS
writers, there is **no config-file Decoder** — a live credential cannot be
expressed safely in YAML.

## Error catalogue — range 0.3.34.\*

| Code | Sentinel | Trigger |
|---|---|---|
| 0.3.34.1 | `ClientInitFailed` | missing socket/stream or an unresolvable credential provider (EX_IOERR) |
| 0.3.34.2 | `AddFailed` | the pipelined XADD batch returned an error (EX_IOERR) |

## Do NOT

- Import the driver outside `client.go`, or return `*redis.Client` from it (use
  the `closeFn` seam).
- Echo the socket path / credentials / record content into an error.

## Verification

```sh
bazel test --config=race //third-party/db/writer/redis:redis_test
# Fallback
go test -race ./third-party/db/writer/redis/...
```
