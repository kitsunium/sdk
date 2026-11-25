# internal/core/data/queue/ — reference

Moved unchanged from `internal/core/data/queue/CLAUDE.md` (ADR 0253); each section keeps its title.

## What ADR 0151, ADR 0178, kitsunium/sdk#474 and ADR 0183 added, and why each piece is where it is

- **`MaxRetryDelay` makes the retry delay grow.** Positive, the message nacked
  on its n-th delivery waits `RetryDelay × 2^(n−1)`, held at the ceiling —
  the `kernel/backoff` curve (ADR 0103, published as `resilience.Backoff`),
  which the brokers call; this package only holds the numbers. Zero keeps the constant delay
  every policy had, which is the zero's one reading and what keeps the field
  backward compatible. A ceiling that is no ceiling is refused by field and
  problem: negative, past `MaxDeadlineOffset`, above no `RetryDelay` (growth
  from zero is zero forever, an inert knob), or below `RetryDelay` (it would
  silently shorten every wait). `TestAGrowingRetryDelayNeedsARealCeiling`.
- **`DoNotRetry(cause)` is the handler's half of immediate dead-lettering.** It
  is an `errs.Wrap` onto the cause with `CodeNotRetryable` as the wrap-site
  code, so ORIGIN WINS: an SDK cause keeps its own Reason, Code and Public — the
  three things a dead letter records, D10 of ADR 0054 — and the mark rides the
  wrap trail, where `errs.HasCode` finds it. A stdlib cause, or none, records
  `NOT_RETRYABLE` itself. `errors.Is(err, NotRetryable)` sees only the origin,
  which is why the recognition is by code and the doc says so.
  `TestDoNotRetryMarksTheFailureAndKeepsTheCauseItsOrigin`.
- **`Rejecter` is the broker's half**, a sibling because `Broker` is frozen and
  a connector with no in-band dead-letter move cannot implement it — the engine
  falls back to `Nack` then, which loses the shortcut and never the message.
  It lives in core because a type the port speaks lives in core (ADR 0074).
- **`Releaser` hands a leased message back without counting it (kitsunium/sdk#474).**
  A consumer that never ran a message — a worker stopped in the middle of its
  batch, a message whose lease ran down before its turn — has nothing to
  report as a failure, and a `Nack` would charge it a retry delay and a
  delivery, moving it toward the dead-letter store with every deployment.
  `Release` makes it receivable at once, its next delivery carrying the count
  this one did, never dead-lettered; it wakes an idle consumer and refuses a
  lapsed or unknown receipt as `Ack` does. A sibling because `Broker` is
  frozen and a connector whose delivery count cannot be given back cannot
  implement it — `Consume` nacks then, which loses the courtesy and never the
  message.
- **`DeadLetterManager` replays or deletes one dead letter by `MessageValue.ID`.**
  Reading the store stays evidence that no read consumes; these two are the
  decisions an operator takes after it. A replay keeps the ID, the payload and
  the enqueue instant, resets the count to zero and loses the cause record;
  an ID the store does not hold is `DEAD_LETTER_NOT_FOUND` (404, `EX_NOINPUT`).
- **`DeadLetterWriter` writes a dead letter as it lies (ADR 0178).** A copy of
  a queue from one broker to another — kit's `data copy`, a file queue into a
  table — must carry each dead letter with what it recorded, and no other verb
  can: `Rejecter` buries a message this broker leased, at the count it reached
  here and with a cause that is an `*errs.Error` (whose `Define` refuses code
  0), so a death by expired leases could only be re-enacted. `PutDeadLetter`
  keeps the payload, both instants, the count, the reason, the cause, the
  code — 0 included — and the trail — but on an SQL broker without
  `SQLConfig.Trail`, which keeps none (issue #500) — and returns the
  identifier the broker mints; `Message.ID` must be empty. A value no store
  keeps as given is `INVALID_DEAD_LETTER` (`0.2.23.8`, `EX_DATAERR`), its
  `field` field naming which: an identifier set, a count below 1 or above
  2³¹−1, an instant outside 1970–2262, a reason or cause over 120 runes or
  with a line break, a trail over sixteen codes — refused rather than cut,
  because a copy must not change what it copies.
- **`MessageReader` and `MessageWriter` move a live message as it lies (ADR
  0178).** The same copy must carry the messages still waiting — and those a
  consumer leased when the product stopped — with their delivery count:
  `Receive` cannot read one without leasing it and counting a delivery, nor
  read a leased one, and `Publish` counts none and stamps its own enqueue
  instant. `Messages(ctx, after, max)` lists the queued and the leased ones
  in the byte order of their identifiers, resuming after `after`, as a
  `LiveMessageValue` — payload, enqueue instant, `Deliveries`, `Leased`, and
  `VisibleAt`, the visibility of a queued one or the deadline of a leased one
  — and leases, counts and reclaims nothing. `PutMessage(ctx, live)` writes
  one as a queued message visible at `VisibleAt`, its count kept, under an
  identifier the broker mints; `Message.ID` must be empty and `Leased` false
  (a lease is the broker's that granted it). `INVALID_MESSAGE` (`0.2.23.9`,
  `EX_DATAERR`) names the field otherwise, as `INVALID_DEAD_LETTER` does. Two
  ports and not one `PutMessage` covering both states: a dead letter's
  failure — instant, reason, cause, code — and a live message's visibility
  are different records, and one value carrying both would leave half its
  fields meaningless in each state.
- **ADR 0183 adds a nack after the caller's delay, a message for later, and a
  publication once per key.** `DelayedNacker.NackAfter(ctx, receipt, cause,
  delay)` is `Nack` with the caller's delay instead of the policy's — counted,
  and dead-lettered with `cause` on the last attempt, which is why it takes
  one; a negative delay is now, one no durable broker records is
  `QUEUE_MISCONFIGURED`. `RetryAfter(cause, delay)` is the handler's half,
  built as `DoNotRetry` is: an `errs.Wrap` whose wrap-site code is
  `CodeRetryRequested` (`0.2.23.10`), the delay a `retry_after_ns` field held to
  `[0, MaxDeadlineOffset]`, `RETRY_REQUESTED` (`EX_TEMPFAIL`) the origin of a
  foreign or nil cause; `RetryAfterOf(err)` reads it back, the outermost mark
  winning. `DelayedPublisher.PublishAt(ctx, payload, at)` holds one message
  until one instant — a past one is now, one past 2262 refused — and the ADR
  draws the frontier with `scheduler` (ADR 0041): one instant, durable, here;
  recurrence there. `OncePublisher.PublishOnce(ctx, key, payload, window)`
  publishes and reserves `key` for `window`, or publishes nothing and returns
  `false` — a boolean, never a sentinel, since a duplicate is what the retry was
  looking for; an empty key and a non-positive window are refused, their zeros
  having opposite readings. Three siblings because `Broker` is frozen and a
  connector with a fixed redelivery delay, no delayed delivery or no
  deduplication cannot implement one; `Consume` nacks when the first is absent.
  `TestRetryAfterMarksTheFailureWithItsDelayAndKeepsTheCauseItsOrigin`,
  `TestRetryAfterReadsItsDelayBack`.
- **ADR 0054 and ADR 0151, amended 2026-11-02, add the siblings that act on a
  whole queue, a message's lifetime and a jittered retry.** Seven siblings,
  each frozen at what it shipped with: `MessageLister.AllMessages(ctx, after)`
  — `Messages` as an `iter.Seq2`, the error yielded once (issue #816);
  `Counter.Stats(ctx)` — `StatsValue{Queued, Leased, Dead, OldestEnqueuedAt,
  NextDueAt}`, no payload read (issue #833); `Purger.Purge(ctx)` — every live
  message, never a dead letter, `PurgeValue{Queued, Leased}` (issue #835);
  `DeadLetterPager.DeadLettersAfter(ctx, after, max)` — and with it ONE order
  for the dead-letter store on every broker, the byte order of the
  identifiers, which every broker mints from the enqueue instant, where the
  SQL broker listed by failure instant and the memory broker by burial (issue
  #1043); `DeadLetterBulkManager.ReplayDeadLetters(ctx, max)` and
  `PurgeDeadLetters(ctx, before, max)`, a partial failure returning the count
  done (issue #1120); `BatchPublisher.PublishMany(ctx, payloads)` — every
  payload checked before any is written (issue #827); `Acker.AckMany(ctx,
  receipts)` — the `AckAll` ADR 0054 deferred, its partial-failure semantic
  the result: one `AckOutcome` per receipt, `AckNotAttempted` the zero for one
  a failure kept the broker from reaching (issue #1152). Four policy fields,
  every zero the behaviour before it: `MaxAge` — a message older is never
  delivered and is dead-lettered `MESSAGE_EXPIRED` (`0.2.23.11`,
  `EX_TEMPFAIL`) at the Receive that reaches it (issue #835);
  `DeadLetterRetention` — a dead letter older is removed by a Receive, at
  most one sweep a minute (issue #1120); `RetryJitter` and `RetryJitterMode`
  — `kernel/backoff`'s three modes on the growing delay, refused without a
  `MaxRetryDelay`, a fraction outside [0, 1] or beside a mode that ignores it
  (issue #1044). And `Acknowledged` (`0.2.23.12`): what a handler returns
  once it acknowledged its delivery in its own transaction — the inbox half
  of ADR 0151's outbox (issue #1046).
  `TestALifetimeIsRefusedNegativeOrPastTheCeiling`,
  `TestARetryJitterNeedsAGrowingDelayAndAFractionInRange`,
  `TestTheTwelveCodesOfTheBacklogAmendmentAreTheNextSerials`.
- **Priorities, a closed set (ADR 0054, amended 2026-11-03, issue #1045).**
  `Priority` (`int8`) and its five levels `PriorityLowest` (−2) to
  `PriorityHighest` (2), the zero `PriorityNormal` — what `Publish` publishes
  at and every message written before the amendment reads as;
  `PriorityPublisher.PublishPriority(ctx, payload, level)`, a level outside the
  set `QUEUE_MISCONFIGURED`; `LiveMessageValue.Priority` and
  `DeadLetterValue.Priority`, so a copy and a replay keep the level. The order
  is strict and ages nothing: a steady higher level starves a lower one, the
  stated non-guarantee. `TestThePrioritiesAreAClosedSetAroundTheZero`.
- **`TestTheQueueSiblingsKeepTheirMethodCounts`** pins `Broker` at four methods
  and each sibling at the count it shipped with.
