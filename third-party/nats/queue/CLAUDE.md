<!-- updated: 2026-11-02T22:00:00Z -->
# third-party/nats/queue/

## Purpose

A `core/data/queue` `Broker` over a NATS JetStream stream and one durable pull
consumer (ADR 0054's deferred connector, ADR 0211, kitsunium/sdk#990), in the
`third-party/nats` vendor module. Kafka is not this port and belongs to
`data/stream`.

## Surface (design: `design/third-party/nats.yaml`)

`NewJetStream(Config) (queue.Broker, error)`; `Config` (`Conn`, `Clock`,
`Stream`, `Subject`, `DeadLetterSubject`, `Consumer`, `Policy`, `Timeout`,
`CreateStream`); `DefaultDuplicateWindow` (2 min); codes `0.3.97.*` —
`JetStreamMisconfigured`, `StreamMissing`, `ConsumerMissing`,
`ServerUnreachable`, `PublishUnacked`, `RequestFailed` (each with
`QUEUE_BACKEND_FAILED` in its trail). Siblings: `Waker`, `LeaseExtender`,
`Rejecter`, `DelayedNacker`, `OncePublisher`, `DeadLetterReader`.

## The mapping

| Verb | JetStream |
|---|---|
| `Publish` | a publication the stream acknowledges (PubAck); the stream sequence is the ID |
| `PublishOnce` | the same under `Nats-Msg-Id`; the window must be the stream's duplicate window |
| `Receive` | `FetchNoWait(max)`; `Deliveries` is `NumDelivered` |
| `Ack` | a double acknowledgement the server confirms |
| `Nack`, `NackAfter` | `NakWithDelay`, or on the last delivery the dead letter |
| `Reject` | the dead letter at once |
| `Extend` | `InProgress`, lasting the shorter of `by` and AckWait |
| `DeadLetters` | a direct get by subject from sequence 1, consuming nothing |

`CreateStream` creates a work-queue stream over both subjects and the durable
consumer filtered on `Subject`, AckWait the policy's visibility timeout and NO
MaxDeliver: the broker dead-letters, by publishing on `DeadLetterSubject` with
the cause in headers and terminating the message — two requests, a duplicate
on a crash between them, never a loss. A message fetched past `MaxDeliveries`
is dead-lettered by that `Receive`, `LEASE_EXPIRED`.

## Whose clock is the lease

The server's. The broker reads `Config.Clock` BEFORE each fetch, takes the
shorter of the visibility timeout and the consumer's AckWait, and refuses a
settlement at or past that deadline before anything is sent. The lease table
(`lease.go`, `nj1.<deadline>.<token>`) holds the delivery that acknowledges
it: a receipt is good with the broker that issued it.

## Contents

| File | Role |
|---|---|
| `config.go` | `Config`, its refusals, `DefaultDuplicateWindow` |
| `broker.go` | `jetStreamBroker`, `NewJetStream`, the stream and consumer lookup, `Publish`, `PublishOnce`, `Receive` |
| `settle.go` | `Ack`, `Nack`, `NackAfter`, `Reject`, `Extend` |
| `deadletter.go` | the dead letter's headers, `DeadLetters` |
| `lease.go`, `wake.go`, `cause.go`, `errors.go` | the lease table, the `Waker`, the cause, the wraps |
| `gen_codes.go`, `doc.go`, `gen_api_test.go` | written by kit gen from the design |

## Tests

- **Every lane** — `config_internal_test.go`: the refusals, the siblings, the
  error mapping and the record decoding, with no server.
- **Against nats-server, opt-in** — `e2e/integration/queue/nats`
  (`//go:build integration`, rule 12): the shared conformance rows on the wall
  clock, the race of two consumers over 1 000 messages, `PublishOnce`'s
  deduplication observed in the stream's state, and the missing stream. A
  `nats:2.11` container with `-js`, or `NATS_URL`; the server stays out of
  this module's `go.mod` (ADR 0157). No lane runs it until kitsunium/sdk#988.

## Do NOT

- Give the consumer a MaxDeliver: the server would stop delivering without
  the cause ever reaching a dead letter.
- Promise a renewal longer than AckWait.
