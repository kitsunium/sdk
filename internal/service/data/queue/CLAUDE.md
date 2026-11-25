# internal/service/data/queue/

The reference half of this file moved, unchanged and under the same section titles, to
`internal/service/data/queue/NOTES.md` (ADR 0253): read the section a change touches.
It holds:
§Contents; §Why the durable broker's state is a FILENAME; §On Windows: the directory rules read the DACL, and the broker is then refused; §The queue directory's rule, beside the other four; §A listing is reused only while its directory is "racily clean" (issue #811); §A Receive walks an index the kernel keeps where it names the changes (issue #811, 2026-11-14); §The SQL broker (ADR 0151); §Failures no retry can fix, the dead letters' two decisions (ADR 0151), and a dead letter written as it lies (ADR 0178); §Consume, and the one piece of ceremony; §A stop, a drain and a lease (kitsunium/sdk#474); §Waking an idle consumer (ADR 0104); §A retry after the handler's delay, a message for later, a publication once (ADR 0183); §The siblings that act on a whole queue, a message's lifetime, and a jittered retry (ADR 0054 and ADR 0151, amended 2026-11-02); §Priorities: five levels, strict (ADR 0054, amended 2026-11-03, issue #1045); §Tests worth knowing about.

## Purpose

The three brokers implementing `internal/core/data/queue` (ADR 0054) and the
consumer engine that drives a `Handler` against any of them: `NewFile` (durable,
its state a directory), `NewSQL` (durable, its state one table of the caller's
own database, every call on the transaction its context carries — ADR 0151),
`NewMemory` (the test double), and `Consume` (the pull loop that runs handlers
on goroutines it owns).

All three implement every capability sibling: `DeadLetterReader`,
`LeaseExtender`, `Waker`, since ADR 0151 `Rejecter` and
`DeadLetterManager`, since ADR 0178 `DeadLetterWriter`, `MessageReader` and `MessageWriter`, since kitsunium/sdk#474 `Releaser`, since ADR 0183 `DelayedNacker`, `DelayedPublisher` and `OncePublisher`, and since ADR 0054 and ADR 0151's 2026-11-02 amendment `MessageLister`, `Counter`, `Purger`, `DeadLetterPager`, `DeadLetterBulkManager`, `BatchPublisher` and `Acker`, and since its 2026-11-03 amendment `PriorityPublisher`; all three wait the same growing retry delay when the
policy's `MaxRetryDelay` asks for one, jittered from the same injected draw
(`retry.go`), dead-letter a message past `MaxAge` and drop a dead letter past
`DeadLetterRetention` at the same Receive (`lifetime.go`).

Code range: `0.3.53.*` (ADR 0054) — `QUEUE_BACKEND_FAILED`,
`QUEUE_DIRECTORY_UNUSABLE`, `CONSUMER_MISCONFIGURED`, `HANDLER_PANICKED` and
`SQL_QUEUE_MISCONFIGURED`, declared since ADR 0160 with the port's `0.2.23.*` in
`internal/core/data/queue`, under the values this package allocated. This
package declares no code.

## Where `internal/service/data/vfs` is used, and where it stops

`vfs.AtomicWriter.WriteAtomic` does the two writes that must be atomic AND
durable: the enqueue, and the dead-letter record. That is exactly ADR 0056's
five steps — temporary in the same directory, write, flush the FILE, rename,
flush the DIRECTORY — with failure paths that were tested by injecting a
failure at each one and comparing the destination's hash. Rewriting that here
would duplicate subtle code, and the first thing to rot would be the failure
paths.

It stops at `corevfs.WritableFS`, which has `WriteFile`, `MkdirAll`, `Remove`
and `RemoveAll` and **no `Rename`**. That is not worked around: the port is
FROZEN (ADR 0039), so a fifth method to carry this domain's state machine would
break every downstream implementation at compile time. The transitions go
through `os.Root` directly — the same confinement mechanism `vfs` itself uses
— and the boundary between the two is the boundary between "publish a whole
file" and "move one".

## Where the flushes are, and where they deliberately are not

`Publish` flushes. Nothing else does, and that is a decision rather than an
omission: every non-durable outcome of a receive, an ack or a nack degrades
into a REDELIVERY, which at-least-once already permits and every consumer is
already obliged to survive. Paying a device round trip to make a duplicate
slightly less likely would buy nothing the contract does not already give away.

`BENCH.md` says what the one flush costs: **3.3 ms of a 3.3 ms round trip** —
the enqueue IS the round trip, to within the noise — and it is independent of
the payload size across a 1 024× range.

## Where this package departs from `service/app/events`, and why

`events` joins its `ListenerFailed` verdict beside the listener's cause,
because `Publish` RETURNS the aggregate and a caller has to be able to ask "did
anything fail?" without knowing every code every listener might produce.

Here the aggregate's destination is a DEAD-LETTER RECORD, which already says "a
handler failed" by existing and has structured fields for the rest. So a
handler's error travels to `Nack` unmodified, there is deliberately no
`HandlerFailed` sentinel, and `TestAFailingHandlerIsRetriedAndThenDeadLettered`
asserts that the HANDLER'S own reason is what reaches the store. A verdict
wrapped or joined around the cause would only displace it.

A handler PANIC is different and does get a sentinel: `HandlerPanicked`,
recovered on the worker's goroutine with the originating stack, with the
recovered value travelling as a FIELD so a `panic(someSentinel)` cannot hijack
the code.

## A handler that parks its own message

A handler may extend its OWN lease through `LeaseExtender` and return nil — the
mail spool (`internal/service/app/mail/spool`, ADR 0111) does exactly that to wait
a backoff that grows with the attempt instead of the fixed `RetryDelay`. The
extension replaces the receipt, so `Consume`'s acknowledgement of the old one
is refused `LEASE_EXPIRED` by EVERY broker and ignored by `settle` like every
lapsed acknowledgement; the message comes back when the new lease lapses, its
delivery count incremented. That is a contract now: a broker that answered
`UNKNOWN_RECEIPT` for a replaced receipt would stop `Consume` instead. Such a
handler leaves `AutoExtend` false: the engine's renewal replaces the receipt
the handler holds, and its own `Extend` would then be refused.

## Do NOT

- Give `Close` any duty beyond releasing descriptors. The durable broker holds
  TWO directory descriptors — its own `os.Root` and the one inside its
  `service/data/vfs` publisher (this rule used to say one, and to refuse a `Close`
  on that basis) — and nothing about the queue's contents lives in this
  process, which is what makes it genuinely inter-process. So `Close` is an
  `io.Closer` reached by type assertion, as the session file store's is: it
  releases both descriptors, flushes nothing and owns nothing, and every call
  after it fails. `os.Root` would release them at the next collection anyway;
  `TestClosingTheDurableBrokerReleasesBothDescriptors` holds the collector off
  to prove `Close` does it without one.
- Flush on `Ack`, `Nack` or `Receive`. See above; it buys nothing.
- Add a sweeper goroutine, a timer, or a background reclaim. Expiry is noticed
  by whoever looks next, in every broker, deliberately. `Wake` does not change
  that: it tells the consumer WHEN to look, and the look is still a `Receive`.
- Render SQL outside `sql_dialect.go`, interpolate anything but the validated
  table name, or read the database's `NOW()`: every instant is the broker's
  clock. Spell no marker, quote or row lock by hand there either: they are
  `core/data/sql.Dialect`'s, which this file used to copy.
- Parse a driver's error, or let its text into a rendering — join it through
  `service/data/sql`'s `Withheld`. The service layer imports no driver (ADR 0055
  §D2).
- Fire the SQL broker's wake before the transaction a publication joined has
  committed: `announce` goes through `Defer`, and a wake for a rolled-back
  message would find nothing.
- Make the SQL broker's idle `Receive` open a transaction. The probe is one
  read and no lock — on SQLite, none of the one write lock every writer of the
  application shares.
- Send on the wake channel instead of closing it, or close it before the
  state it announces is visible. A send wakes one worker or none; a close
  before `Nack`'s rename wakes a worker into a directory that does not show
  the message yet.
- Take a lock in the file broker. `rename(2)` is the exclusion, and ADR 0052
  measured why a `flock` would not be.
- Reuse a state directory's listing on anything but an unchanged mtime older
  than the listing by `racyMargin`, or past `listingMaxAge`, and never read
  the instants it is judged on from the broker's clock: the mtime is stamped
  from the real one. Reusing it after this broker's own rename would miss a
  publication another process made in the same tick (issue #811).
- Optimise `Publish/file`'s 41 allocations. They sit next to 3 ms of `fsync`;
  see `BENCH.md`.
- Range over the in-flight map in `memory.go` looking for expiries. That was
  the original code and it measured 105 µs per lease; the `kernel/collections/heap` min-heap
  that replaced it measures 1.83 µs. `BENCH.md` has both numbers.
- Leave a lease's expiry entry behind when the lease ends or is extended.
  Every path out of flight goes through `unlease` and `Extend` re-keys with
  `Update`; a stale entry would grow the heap by one per extension (#1036).

## Reference

- ADR 0054 — `docs/adr/0054-sdk-queue-domain.md`
- ADR 0053 §D1 — the frontier this domain is the right-hand column of
- ADR 0056 — `vfs`, whose `WriteAtomic` is this package's durable publish
- ADR 0052 — `lock`, whose `flock(2)` measurement is why there is no lock here
- ADR 0104 — the `Waker` an idle consumer sleeps on
- ADR 0139 — docstore over SQL, whose transaction rules the SQL broker follows
- ADR 0151 — the SQL broker, the growing retry delay, `Rejecter`, `DeadLetterManager`
- ADR 0178 — `DeadLetterWriter`, `MessageReader` and `MessageWriter`, for kit's `data copy`
- kitsunium/sdk#474 — `Releaser`, the drain, the bound on each outcome, the handler's lease bound and `AutoExtend`
- ADR 0183 — `DelayedNacker` and `RetryAfter`, `DelayedPublisher`, `OncePublisher`; the frontier with the scheduler
- `internal/core/data/queue/CLAUDE.md` — the port and the five decisions
- `e2e/integration/sql/CLAUDE.md` — the SQL broker on the three real engines
- `BENCH.md` — the cost of durability, and the two defects the numbers found
