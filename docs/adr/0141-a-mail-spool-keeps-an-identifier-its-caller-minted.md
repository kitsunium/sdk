# ADR 0141 — a mail spool keeps an identifier its caller minted

- **Status**: Accepted
- **Date**: 2026-09-27
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0111](0111-a-mail-spool-retries-on-a-backoff-and-resends-only-after-a-crash.md) — D2, where a mail's identifier comes from and the rule it keeps; D4, what a repeated identifier meets; D5, how the observer's order is kept
- **Related**: [ADR 0064](0064-sdk-mail-domain.md) (the mail domain and its dot-atom grammar), [ADR 0005](0005-sdk-error-codes-dotted-quad.md) (the code), kitsunium/sdk#254, kitsunium/platform ADR 0004 (the need)

## Context

kit holds the effects of a unit of work until its transaction commits
(kitsunium/platform ADR 0004, "Held effects"): a publish, a queued command, a
mail. Each effect is checked at the call, so a caller's mistake still returns
to the caller; it is released after the commit, or dropped by a rollback. A
held `Mailer.Send` returns the outbox ID its mail will keep, so kit needs the
identifier AT THE CALL — before the spool has the mail, which it only gets
after the commit.

ADR 0111's spool mints the identifier inside `Send`, with `Config.NewID`, a
function that takes no argument and no context. A caller that must know the
identifier first has no way to hand it in.

Two more facts shape the answer. The identifier is not only a key: `Send`
makes the mail's Message-ID `<id>@<the sender's domain>` of it, so it lands in
a header, whose left half RFC 5322 §3.6.4 writes as a `dot-atom-text`
(`id-left`), and which "MUST be a globally unique identifier for a message".
And the one check ADR 0111 put on an identifier was that it be non-empty: a
`Config.NewID` returning `mail 7` wrote `Message-ID: <mail 7@example.com>`,
which no receiver reads as one identifier.

## Decision

### D1 — `Spool.SendWithID(ctx, id, msg) error`

`SendWithID` queues `msg` under `id`. It stamps what `Send` stamps, makes the
Message-ID of `id` when the mail brings none, and every event, attempt and
dead letter carries `id`. It returns only an error: the caller holds the
identifier already. `Send` is unchanged — it mints with `Config.NewID`, a ULID
without one.

It is a method rather than:

- **a variadic option on `Send`**: `Send(ctx, msg, opts...)` changes the
  method's type, and every interface a consumer declared over `Send` stops
  being satisfied;
- **a value in the context**: a parameter nobody sees at the call site, for an
  argument the call cannot do without;
- **`Config.NewID(ctx)`**: a breaking change to a published field, and the
  caller would still smuggle its identifier to the generator through the
  context;
- **an empty identifier meaning "mint one"**: the zero would have two readings,
  and it is the one identifier the ledger must never see (ADR 0111 D2).

The name follows `NewEncoderWithBoundary` and `LoadWithOrigins`: the verb, and
what the caller adds to it.

### D2 — one rule for every identifier, whoever minted it

An identifier is non-empty, at most `MaxIDBytes` (255) bytes, and an RFC 5322
§3.2.3 `dot-atom-text`: runs of atext joined by single dots — printable ASCII,
no space, no special, no empty label. That is the grammar of a Message-ID's
left half, so `<id@domain>` is one identifier to every receiver. It cannot
carry a CR, an LF or a NUL, so it never ends a header. And it is ASCII, so it
comes back from the spool's JSON as the bytes it went in as, where
`encoding/json` would have replaced an invalid UTF-8 byte with U+FFFD and the
identifier the caller holds would name nothing. 255 bytes is well above every
identifier `service/id` mints — a TypeID under the longest prefix TypeID
allows is 90 — and `Message-ID: <id@domain>` stays inside RFC 5322 §2.1.1's
998-character line beside the longest domain an address may carry (255).

The grammar has one home. `core/mail` applied it, unexported, to both halves
of an addr-spec; it is exported as `IsDotAtom`, and the spool calls it.

The rule holds even for a mail that brings its own Message-ID: it belongs to
the identifier, and an identifier accepted with one mail and refused with the
next would be a trap.

- A caller's identifier that breaks it is `INVALID_MAIL_ID` (`0.3.81.6`,
  HTTP 400), and nothing is queued. The fields name the rule and the length,
  never the identifier: it may carry the very bytes it was refused for, a line
  break among them.
- `Config.NewID`'s is `SPOOL_MISCONFIGURED`, as an empty one already was: a
  generator that mints malformed identifiers is the configuration's defect,
  not the mail's.

### D3 — a repeated identifier meets the ledger; it is not refused

An identifier is its caller's promise that the mail is new, as one from
`Config.NewID` is, and RFC 5322 makes the Message-ID derived from it a MUST
of uniqueness. The spool does not look for the identifier among the mails it
holds: it cannot without reading every record in its queue, and ADR 0111
deferred an idempotent `Send` for exactly that reason. A repeated identifier
meets what every redelivery meets (ADR 0111 D4), the ledger of the last
`DeliveredMemory` identifiers this process delivered:

- repeated after its mail was delivered, the mail is queued, then dropped at
  delivery with `duplicate` instead of being sent;
- repeated while its mail is still queued, whichever the queue hands out first
  is sent and the other dropped;
- repeated after its mail was dead-lettered, it is attempted: that mail was
  never delivered, and its dead letter stays;
- repeated after a restart, it is sent again — under the same Message-ID when
  that was made of the identifier, the one duplicate ADR 0111 names, reached
  another way.

So a `SendWithID` retried after a failure that hid a landed publication sends
the mail once, while the process remembers it. Two DIFFERENT mails under one
identifier break the caller's promise, and only one of them is sent while the
ledger remembers the identifier. An observer keyed by identifier sees a
repeat's `queued`, then its `duplicate`, and reads the latter as "delivered
already".

### D4 — the Sends of one identifier are counted

ADR 0111 D5 keeps a mail's `queued` event before every event of its
delivery: a Send registers its identifier before it publishes, and a delivery
waits for the registration to end. A registration replaced any earlier one
under the same identifier, which was harmless while every identifier was new.
A repeated identifier can have two Sends in flight at once, and then the first
to tell the observer ended the only registration left and released a delivery
of the OTHER Send's mail, whose `sent` could reach the observer before its
`queued`. The registrations are now counted, and a delivery of an identifier
waits until none is left.

## Consequences

- kit can do what its ADR 0004 plans: mint the outbox ID at `Mailer.Send`
  (`kit.NewID("mail")`, a TypeID), return it, hold the mail with the
  transaction, and call `SendWithID` after the commit. The identifier returned
  at the call is the delivered mail's, in its events, its attempts and its
  Message-ID.
- `pkg/v1/mail` gains `InvalidMailID` and `SpoolMaxIDBytes`;
  `Spool.SendWithID` reaches consumers through the `Spool` alias.
- The facade's test spools one identifier from every generator `pkg/v1/id`
  offers, so a generator whose alphabet leaves the grammar fails a test.

## Breaking changes

One tightening, for `Config.NewID` only: a generator minting an identifier
longer than 255 bytes, or one that is not a dot-atom — which wrote a malformed
Message-ID header — is now `SPOOL_MISCONFIGURED` at `Send`, as an empty one
was. The default ULID and every generator in `service/id` pass. The new code,
`0.3.81.6`, is inside the spool's range.

## Alternatives considered

- **Refuse a repeated identifier at the call.** The spool knows the
  identifiers it delivered in this process and the Sends in flight; the ones
  waiting in its directory are inside records it would have to read. A
  refusal that fires for some repeats and not others is a guarantee nobody can
  state, and ADR 0111's Deferred entry stands.
- **A narrower alphabet** (`[A-Za-z0-9_-]`). It would refuse identifiers that
  are valid halves of a Message-ID — base64's `+`, `/` and `=` — for nothing:
  the grammar the identifier lands in is RFC 5322's.
- **Check the identifier only when it becomes the Message-ID.** The same
  identifier would be accepted with one mail and refused with another.
- **A copy of the grammar in the spool.** Two copies of one RFC production
  drift apart; the core guard had it already.

## Deferred

- **An idempotent `Send` across processes** (ADR 0111, Deferred). It still
  needs a durable index of the identifiers queued and recently delivered.

## Verification

- `internal/core/mail` — `TestIsDotAtom`: the shapes every SDK generator
  mints are in the grammar; a space, a line break, a NUL, a special, a
  non-ASCII letter, an invalid UTF-8 byte and an empty label are not.
- `internal/service/mail/spool`:
  - `TestACallerMintedIDIsTheMailsID` — the events, the attempt and the
    Message-ID carry the caller's identifier, and a mail's own Message-ID is
    kept;
  - `TestSendWithIDRefusesAnIDNoMailCanKeep` — twelve malformed identifiers
    are `INVALID_MAIL_ID`, nothing is queued, and no rendering or field quotes
    them; a 255-byte identifier is accepted;
  - `TestSendRefusesAnIdentifierNewIDGotWrong` — `Config.NewID` held to the
    same rule;
  - `TestARepeatedIDIsDeliveredOnce` — after delivery, and while still queued;
  - `TestADeadLetteredIDCarriesANewAttempt`;
  - `TestTwoSendsOfOneIDEachKeepTheirQueuedEventFirst` — fails against the
    registration that replaced, passes with the count;
  - `TestAClosedSpoolRefusesMail` — `SendWithID` too.
- `pkg/v1/mail` — `TestSendWithIDThroughTheFacade`: an identifier from every
  `pkg/v1/id` generator, a TypeID under the longest prefix among them;
  `errors.Is(err, mail.InvalidMailID)`; the bound.

## References

- `internal/service/mail/spool/identifier.go`, `internal/service/mail/spool/spool.go`,
  `internal/core/mail/address.go`
- RFC 5322 §2.1.1 (line length), §3.2.3 (atext, dot-atom-text), §3.6.4
  (msg-id, id-left, uniqueness)
- kitsunium/platform `docs/adr/0004-the-store-is-the-port-databases-are-adapters.md`
  ("Held effects", and the third SDK change it lists)
- kitsunium/sdk#254
