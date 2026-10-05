<!-- updated: 2026-10-05T12:00:00Z -->
# pkg/v1/data/queue/

## Purpose

The public facade for the SDK's asynchronous, durable message queue (ADR 0054):
type aliases onto `internal/core/data/queue`, the three broker constructors — file,
SQL (ADR 0151) and memory — the SQL broker's migration, the `DoNotRetry` mark,
and the consumer engine.

## Surface

| Kind | Names |
|---|---|
| Ports | `Broker` (frozen at four methods), `Handler` (func port) |
| Values | `Message`, `Delivery`, `Lease`, `Receipt`, `Nack`, `Policy` (with `MaxRetryDelay`, ADR 0151), `DeadLetter` |
| ADR 0039 siblings | `DeadLetterReader`, `LeaseExtender`, `Waker` (+ its value `Wake`), `Rejecter`, `DeadLetterManager` — reached by type assertion |
| Configs | `FileConfig`, `SQLConfig`, `MemoryConfig`, `ConsumerConfig` |
| Constructors | `NewFile` (its broker also answers `io.Closer` by type assertion, releasing its two directory descriptors; the messages stay on disk), `NewSQL`, `NewMemory` |
| Functions | `SQLMigration` (the SQL broker's one table, a `sql.Migration` the caller numbers), `DoNotRetry` (a failure no retry can fix) |
| Engine | `Consume` |
| Constants | `DefaultMaxMessageBytes`, `MaxDeadlineOffset`, `DefaultPollInterval`, `MaxSQLTableLen`, `CodeNotRetryable`, `CodeDeadLetterNotFound`, `CodeSQLQueueMisconfigured` |
| Sentinels | `QueueMisconfigured`, `MessageTooLarge`, `UnknownReceipt`, `LeaseExpired`, `InvalidBatchSize`, `NotRetryable`, `DeadLetterNotFound`, `QueueBackendFailed`, `QueueDirectoryUnusable`, `SQLQueueMisconfigured`, `ConsumerMisconfigured`, `HandlerPanicked` |

## Why this shape

- **Everything is an alias.** `Broker`, `Delivery`, `Policy` and the rest are
  `=` aliases onto `internal/core/data/queue`, so a consumer's implementation of the
  port IS the internal one to the compiler and no adapter sits between them.
- **`README.md` is GENERATED** by `gomarkdoc` from `queue.go`'s package doc
  (rule 10, ADR 0008). Edit the doc comment and run `make docs-readme`; never
  hand-edit the README.
- **The package doc leads with the frontier table**, not with an example. This
  package and `pkg/v1/app/events` are described with the same words and guarantee
  opposite things, and a reader who picks the wrong one finds out in production.
  The table is the first thing after the synopsis for that reason.
- **The at-least-once guarantee is stated three times** — `Delivery.Deliveries`,
  `ConsumerConfig.HandlerIsIdempotent`, and "removed at acknowledgement, never
  at read" — because a reader who assumes exactly-once writes a handler that
  double-charges a card, and no amount of stating it once has ever been enough.

- **`PollInterval` is a bound, not a cadence, over a `Waker`** (ADR 0104).
  Every broker wakes an idle `Consume` on a Publish or a Nack in this process
  (the SQL broker once that transaction commits — ADR 0151) and at the
  instant a retry or a lapsed lease becomes due; the poll is left with another
  process's publication. The package doc shows a five-second
  poll for that reason, and the constant keeps its 100 ms so a zero means what
  it always meant for a broker that cannot wake anyone.

- **Three codes are published beside their sentinels** — the three ADR 0151
  added. `CodeNotRetryable` is the one a caller NEEDS: `DoNotRetry` puts it on
  the wrap trail of an SDK cause, whose own code stays the origin, so
  `errors.Is(err, NotRetryable)` does not see it and `errs.HasCode` does.
- **Every new name is built by a test with no internal import**
  (`sql_external_test.go`): `SQLConfig`, `NewSQL` over a transactor of the
  test's own making, `SQLMigration`, `DoNotRetry`, both new siblings on the
  memory and file brokers. Issue #259 was a public API a downstream module
  could not spell; this is the check that it can.

## Generated

`facade_gen.go` is kit's (ADR 0165): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/data/queue.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `facade_gen.go`: the hand-written files keep the package comment and the declarations of their own. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `facade_gen.go` edited by hand.

## Do NOT

- Add an `Enqueue`/`Emit` spelling beside `Publish`, or a `Subscribe` beside
  `Consume`. A second name for one verb costs every reader a choice.
- Re-export a `internal/service/data/queue` concrete type. `FileConfig`,
  `SQLConfig`, `MemoryConfig` and `ConsumerConfig` are aliases onto
  configuration STRUCTS,
  which is the same pattern `pkg/v1/app/lock` and `pkg/v1/security/session` use; the brokers
  themselves stay unexported behind their constructors.
- Hand-edit `README.md`.
- Soften the frontier table, the ordering warning, or the idempotence
  requirement into "usually" language. Every sentence in them was paid for by
  somebody's outage.

## Reference

- ADR 0054 — `docs/adr/0054-sdk-queue-domain.md`
- ADR 0151 — the SQL broker, the growing retry delay, `DoNotRetry`, `Rejecter`, `DeadLetterManager`
- ADR 0053 — `events`, the other column of the frontier table
- ADR 0008 — README generation from Go doc comments
- `internal/core/data/queue/CLAUDE.md` — the port and the five decisions
- `internal/service/data/queue/BENCH.md` — what durability costs
