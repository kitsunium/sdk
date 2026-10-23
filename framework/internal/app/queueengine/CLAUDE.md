<!-- updated: 2026-10-17T22:00:00Z -->
# framework/internal/app/queueengine — the queues' brokers and their consumer

What `framework/app/queue` plugs into kit (`plug.Queues`, ADR 0179 §1, step
4): the brokers kit's consumers' queues run on — the SDK's queue in memory,
in the data directory and over SQL —, the SDK's consumer that delivers them,
a mailer's outbox on a database sealed in front of the SQL broker, and the
migration of a queue's table. kit's core imports none of the SDK's queue: it
opens every queue through `plug` with a `plug.QueueConfig` and holds it as a
`plug.Broker`, and a program that does not import `framework/app/queue`
links none of this package (`framework/app`'s
`TestTheSmallestProductLinksOnlyTheSubsystemsStillInTheCore`) and is refused
at the start for a topic, a subscription, a watch, a queued command or an
outbox on a database it declares (`CodeSubsystemMissing`).

## Contents

| File | Holds |
|---|---|
| `queueengine.go` | `Enable`, which sets `plug.Queues` (`open`, `openSDK` — the SDK's broker `openBroker` places, handed as it is, for the OTLP export's forwarder on a database, issue #537 —, `tableMigration`, `trailMigration` — the SDK's `queue.SQLTrailMigration`, the trail column added to a table an earlier release made, issue #500 —, the SDK's `DoNotRetry`, `notRetryable` — `queue.CodeNotRetryable`); `open` — on the config's `Database` the SQL broker (`openSQL`, on the transaction manager and dialect `storeengine.SQLOf` hands on, so a publication joins the transaction its context carries, with `SQLConfig.Trail` set: every framework queue table has the trail column), else a file queue in `Dir`, else memory —, behind the sealing when `Seal` is set and behind a test's wrapper (`Wrap`, a `func(queue.Broker) queue.Broker`); `policyOf`; `tableMigration` and `dialectOf`, the SQL broker's one table (ADR 0178 C1) |
| `broker.go` | `broker`, one of the SDK's brokers as kit's core uses it: `Publish`, `PublishAt` and `PublishOnce` (`plug.LaterPublisher`: the SDK's `DelayedPublisher` and `OncePublisher`, issue #483 part 3), `Consume` (the SDK's `queue.Consume`, every handler idempotent, on the app's clock, with the `ConsumeConfig`'s `DrainTimeout`, `SettleTimeout` and `AutoExtend`; a handler's failure its `RetryIn` reads a delay on is the SDK's `queue.RetryAfter`), `DeadLetters`, and the data copy's `Messages`, `PutMessage`, `PutDeadLetter` — a dead letter's `Trail` carried both ways (issue #500) —; `Close` (a file queue's descriptors); `BrokerOf`, the SDK's broker behind a queue this package opened — what `mailengine`'s spool keeps a mailer's outbox in on a database (ADR 0179 step 6); the conversions between the SDK's values and plug's |
| `outbox.go` | `sealedOutbox`, a mailer's outbox on a database (ADR 0178 C7 item 7): the SQL broker, each mail sealed on its way in (`Publish`, `PutMessage`, `PutDeadLetter`, a mail above `MaxPayload` the queue's `MessageTooLarge`) and opened on its way out (`Receive`, `DeadLetters`, `Messages`), every sibling of the SQL broker answered — the sealing itself kit's, handed in `plug.QueueConfig`'s `Seal` and `Unseal` |
| `clock.go` | `sdkClock`, the app's clock (`plug.Clock`) as the SDK's queue stamps and waits on it — a test's manual clock included |
| `queueengine_compliance.go` | the compile-time assertions of every plug interface a broker implements, every SDK sibling the sealed outbox answers, and the clock |
| `doc.go`, `README.md` | the package comment (kit writes it from the design, ADR 0167); the README, written by `tools/genindex` from `docs/api` (`make docs-readme`) |

## Rules

- It imports `plug`, `storeengine` (the SDK's handles of a database the store subsystem opened, `SQLOf` — ADR 0179 Q4: the queues depend on the stores) and the SDK, never kit's core: the core imports no `*kit` package (ADR 0179 §3), and kit's own suites plug it in through `framework/app/queue` (`framework/internal/app/subsystems_test.go`).
- The core encodes every message: a queue carries bytes, and nothing here names a message's type. The bytes at rest are unchanged — the same tables (`app_queue__…`, `kit_queue__…` before ADR 0180 step 5, which the store subsystem's `RenameTables` moves), the same file queues.
- A refusal of the SDK's queue reaches the core as the SDK's error, which the core wraps in its own codes (`CodeQueueOpen`, `CodeTopicPublish`, `CodeCommandQueue`, `CodeWatchQueue`, `CodeMailQueue`, `CodeDataCopy`); it never matches a queue sentinel itself — the one mark it reads, a delivery no retry helps, it asks through `plug.QueueDriverConfig`'s `NotRetryable`.

## Verify

```sh
cd framework && GOWORK=off go vet ./internal/app/queueengine/ && GOWORK=off go test -race ./internal/app/ ./app/ -run 'Queue|Topic|Subscri|Watch|Queued|Consumer|Mail|DataCopy|Initgraph|TheCore|TheSmallest|ImportsThe'
```
