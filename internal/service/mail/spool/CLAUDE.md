# internal/service/mail/spool/

## Purpose

Outbound mail made durable (ADR 0111): `Send` validates a mail, stamps what a
retry must not change, and queues it; `Run` hands each mail to a
`core/mail.Transport`, retries a failure on a growing backoff, dead-letters a
mail after its last attempt with that failure, and drops a redelivery of a
mail it delivered — the one resend left is a crash between the relay's
acceptance and the acknowledgement, under the same Message-ID. The queue is `internal/service/queue` — `NewFile` with a
`Dir`, `NewMemory` without. Public facade: `pkg/v1/mail` (`NewSpool`).

Code range `0.3.81.*`.

## Contents

| File | Surface |
|---|---|
| `spool.go` | package doc, `Spool`, `New`, `Send` (`stamp`, `domainOf`), `Run` (the queue's `Consume`, one mail at a time), `DeadLetters`, `Close` |
| `config.go` | `Config`, `DefaultSendTimeout`, `DefaultRetryBase` / `DefaultRetryMax`, `DefaultMaxMessageBytes`, `DeliveredMemory`; the refusals |
| `deliver.go` | the queue handler: the record read back, a duplicate dropped, the attempt bounded on the spool's clock with its `AttemptValue` in the context, a transport panic recovered, a failure parked (`LeaseExtender.Extend` by `Backoff.Delay(attempt)`) or — on the last attempt — nacked into the dead letter |
| `ledger.go` | the delivered-ID ring (`DeliveredMemory` entries) |
| `event.go` | `EventKind` (`queued`, `sent`, `retrying`, `dead-lettered`, `duplicate`), `EventValue`, `AttemptValue` + `AttemptFrom`, `DeadLetterValue` |
| `codes.go` / `errors.go` | `SpoolMisconfigured`, `SpoolClosed`, `MessageUndecodable`, `MessageUnencodable`, `TransportPanicked` |

## Why-this-shape

- **A retry parks the mail; it does not nack it.** The queue's `RetryDelay`
  is one fixed delay per queue, and a mail wants a backoff that grows with the
  attempt. So a failure with attempts left EXTENDS the lease by
  `Backoff.Delay(attempt)` and the handler returns nil: `Extend` replaced the
  receipt, the consumer's acknowledgement of the old one is `LEASE_EXPIRED`,
  which `Consume` ignores as it ignores every lapsed acknowledgement, and the
  queue hands the mail back when the new lease lapses — its delivery count, the
  one that decides the dead letter and survives a crash, incremented. The last
  attempt returns its error: the nack dead-letters the mail WITH that failure
  (reason, wire-safe cause, code), which a lapsed lease would not carry. Both
  brokers answer `LEASE_EXPIRED` for a replaced receipt; `TestRetriesOnTheBackoffThenDeadLetters`
  and `TestAMailOutlivesItsProcess` run the path on both.
- **The Message-ID is minted at Send, not by the composer.** The mail domain
  mints none (RFC 6409 §8.2 leaves it to the submission server) — and a
  server that adds one per submission gives every retry a new one, so a
  receiver cannot tell a retry from a new mail. The spool knows there will be
  retries: `<id>@<the sender's domain>`, entropy from `service/id`, domain the
  caller's own.
- **The lease is twice `SendTimeout`, and the attempt is bounded on the
  spool's clock**, so a relay slower than the timeout is needed for a lease to
  lapse mid-attempt — and then the delivered-ID ledger drops the redelivery.
  The duplicate left is a process dying between the relay's acceptance and the
  acknowledgement; the next process sends the mail again under the same
  Message-ID. The ledger is not persisted: an acknowledgement follows a send
  at once, so a durable ledger would narrow nothing.
- **`Run` is a function that runs until its context ends**, so a caller
  supervises it — `lifecycle.NewSupervisor` restarts it after a storage
  failure — rather than the spool owning a goroutine it would have to stop.
- **Nothing is written anywhere by the spool.** `Observe` gets every event;
  logging is the caller's.
- **`Close` waits for the publications in flight.** Every publication holds
  `closing` for reading and `Close` holds it for writing, so a `Send` racing
  `Close` lands or is `SPOOL_CLOSED`. Before this, a Send cut off by the
  queue's closing answered `PUBLISH_FAILED`, or `DIRECTORY_SYNC_FAILED` for a
  mail whose file was already written, which invites a resend
  (`TestASendRacingCloseLandsOrIsRefused` fails without the lock). The lock is
  released before the observer is told. A second `Close` is nil.
- **The observer's calls are serialised, and a mail's `queued` comes first.**
  The consumer can deliver a mail before `Send` has returned, since `Publish`
  wakes it. `Send` therefore registers the mail's identifier in `announcing`
  before publishing and releases it after its `queued` event, and every
  delivery event goes through `report`, which waits for that release. A
  `queued` arriving after `sent` would leave an observer's mailbox showing a
  delivered mail as waiting. It happened on the linux/386 lane before this
  existed.

## Do NOT

- **Nack a failure that has attempts left.** It would wait the queue's fixed
  `RetryDelay`, not the backoff.
- **Mint the Message-ID in the composer**, or per attempt.
- **Put a secret in `Annotate`'s map**: it is written into the spool as is.
- **Log from here.**
- **Emit a delivery event with `emit`**: use `report`, which keeps the mail's
  `queued` first. And never hold `observing` while waiting for anything.

## Verification

```
bazel test --config=race //internal/service/mail/spool:spool_test
# OR
cd internal/service && GOWORK=off go test -race ./mail/spool
```

The durable cases (`disk_external_test.go`) need the file queue, which
Windows refuses by design (ADR 0056): they assert that refusal and skip there.
