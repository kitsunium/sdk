<!-- updated: 2026-11-02T22:00:00Z -->
# third-party/aws/queue/sqs/

## Purpose

A `core/data/queue` `Broker` over Amazon SQS (ADR 0054's deferred connector,
ADR 0211, kitsunium/sdk#1130), in the `third-party/aws` module beside the
writers: the one connector ADR 0054 names whose vendor module already existed.

## Surface (design: `design/third-party/aws.yaml`)

`New(Config) (queue.Broker, error)`; `Config` (`QueueURL`,
`DeadLetterQueueURL`, `Region`, `Endpoint`, `Credentials`, `Clock`, `Policy`,
`WaitTime`); `MaxReceiveBatch` (10), `MaxWaitTime` (20 s), `MaxPublishDelay`
(15 min), `MaxVisibility` (12 h), `MaxPayloadBytes` (191 KiB); codes
`0.3.101.*` — `SQSMisconfigured`, `SendFailed`, `ReceiveFailed`, `AckFailed`,
`LeaseLost`, `DelayTooLong`, `BatchTooLarge`.

## The mapping

| Verb | SQS |
|---|---|
| `Publish` | `SendMessage`: the payload base64 in the body, the enqueue instant an attribute |
| `PublishAt` (`DelayedPublisher`) | `SendMessage` with `DelaySeconds`, up to `MaxPublishDelay` |
| `Receive` | one `ReceiveMessage`, `VisibilityTimeout` the policy's rounded up, `WaitTimeSeconds` `Config.WaitTime`; `Deliveries` is `ApproximateReceiveCount` |
| `Ack` | `DeleteMessage` |
| `Nack`, `NackAfter` (`DelayedNacker`) | `ChangeMessageVisibility`, or on the last delivery the dead letter |
| `Reject` (`Rejecter`) | the dead letter at once |
| `Extend` (`LeaseExtender`) | `ChangeMessageVisibility` |
| `DeadLetters` (`DeadLetterReader`) | `ReceiveMessage` on the dead-letter queue with a zero visibility timeout |

The dead letter is a `SendMessage` to `DeadLetterQueueURL` carrying the cause
in attributes, then a `DeleteMessage`: two requests, a duplicate dead letter on
a crash between them, never a loss. A message received past `MaxDeliveries`
— its leases all lapsed — is dead-lettered by that `Receive`, `LEASE_EXPIRED`.

## Whose clock is the lease

SQS's. The broker reads `Config.Clock` BEFORE each `ReceiveMessage`, so its
deadline never falls after SQS's, writes it in the receipt, and refuses a
settlement at or past it with `LEASE_EXPIRED` (`LEASE_LOST` in the trail)
before anything is sent — a `DeleteMessage` with an old handle may succeed on
SQS. The lease table (`lease.go`) holds the receipt handle and the payload a
dead letter needs: a receipt is good with the broker that issued it.

## Contents

| File | Role |
|---|---|
| `config.go` | `Config`, its refusals, the SQS ceilings |
| `client.go`, `cred_adapter.go` | `queueAPI`, the four SQS calls the tests double; the credential bridge |
| `broker.go` | `sqsBroker`, `New`, `Publish`, `PublishAt`, `Receive` |
| `settle.go` | `Ack`, `Nack`, `NackAfter`, `Reject`, `Extend`, the retry curve |
| `deadletter.go` | the dead letter's attributes, `DeadLetters` |
| `lease.go` | the lease table and the receipt (`sqs1.<deadline>.<token>`) |
| `encoding.go`, `cause.go`, `errors.go` | the body and attributes, the cause a dead letter keeps, the wraps |
| `gen_codes.go`, `doc.go`, `gen_api_test.go` | written by kit gen from the design |

## Tests

- **Every lane** — `conformance_internal_test.go` restates the rows of
  `internal/service/data/queue`'s conformance table that SQS can keep and runs
  them over `fakesqs_internal_test.go`, an in-memory SQS on a `ManualClock`
  (visibility, receive counts, one current handle per message);
  `config_internal_test.go` the refusals, the error mapping and the receipts.
- **LocalStack, opt-in** — `localstack_test.go`, `//go:build localstack`
  (rule 12): the same rows on the wall clock against the emulator (ElasticMQ
  answers the same requests). `cd third-party/aws && GOWORK=off go test -tags
  localstack -race ./queue/sqs/...`. No lane runs it until kitsunium/sdk#988.

## Do NOT

- Let SQS's redrive policy dead-letter first: allow it more receives than
  `MaxDeliveries`, or the cause is lost.
- Settle a lease without checking its deadline on the broker's clock.
