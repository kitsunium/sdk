# ADR 0111 — a mail outbox retries on a backoff, dead-letters with its last failure, and never sends a delivered mail twice

- **Status**: Accepted
- **Date**: 2026-09-26
- **Deciders**: SDK maintainers
- **Related**: [ADR 0064](0064-sdk-mail-domain.md) (the mail domain it builds on), [ADR 0054](0054-sdk-queue-domain.md) (the queue it spools into), [ADR 0104](0104-an-idle-consumer-sleeps-until-there-may-be-work.md) (the wake its consumer sleeps on), [ADR 0103](0103-a-bucket-per-caller-one-backoff-curve-and-a-retry-on-the-clock-it-is-given.md) (the backoff curve), [ADR 0112](0112-a-loop-that-must-keep-running-is-supervised-beside-the-lifecycle.md) (what restarts its consumer)

## Context

A `mail.Transport` (ADR 0064) sends once and synchronously. A relay that is
down, slow or refusing makes that the caller's problem, in the middle of a
request. The framework built on this SDK (kitsunium/platform, kit) solved it in
`kit/mailer.go`: an outbox. `Send` validates and queues into an SDK queue (a
file queue when the app has a data directory, memory otherwise). A consumer
hands each mail to the transport and retries a failure with a backoff. The
mail is dead-lettered after its last attempt, a mail already delivered is never
sent again, and a capture transport stands in for SMTP in development. None of
that is kit's framework: a program that never imports kit and sends mail wants
all of it, unchanged.

The name is taken. `mail.Outbox` has been the capability sibling "what a
transport has been asked to send" since ADR 0064, so the outbox is a **spool**,
the word mail servers have always used for mail waiting to leave.

## Decision

### D1 — `internal/service/mail/spool`, over the queue domain

`spool.New(Config)` builds a spool over a durable queue (`queue.NewFile`) when
`Config.Dir` is set, and an in-memory one (`queue.NewMemory`) otherwise. A
spool is a queue whose records are mails. It lives beside `mail` and not
inside it, because it composes the queue domain and the plain transports do
not. Its own code range is `0.3.81.*`. The public facade is `pkg/v1/mail`
(`NewSpool`, `Spool`, `SpoolConfig`, …), so a caller sees one mail package.

### D2 — `Send` refuses early and stamps what a retry must keep

`Send` runs the mail domain's own `Validate`, so a header carrying a line
break, an unusable address, no recipient or no body is refused at the call
site, with the domain's typed verdict, before anything is queued. It fills in:

- the sender, from `Config.From`, when the mail names none;
- the `Date`, from the spool's clock;
- a **Message-ID**, `<id>@<the sender's domain>`, when the mail has none.

The composer mints no Message-ID, and that stays right for a single send
(RFC 6409 §8.2). A submission server that adds one per submission gives every
retry a different identity, though, and only the spool knows there will be
retries. The identifier is a ULID from `service/id`, or `Config.NewID`'s, and
the domain is the caller's own. An empty identifier is `SPOOL_MISCONFIGURED`
at `Send`, because the spool drops a mail whose identifier it delivered
already (D4). `Send` returns once the mail is in the queue, on disk with a
`Dir`.

### D3 — a retry PARKS the mail; only the last attempt nacks

The queue retries a nacked message after one fixed `RetryDelay`. A mail wants
a wait that grows with the attempt. So a failed attempt with attempts left
extends its lease by `Backoff.Delay(attempt)`, through the queue's
`LeaseExtender`, and the handler returns nil:

1. The extension replaced the receipt, so the consumer's acknowledgement of
   the old one is `LEASE_EXPIRED`, which `Consume` ignores, as it ignores
   every lapsed acknowledgement.
2. The queue hands the mail back when the new lease lapses.
3. Its delivery count has been incremented.

That count lives with the mail, survives a crash, and decides the dead letter.
The last attempt returns its error, and the nack dead-letters the mail WITH
that failure: reason, wire-safe cause, code. A lapsed lease would carry
`LEASE_EXPIRED` instead. The backoff's zero clamps to one second, doubling to
five minutes. `MaxAttempts` is REQUIRED, because its zero reads as "unlimited"
or as "none", two opposites (ADR 0031's refusal half).

### D4 — never sent twice by this process; the one duplicate left, named

Each attempt runs under `SendTimeout`, timed on the spool's clock. The lease
is twice that, so a lease can lapse mid-attempt only behind a relay that
ignores the timeout. Even then, the spool remembers the last `DeliveredMemory`
mails it delivered, by identifier, and a redelivery is dropped with a
`duplicate` event instead of being sent.

One duplicate is left, and no transport can remove it: a process that dies
between the relay's acceptance and the acknowledgement. The next process sends
the mail again under the same Message-ID, which is how a receiver recognises
it. SMTP has no idempotent submission. Persisting the ledger would not narrow
this: the acknowledgement follows the send at once, so the window is the same
size either way.

### D5 — what an attempt knows, and what the caller is told

- **The attempt, in its context.** Each attempt's context carries an
  `AttemptValue`: the identifier, the attempt number, the queue time, and the
  map `Config.Annotate` returned at `Send`. A framework uses it to continue
  the Send's trace in the delivery, or to name the attempt in a log.
- **Every mail's fate, to the observer.** `Config.Observe` receives an
  `EventValue` for each outcome: queued, sent, retrying (with when the next
  attempt is due), dead-lettered, duplicate. The event carries the mail
  itself, because a spool may deliver a mail an earlier process queued, and an
  observer building a mailbox has seen no `Send` for it.
- **Nothing written by the spool.** It logs nothing itself.
- **A panicking transport.** A panic inside the transport is recovered into
  `TRANSPORT_PANICKED`: the attempt fails like any other, and every other mail
  is untouched.

### D6 — `Run` is a function, not a goroutine the spool owns

`Run(ctx)` consumes until `ctx` ends, one mail at a time, on the caller's
goroutine, through the queue's `Consume`. It returns the queue's error when the
storage fails, because a spool whose directory stopped answering is not
something to poll in a loop. A caller supervises it: `lifecycle.NewSupervisor`
(ADR 0112) restarts it after a backoff.

### D7 — the capture transport

`mail.NewCapture(keep)` is `NewMemory`'s double, keeping only the last `keep`
deliveries, oldest dropped first. It composes every mail and refuses what SMTP
refuses. This is the transport a development server delivers through, where
`NewMemory` would keep every mail for as long as the server runs. A
non-positive `keep` clamps to `DefaultCaptureKeep` (200), a mailbox a person
can scroll.

## Consequences

- kit's `Mailer` becomes a `mail.Spool`, and its `smtpTransport`,
  `mailBackoff`, `deliveredMemory` ring, lease parking and panic recovery move
  out of kit. What kit keeps: its node, its spans (an `AttemptValue` in the
  transport's context carries the trace kit put in `Annotate`), its Studio
  mailbox (fed by `Observe`), and its `KIT_SMTP_URL` connector.
- The spool depends on one queue behaviour, now written down in both places.
  After an `Extend`, the old receipt's acknowledgement is `LEASE_EXPIRED`, and
  `Consume` ignores it. Both brokers answer that way, and the spool's tests
  run the path on both.

## Breaking changes

None. Everything here is new: the spool package and its codes `0.3.81.1`–`0.3.81.5`,
`NewCapture` and `DefaultCaptureKeep` in `service/mail`, and their facade names
in `pkg/v1/mail`.

## Alternatives considered

- **Call it `Outbox`.** Rejected: `mail.Outbox` has been a published sibling
  interface since ADR 0064. Another thing under the same name in the same
  package would be a trap.
- **Nack every failure and let the queue's `RetryDelay` pace retries.**
  Rejected (D3): the delay is fixed per queue, and a relay that is down would
  be retried at the same rate for as long as attempts last.
- **A `RetryAfter` error the consumer engine understands.** Considered,
  because it would make parking a feature of `Consume`. It was left aside
  because `Consume` does not know the broker's `MaxDeliveries`, so it could
  not tell a parked failure from the last one. That decision needs the
  attempt count, which is the spool's, and the spool has it.
- **Persist the delivered-ID ledger.** Rejected (D4): it narrows nothing an
  immediate acknowledgement does not already narrow.

## Deferred

- **Idempotent `Send`** ("the same Message-ID twice is one mail"). It would
  need an index of every queued and recent mail, durable across processes.
- **Parallel delivery.** One mail at a time keeps the ledger and the ordering
  trivial, and a spool is not a throughput tool.

## Verification

- `internal/service/mail/spool`:
  - a mail queued then delivered, with the sender, the Date, the Message-ID
    and the attempt in the context;
  - the three early refusals;
  - retries at 1 s then 2 s, not a nanosecond early, then a dead letter with
    its reason, cause and code, and one Message-ID across attempts;
  - a relay slower than the lease whose redelivery is dropped;
  - `SendTimeout` on the manual clock;
  - a panicking transport;
  - a mail that outlives its process and is delivered by the next one as its
    second attempt;
  - a record that is not a mail, dead-lettered;
  - the refusals and `SpoolClosed`.
- `internal/service/mail`: `NewCapture` keeps the newest, refuses what
  `NewMemory` refuses, and clamps its zero.
- `pkg/v1/mail`: the spool through public names.

## References

- `internal/service/mail/spool/`, `internal/service/mail/memory.go`
- kitsunium/platform `docs/adr/0001-the-sdk-holds-the-mechanisms-kit-is-the-framework.md`
  (the map, wave 3)
