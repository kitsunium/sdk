# ADR 0111 — a mail spool retries on a backoff, dead-letters with its last failure, and resends a delivered mail only after a crash

- **Status**: Accepted
- **Date**: 2026-09-26
- **Deciders**: SDK maintainers
- **Amended by**: [ADR 0141](0141-a-mail-spool-keeps-an-identifier-its-caller-minted.md) — D2: `SendWithID` queues under an identifier the caller minted, and every identifier, `Config.NewID`'s included, is a dot-atom of at most 255 bytes; D4: a repeated identifier meets the ledger; D5: the Sends of one identifier are counted; [ADR 0160](0160-every-service-has-a-core-and-a-code-keeps-its-value-when-it-moves.md) §As built — the spool is published by a facade of its own, `pkg/v1/app/mail/spool` (`New`, `Config`, `Event`… without the `Spool` prefix), and its codes are declared in `internal/core/app/mail/spool`; its own amendment of 2026-10-21 (below) — D3: a 5xx reply dead-letters the mail at once, `Config.RetryPermanent` retries it, and `Config.MaxAge` gives a mail up
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
`Dir`. `Close` waits for the publications in flight and none starts after it,
so a `Send` racing `Close` either lands or is `SPOOL_CLOSED`. Without that, a
publication cut off by the queue's closing could fail after its file was
already written, and the caller would send the mail again.

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
five minutes, and a curve without a `BaseDelay` starts from one second under
its own ceiling. `MaxAttempts` is REQUIRED, because its zero reads as "unlimited"
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
  observer building a mailbox has seen no `Send` for it. The calls are
  serialised, and a mail's `queued` event always comes before the events of
  its delivery, even when the consumer, woken by the publication, delivers the
  mail before `Send` returns. A delivery event waits for its mail's `queued`
  event, so an observer never sees a delivered mail go back to waiting.
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

## Consequences / Semantics

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
  - a curve without a base, whose first retry is still a second away;
  - retries at 1 s then 2 s, not a nanosecond early, then a dead letter with
    its reason, cause and code, and one Message-ID across attempts;
  - a relay slower than the lease whose redelivery is dropped;
  - `SendTimeout` on the manual clock;
  - a panicking transport;
  - a mail that outlives its process and is delivered by the next one as its
    second attempt;
  - a record that is not a mail, dead-lettered;
  - the refusals and `SpoolClosed`;
  - Sends racing `Close` over a directory, each landing or `SpoolClosed`,
    and a second `Close` that is nil.
- `internal/service/mail`: `NewCapture` keeps the newest, refuses what
  `NewMemory` refuses, and clamps its zero.
- `pkg/v1/mail`: the spool through public names.

## References

- `internal/service/mail/spool/`, `internal/service/mail/memory.go`
- kitsunium/platform `docs/adr/0001-the-sdk-holds-the-mechanisms-kit-is-the-framework.md`
  (the map, wave 3)

## Amendment (2026-10-21): a permanent refusal is not retried, and a mail can be too old

kitsunium/sdk#478. D3 had one way to fail: every failure parked the mail
until `MaxAttempts`. A relay that answered `550 5.1.1 no such user` was asked
again `MaxAttempts` times, which RFC 5321 §4.2.1 forbids — a 5yz reply means
the same command will fail the same way — and a mail had no age limit, where
§4.5.4.1 gives up after four to five days. D3 now reads:

- **A 5xx reply dead-letters the mail on the attempt that got it.** The
  spool reads a `*net/textproto.Error` whose `Code` is 500 to 599 anywhere in
  the failure's chain. The SMTP transport keeps it there for a refused MAIL,
  RCPT or DATA (`SEND_REFUSED` stays the origin, the reply its source, its
  text still a log-only field), and a connector may return one as it is. A
  5xx to the greeting, EHLO, STARTTLS or AUTH is NOT kept reachable: it
  speaks of the relay or of this program's configuration, and dead-lettering
  every mail for one wrong password would lose them all to it.
- **A 4xx reply, and every failure without a reply, stays temporary** —
  parked on the backoff as before.
- **`Config.RetryPermanent` is the escape hatch**: a relay known to answer
  5xx for a condition that passes gets its 5xx retried like a 4xx.
- **`Config.MaxAge`**: an attempt that fails once `MaxAge` has passed since
  `QueuedAt`, on the spool's clock, dead-letters the mail with that failure,
  whatever attempts are left. The age is read when an attempt fails, so a
  mail is attempted at least once however late it is delivered. Zero sets no
  age — the behaviour before this amendment (ADR 0031: the zero changes
  nothing); negative is `SPOOL_MISCONFIGURED`.

The early dead letter goes through the queue domain's own path: the handler
returns its failure marked `DoNotRetry` (ADR 0151), and `Consume` rejects it
through the broker's `Rejecter`, which all three brokers implement. Over a
`Config.Broker` without the sibling the spool does not try: a nack there
would hand the mail back after an `EventDeadLettered` the observer already
believed, so the mail is parked and retried as before and dead-lettered
after its last attempt — the shortcut is lost, never the mail. Two codes join the range, `MAIL_REJECTED` (`0.3.81.7`) and
`MAIL_EXPIRED` (`0.3.81.8`). Each is added to the failure as `DoNotRetry`
adds its own: an SDK failure keeps its Reason, Code and Public — the dead
letter still records the transport's verdict, `SEND_REFUSED` or
`DIAL_FAILED` — and gains the code in its trail, where `errs.HasCode` finds
it on the `EventDeadLettered`'s `Err`; a failure that is not an SDK error
takes the sentinel as its origin. `EventDeadLettered` now means "kept, never
delivered again" for all three reasons. The `MessageUndecodable` path is
unchanged.

Verified by `internal/service/app/mail/spool/permanent_external_test.go` (a
550 dead-lettered on attempt 1 with the reply reachable, a bare 554 from a
connector recorded as `MAIL_REJECTED`, a 450 retried, `RetryPermanent`, a
mail given up at `MaxAge` on the manual clock and none given up with a zero
`MaxAge`, a broker without `Rejecter` whose 5xx is retried) and `internal/service/app/mail`'s
`TestSMTPKeepsTheReplyOfARefusal` (the fake relay's 550 and 450 RCPT).
