# pkg/v1/queue/

## Purpose

The public facade for the SDK's asynchronous, durable message queue (ADR 0054):
type aliases onto `internal/core/queue`, the two broker constructors, and the
consumer engine.

## Surface

| Kind | Names |
|---|---|
| Ports | `Broker` (frozen at four methods), `Handler` (func port) |
| Values | `Message`, `Delivery`, `Lease`, `Receipt`, `Nack`, `Policy`, `DeadLetter` |
| ADR 0039 siblings | `DeadLetterReader`, `LeaseExtender` — reached by type assertion |
| Configs | `FileConfig`, `MemoryConfig`, `ConsumerConfig` |
| Constructors | `NewFile`, `NewMemory` |
| Engine | `Consume` |
| Constants | `DefaultMaxMessageBytes`, `DefaultPollInterval` |
| Sentinels | `QueueMisconfigured`, `MessageTooLarge`, `UnknownReceipt`, `LeaseExpired`, `InvalidBatchSize`, `QueueBackendFailed`, `QueueDirectoryUnusable`, `ConsumerMisconfigured`, `HandlerPanicked` |

## Why this shape

- **Everything is an alias.** `Broker`, `Delivery`, `Policy` and the rest are
  `=` aliases onto `internal/core/queue`, so a consumer's implementation of the
  port IS the internal one to the compiler and no adapter sits between them.
- **`README.md` is GENERATED** by `gomarkdoc` from `queue.go`'s package doc
  (rule 10, ADR 0008). Edit the doc comment and run `make docs-readme`; never
  hand-edit the README.
- **The package doc leads with the frontier table**, not with an example. This
  package and `pkg/v1/events` are described with the same words and guarantee
  opposite things, and a reader who picks the wrong one finds out in production.
  The table is the first thing after the synopsis for that reason.
- **The at-least-once guarantee is stated three times** — `Delivery.Deliveries`,
  `ConsumerConfig.HandlerIsIdempotent`, and "removed at acknowledgement, never
  at read" — because a reader who assumes exactly-once writes a handler that
  double-charges a card, and no amount of stating it once has ever been enough.

## Do NOT

- Add an `Enqueue`/`Emit` spelling beside `Publish`, or a `Subscribe` beside
  `Consume`. A second name for one verb costs every reader a choice.
- Re-export a `internal/service/queue` concrete type. `FileConfig`,
  `MemoryConfig` and `ConsumerConfig` are aliases onto configuration STRUCTS,
  which is the same pattern `pkg/v1/lock` and `pkg/v1/session` use; the brokers
  themselves stay unexported behind their constructors.
- Hand-edit `README.md`.
- Soften the frontier table, the ordering warning, or the idempotence
  requirement into "usually" language. Every sentence in them was paid for by
  somebody's outage.

## Reference

- ADR 0054 — `docs/adr/0054-sdk-queue-domain.md`
- ADR 0053 — `events`, the other column of the frontier table
- ADR 0008 — README generation from Go doc comments
- `internal/core/queue/CLAUDE.md` — the port and the five decisions
- `internal/service/queue/BENCH.md` — what durability costs
