<!-- updated: 2026-11-12T00:00:00Z -->
# framework/internal/app/streamengine — the streams of the streamed topics

What `framework/app/stream` plugs into kit (`plug.Streams`, issue #826, the
shape of ADR 0179 step 4): the log a streamed topic (`Topic.Streamed`)
appends each message to ONCE — the SDK's stream (`pkg/v1/data/stream`, ADR
0231) in memory, in the data directory over `kernel/fs/seglog`, or over SQL
joined to the caller's transaction —, the consumer groups its subscriptions
read it as (the SDK's `Subscribe`), and the migration of a stream's three
tables. kit's core imports none of the SDK's stream: it opens every stream
through `plug` with a `plug.StreamConfig` and holds it as a `plug.Stream`,
and a program that does not import `framework/app/stream` links none of
this package (`framework/app`'s `TestTheSmallestProductLinksNoSubsystem`,
`kitEngines` and `optInSDK`) and is refused at the start for a streamed
topic or its subscription (`CodeSubsystemMissing`).

## Contents

| File | Holds |
|---|---|
| `streamengine.go` | `Enable`, which sets `plug.Streams` (`open`, `tableMigration`); `open` — on the config's `Database` the SQL stream (`openSQL`, on the transaction manager and dialect `storeengine.SQLOf` hands on, so an append joins the transaction its context carries), else a file stream in `Dir`, else memory, `Partitions` from the config — behind a test's wrapper (`Wrap`, a `func(stream.Stream) stream.Stream`); `tableMigration`, `dialectOf`, `pluggedStep` |
| `stream.go` | `stream`, one of the SDK's streams as kit's core uses it: `Append` (one record, one batch — one sync on the file engine), `Join` (a group the stream does not know is committed where it starts on every partition: the next offset — from now, the default —, `Seeker.OffsetAt(From)`, or nothing for `FromBeginning`, the SDK's subscriber starting a new group at the first record kept), `Subscribe` (`Join`, then the SDK's `Subscribe` — every handler idempotent, on the app's clock, its `SettleTimeout` —, each record handed on as a `plug.StreamRecordValue`, at most `Parallelism` at once; once the app's context ended no record starts — refused, it is read again at the next start — and the subscriber's own context ends when none runs, at the latest `DrainTimeout` after: `running`, `drainAfter`), `Close` (a file stream's directory) |
| `clock.go` | `sdkClock`, the app's clock (`plug.Clock`) as the SDK's stream stamps and waits on it |
| `streamengine_compliance.go` | the compile-time assertions: a `stream` is a `plug.Stream`, the clock the SDK's |
| `doc.go`, `README.md` | the package comment (kit writes it from the design, ADR 0167); the README, written by `tools/genindex` from `docs/api` (`make docs-readme`) |

## Rules

- It imports `plug`, `storeengine` (`SQLOf`: a stream on a database joins the store subsystem's transactions) and the SDK, never kit's core.
- The core encodes every message: a stream carries bytes, and nothing here names a message's type. A message's key — `Streamed`'s key, else the topic's node — chooses its partition.
- A refusal of the SDK's stream reaches the core as the SDK's error, which the core wraps in its own codes (`CodeQueueOpen` as `STREAM_OPEN`, `CodeTopicPublish` as `TOPIC_APPEND`).

## Verify

```sh
cd framework && GOWORK=off go vet ./internal/app/streamengine/ && GOWORK=off go test -race ./internal/app/ -run 'Streamed' && GOWORK=off go test ./app/ -run 'TheCore|TheSmallest|ImportsThe'
```
