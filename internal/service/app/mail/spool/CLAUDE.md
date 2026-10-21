# internal/service/app/mail/spool/

## Purpose

Outbound mail made durable (ADR 0111): `Send` validates a mail, stamps what a
retry must not change, and queues it; `SendWithID` does the same under an
identifier its caller minted (ADR 0141); `Run` hands each mail to a
`core/app/mail.Transport`, retries a failure on a growing backoff, dead-letters a
mail after its last attempt with that failure — or at once for an SMTP 5xx
reply, and on the first failure once it is `Config.MaxAge` old (ADR 0111,
amended 2026-10-21) —, and drops a redelivery of a
mail it delivered — the one resend left is a crash between the relay's
acceptance and the acknowledgement, under the same Message-ID. The queue is `internal/service/data/queue` — `NewFile` with a
`Dir`, `NewMemory` without — or `Config.Broker`, one its caller built: a
queue on SQL joined to the caller's transaction, with `Config.Deferrer` its
transaction manager (ADR 0178). Public facade: `pkg/v1/app/mail/spool` (`New`).

Code range `0.3.81.*`, declared with its sentinels in
`internal/core/app/mail/spool` (ADR 0160). This package declares no code and
raises the core's.

## Contents

| File | Surface |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `spool.go` | `Spool`, `New`, `Send` (`mint`), `SendWithID`, both through `queue` (`stamp`, `domainOf`), the per-identifier `announcement` count, `holdAnnouncement` (a Send inside a transaction: `EventQueued` held until the commit, the registration ended by `OnAbort` on a rollback), `Run` (the queue's `Consume`, one mail at a time), `DeadLetters`, `Close` (a `Config.Broker` stays its caller's) |
| `identifier.go` | `MaxIDBytes` and `checkID` — the rule every identifier keeps, whoever minted it |
| `config.go` | `Config` (`Broker` and `Deferrer` since ADR 0178; `MaxAge` and `RetryPermanent` since ADR 0111's 2026-10-21 amendment, a negative `MaxAge` refused), `DefaultSendTimeout`, `DefaultRetryBase` / `DefaultRetryMax`, `DefaultMaxMessageBytes`, `DeliveredMemory`; the refusals (`validateBroker`: `Dir` or `MaxMessageBytes` beside a `Broker`, a `Deferrer` without one or that is not an `Aborter`) |
| `deliver.go` | the queue handler: the record read back, a duplicate dropped, the attempt bounded on the spool's clock with its `AttemptValue` in the context, a transport panic recovered, a failure parked for `Backoff.Delay(attempt)` (`DelayedNacker.NackAfter` where the queue has it — ADR 0183 —, `LeaseExtender.Extend` otherwise) or — on the last attempt — nacked into the dead letter; `giveUp` first: a failure carrying a 5xx `*textproto.Error` (`permanentReply`) is marked `MailRejected` unless `RetryPermanent`, one at or past `MaxAge` `MailExpired` (`mark`, origin-wins like `DoNotRetry`), and returned `corequeue.DoNotRetry`, which `Consume` turns into the broker's `Reject` |
| `ledger.go` | the delivered-ID ring (`DeliveredMemory` entries) |
| `event.go` | `EventKind` (`queued`, `sent`, `retrying`, `dead-lettered`, `duplicate`), `EventValue`, `AttemptValue` + `AttemptFrom`, `DeadLetterValue` |

## Why-this-shape

- **A retry parks the mail; it does not nack it on the queue's delay.** The
  queue's `RetryDelay` is one fixed delay per queue, and a mail wants a backoff
  that grows with the attempt. So a failure with attempts left is handed back
  for `Backoff.Delay(attempt)` and the handler returns nil. Since ADR 0183 that
  is `NackAfter` on a queue with the `DelayedNacker` sibling — all three SDK
  brokers, and the framework's sealed outbox — so the mail waits as a queued
  message, the attempt's failure handed over as the nack's cause; a queue
  without it EXTENDS the lease by the delay instead, as before. The mail comes
  back at the same instant with the same count either way; what differs is
  what `MessageReader` lists meanwhile (queued, not leased) and, on a caller's
  queue whose `MaxDeliveries` is below `MaxAttempts`, the cause a premature
  dead letter records (the failure, not `LEASE_EXPIRED`). Parking voided the
  receipt, the consumer's acknowledgement of the old one is `LEASE_EXPIRED`,
  which `Consume` ignores as it ignores every lapsed acknowledgement, and the
  queue hands the mail back when the backoff ends — its delivery count, the
  one that decides the dead letter and survives a crash, incremented. The last
  attempt returns its error: the nack dead-letters the mail WITH that failure
  (reason, wire-safe cause, code), which a lapsed lease would not carry. Both
  brokers answer `LEASE_EXPIRED` for a nacked or a replaced receipt; `TestRetriesOnTheBackoffThenDeadLetters`
  and `TestAMailOutlivesItsProcess` run the path on both.
- **A 5xx is not repeated, and a mail can be too old** (ADR 0111, amended
  2026-10-21, #478). RFC 5321 §4.2.1: a 5yz reply means the same command
  fails the same way, so a failure carrying a `*net/textproto.Error` with a
  Code from 500 to 599 is dead-lettered on the attempt that got it, through
  the queue's own early path — the handler returns it marked
  `core/data/queue.DoNotRetry`, and `Consume` rejects it through the
  broker's `Rejecter` (all three brokers have it; over a `Config.Broker`
  without it `giveUp` declines, because a nack would hand back a mail the
  observer was told was dead-lettered: it is parked as before and
  dead-letters after its last attempt). A 4xx and
  a failure without a reply park on the backoff as before, and so does a 552
  whose `command` field is `RCPT` (RFC 5321 §4.5.3.1.10: read as 452).
  `Config.RetryPermanent` retries a 5xx like a 4xx. `Config.MaxAge` gives
  a mail up on the first failure at or past `QueuedAt + MaxAge`, on the
  spool's clock, read when the attempt fails so a late mail is still tried
  once; zero is the behaviour before (ADR 0031). The marks (`MAIL_REJECTED`,
  `MAIL_EXPIRED`) are added as `DoNotRetry` adds its own: the transport's
  SDK verdict stays the origin the dead letter records, the mark joins the
  trail for `errs.HasCode`, and a foreign failure takes the mark as origin
  with itself as source, so `errors.As` still reaches the reply.
- **The Message-ID is minted at Send, not by the composer.** The mail domain
  mints none (RFC 6409 §8.2 leaves it to the submission server) — and a
  server that adds one per submission gives every retry a new one, so a
  receiver cannot tell a retry from a new mail. The spool knows there will be
  retries: `<id>@<the sender's domain>`, entropy from `service/app/id`, domain the
  caller's own.
- **A caller may mint the identifier, and it is held to one rule.**
  `SendWithID` exists for a caller that must return the identifier before the
  spool has the mail — kit holds a mail in a transaction and queues it after
  the commit (kit ADR 0004). It is a method and not an option of `Send`: a
  variadic `Send` changes the method's type under every interface a consumer
  declared over it, an identifier in the context is a parameter nobody sees,
  and an empty string meaning "mint one" would give the zero a second
  reading. Every identifier becomes the left half of a Message-ID, so every
  one — the caller's, and `Config.NewID`'s since ADR 0141 — is non-empty, at
  most `MaxIDBytes` and a dot-atom (`service/app/mail.IsDotAtom`, the grammar's one
  home). A caller's that breaks it is `INVALID_MAIL_ID`, which names the rule
  and never the identifier; a generator's is `SPOOL_MISCONFIGURED`.
- **A repeated identifier is not refused; the ledger meets it.** The spool
  cannot see the identifiers in its queue without reading every record, and
  ADR 0111 deferred an idempotent `Send` for exactly that. A mail under an
  identifier this process delivered — among the last `DeliveredMemory` — is
  dropped with `duplicate` after its own `queued`; a dead-lettered mail was
  never delivered, so its identifier carries a new attempt; after a restart a
  repeat is sent again. Two Sends of a repeated identifier can be in flight at
  once, which is why `announcing` counts them (below).
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
  wakes it. `Send` and `SendWithID` therefore register the mail's identifier
  in `announcing` before publishing and release it after its `queued` event, and every
  delivery event goes through `report`, which waits for that release. A
  `queued` arriving after `sent` would leave an observer's mailbox showing a
  delivered mail as waiting. It happened on the linux/386 lane before this
  existed. The registrations of one identifier are COUNTED, not replaced:
  when two Sends of one identifier are in flight, the first to tell the
  observer must not release a delivery of the other's mail
  (`TestTwoSendsOfOneIDEachKeepTheirQueuedEventFirst` fails with the
  replace-the-entry version).
- **In its caller's transaction, `queued` waits for the commit, and a
  rollback ends the registration in silence** (ADR 0178). With a `Broker` that
  joins the transaction, the mail exists only if it commits, and the commit
  wakes the consumer BEFORE any function the spool held runs (the SQL broker
  holds its wake first, ADR 0151 §D2) — so the registration must outlive
  `Send` and end after `queued`, told by a function held with
  `Deferrer.Defer`. Held with `Defer` alone, a rollback dropped that end
  without a word, the registration stayed open, and the next delivery of the
  identifier — a repeat ADR 0141 D3 allows, which a rollback makes legitimate —
  waited forever with every mail behind it on the one consumer. So the end is
  also held with `Aborter.OnAbort` (ADR 0139, amended 2026-10-14), behind a
  `sync.Once`: exactly one of the two runs. The abort half is held first, so a
  transaction that settles between the two calls has either run it or
  committed, and `Send` then announces as outside a transaction.
  `TestAMailQueuedInARolledBackTransactionNeverExists` stalls until its
  timeout with the abort half removed. `Deferrer` is one field, asserted to be
  an `Aborter`, as `queue.NewSQL` takes one `Transactor`: the two halves of one
  outcome cannot come from two managers.

## Do NOT

- **Nack a failure that has attempts left.** It would wait the queue's fixed
  `RetryDelay`, not the backoff.
- **Read a 5xx from the error's text**, or treat a failure without a
  `*textproto.Error` as permanent: a relay that is down is what retries are
  for. And never dead-letter a mail on `MaxAge` before it was attempted.
- **Mint the Message-ID in the composer**, or per attempt.
- **Read an empty identifier as "mint one"**, or exempt an identifier from the
  rule because its mail brings its own Message-ID: the rule is the
  identifier's, not one mail's.
- **Put the identifier in `INVALID_MAIL_ID`'s fields**: it may carry the very
  bytes it was refused for.
- **Replace an `announcing` entry** for a second Send of one identifier:
  count it.
- **Put a secret in `Annotate`'s map**: it is written into the spool as is.
- **Log from here.**
- **Emit a delivery event with `emit`**: use `report`, which keeps the mail's
  `queued` first. And never hold `observing` while waiting for anything.
- **End a transactional Send's registration on `Defer` alone**: a rollback
  drops it and stalls the consumer. Hold the end with `OnAbort` too.
- **Close a `Config.Broker`**, or set its policy: it is the caller's.

## Verification

```
bazel test --config=race //internal/service/app/mail/spool:spool_test
# OR
cd internal/service && GOWORK=off go test -race ./app/mail/spool
```

The durable cases (`disk_external_test.go`) need the file queue, which
Windows refuses by design (ADR 0056): they assert that refusal and skip there.
The transactional cases (`transaction_external_test.go`) run the SDK's real
transaction manager over a driver that accepts every statement, and a memory
broker whose publication is held with that manager's `Defer` — the SQL
broker's outcome and wake order, without its SQL.
