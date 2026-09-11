# ADR 0054 — asynchronous durable message queue domain (`queue`), the right-hand column of ADR 0053's frontier

- **Status**: Accepted
- **Date**: 2026-09-10
- **Deciders**: SDK maintainers
- **Related**: [ADR 0053](0053-sdk-events-domain.md) (the frontier table, written before this domain existed), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (a zero value is a safe default or an explicit refusal), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (a published port grows by siblings), [ADR 0052](0052-sdk-lock-domain.md) (leases that expire, and the `flock(2)` measurement), [ADR 0056](0056-sdk-vfs-domain.md) (`vfs`, whose `WriteAtomic` is this domain's durable publish), [ADR 0018](0018-sdk-cross-platform-portability.md) (refuse rather than approximate), [ADR 0001](0001-sdk-go-multimodule-layout.md) / [ADR 0005](0005-sdk-error-codes-dotted-quad.md)

## Context

ADR 0053 drew this domain's boundary before a line of it was written, and it
said why:

> An in-process event bus and a message queue are described with the same words
> — publish, subscribe, listener, event, handler — and they guarantee opposite
> things. […] The library that "supports both" reliably guarantees neither.

It then wrote the table, and named the sentence this domain is the destination
of: **"a caller who wants 'asynchronous but reliable' is asking for a queue and
must be sent to one"**. Sending them somewhere means the somewhere has to
exist.

### The frontier, and how this domain respects it

| | `events` (ADR 0053) | `queue` (this ADR) |
|---|---|---|
| Scope | one process | **many processes** |
| Timing | synchronous | **asynchronous** |
| Goroutine | the publisher's | **a consumer's** |
| Transaction | the publisher's | **its own** |
| Durability | none | **the point of it** |
| Retries / DLQ | none | **the point of it** |
| Process dies mid-flight | the event never happened | **the message is still there** |

Every axis is honoured by a mechanism rather than by an intention:

- **Many processes.** `NewFile`'s entire state is a directory. Two brokers over
  one directory, in one process or in twenty, are one queue. Nothing about the
  queue's contents lives in a process's heap.
- **Asynchronous, on a consumer's goroutine.** `Publish` returns when the
  message is on the device; the handler runs later, on a goroutine `Consume`
  owns, usually in another process.
- **Durable.** `Publish` flushes the file and the directory entry (§D7).
- **Retries and a dead-letter path.** §D3 and §D4.
- **A process that dies mid-flight loses nothing.** §D2 — and this is the one
  claim that gets a test which really kills a real process (§D9).

The temptation on this side is the mirror of ADR 0053's: making the durable
queue "fast" by making it not durable. There is no in-memory mode offered as a
production option — `NewMemory` exists and is documented, in four places, as a
test double.

## Decision

### D1 — At-least-once delivery. Exactly-once is refused because it does not exist.

Exactly-once delivery over a transport is not a hard problem, it is an
impossible one: any protocol in which the consumer can die between doing the
work and reporting it has two indistinguishable states, and the queue must
choose to lose the work or to repeat it. This domain repeats it.

What is unusual here is not the choice — everybody makes it — but that the
consequence is put in the TYPE rather than in a paragraph nobody reads:

- **`DeliveryValue.Deliveries`** is a field of every delivery, counts from 1,
  and is handed to the handler. A consumer cannot destructure a delivery
  without reading the number that says this may not be the first time. The
  `Handler` port takes the whole `DeliveryValue` and not just the payload for
  exactly this reason.
- **`ConsumerConfig.HandlerIsIdempotent` must be `true`.** Its zero value is
  `false` and is **REFUSED** (`CONSUMER_MISCONFIGURED`, `0.3.53.3`). This is
  `resilience.HedgeConfig.Idempotent`'s instrument, cited by ADR 0031 for its
  reason: a precondition the SDK cannot check becomes a required, greppable
  assertion instead of a comment. `grep -rn HandlerIsIdempotent` enumerates
  every place in a codebase where somebody promised this.

The ceremony is deliberate and it is the only one in the domain. Every other
field of `ConsumerConfig` is clamped, so a caller writes two fields and gets a
correct consumer.

**What is NOT guaranteed, stated plainly:** this queue will deliver a message
twice. Not rarely, not under exotic failure — routinely, whenever a consumer is
slower than its visibility timeout or dies after doing the work. A handler that
is not idempotent is a bug the SDK has made you sign for.

### D2 — A message is removed at ACKNOWLEDGEMENT, never at read. The lease is the whole mechanism.

`Receive` LEASES: the message becomes invisible to every other consumer for
`PolicyValue.VisibilityTimeout`. `Ack` is the only call in the domain that
removes a message.

What happens if the consumer dies between the two is not an edge case, it is
the design:

- it never acknowledges and never nacks — a SIGKILLed process runs no deferred
  function and no signal handler;
- its lease lapses;
- the next consumer to look, **in any process**, finds a lapsed lease and
  returns the message to the queue with `Deliveries` incremented.

`VisibilityTimeout` is therefore the one number worth thinking about when
wiring this: it is how long a dead consumer's work sits idle. It is also why it
is REFUSED at zero (§D5) rather than defaulted.

**A lapsed holder's acknowledgement releases NOTHING.** `Ack`, `Nack` and
`Extend` on a lapsed lease return `LEASE_EXPIRED` (`0.2.23.4`) and change
nothing. This is ADR 0052's rule for a lapsed lock lease, one domain over and
for the same reason: a holder that lost its claim must not be able to end the
new holder's section, and must find out that it lost it. A consumer that gets
this has produced a duplicate — which at-least-once permits — and the one thing
it must not do is believe the work is finished.

The refusal is deliberately checked against the CLOCK before the storage, so a
lapsed lease whose message nobody has reclaimed yet is refused too. Otherwise
"is this still mine?" would have two answers for one state, depending on which
consumer last happened to poll.

**`UNKNOWN_RECEIPT` and `LEASE_EXPIRED` are separate codes.** "You were too
slow" and "this was never yours" are different bugs — a visibility timeout too
short for the handler, against a receipt built by hand or held across a restart
— and collapsing them would send every reader looking for the wrong one half
the time. The two brokers distinguish them differently, on purpose: the memory
broker by an instance nonce in the receipt, the file broker by whether the
receipt is a name this package could have written. A broker-instance nonce
would be *wrong* in the durable broker, because a receipt minted in another
process is exactly what it is supposed to accept.

### D3 — The dead-letter path is after `MaxDeliveries`, and it keeps the CAUSE. The BROKER owns the count.

After `PolicyValue.MaxDeliveries` deliveries the message is moved to the
dead-letter store rather than retried again. `Nack` reports which happened, in
`NackValue.DeadLettered`, because "I reported the failure" and "that message is
now dead" are different facts and a consumer that cannot tell them apart cannot
log the one that matters.

**The count lives with the message, in the broker, because the consumer is the
thing that dies.** A count held by the consumer engine would be lost by exactly
the event it exists to survive, so a payload that crashes the process on every
attempt would be retried forever, killing every consumer that touched it. The
same comparison therefore governs a lapsed lease as governs a nack: a message
that has been delivered `MaxDeliveries` times and never acknowledged is
dead-lettered with `LEASE_EXPIRED` as its reason, which is the signature of a
handler that takes the process down with it.

**A dead letter without its cause is an investigation nobody can run**, so
`DeadLetterValue` carries the failure's `Reason` (SCREAMING_SNAKE), its
dotted-quad `Code`, and its `Cause` — the WIRE-SAFE Public half.

The Private half is deliberately **not** kept, and this is CLAUDE.md rule 4
applied rather than repeated: a dead-letter store is read by whoever is
investigating, frequently not the process, the host or the trust domain that
produced the failure, so it is the wrong place for the half of an error the SDK
defines as log-only. `MessageValue.ID` is the join key back to the log line
that has it. A nil cause is recorded as `UNREPORTED` rather than as an empty
string, because "it did not work and I cannot say why" is a real answer.

Reading the store does not consume it. Evidence a read consumes is evidence the
second investigator does not get.

### D4 — Ordering is NOT guaranteed, and one retry is enough to lose it.

The whole promise: **one consumer, no failures, one broker — publication
order.** Change any of the three and it is gone.

The one people discover in production is the retry, so it is demonstrated by a
test rather than left to be found. `TestASingleRetryReordersTheStream`
publishes `first`, `second`, `third` to one consumer with no concurrency, fails
`first` once, and asserts the delivery order is **`second`, `third`, `first`**.

That is not a defect. It is the arithmetic of a retry: a message that must be
reprocessed can either wait — stalling everything behind it, which is
head-of-line blocking and a worse failure — or step aside. This domain steps
aside.

**There is no per-key ordering**, and it is deferred by name rather than
omitted quietly (see Deferred). Two messages that must be processed in order
are one message.

### D5 — Zero values: both branches of ADR 0031, in one struct, argued field by field.

`PolicyValue` uses both halves deliberately, because a domain in which every
zero is refused teaches a reader nothing except that the author was nervous.

**REFUSED**, because the zero has two natural readings that are OPPOSITES and
one of each pair silently destroys the guarantee:

- `VisibilityTimeout`: "redeliver immediately" (a delivery storm) against
  "never redeliver" (a lost message).
- `MaxDeliveries`: "unlimited" (a poison message loops forever and the
  dead-letter store stays empty) against "none" (a queue that delivers
  nothing).

This is ADR 0052's argument for refusing a zero lock TTL, and it is the same
argument because it is the same class of value: a lifetime belongs to the work
being protected, and only the caller knows what that is. `LeaseExtender.Extend`
refuses a non-positive renewal for the identical reason.

**CLAMPED**, because the zero has exactly one reading and it is harmless:

- `RetryDelay` zero means "eligible as soon as the nack returns". Negative is
  read as zero.

**BOUNDED FROM ABOVE**, which is arithmetic rather than a reading.
`VisibilityTimeout` and `RetryDelay` are refused past `MaxDeadlineOffset`, a
century: every deadline is now plus one of them, the durable broker writes it
into a file name as Unix nanoseconds, and those end on 2262-04-11. Past it the
number wraps negative, the name cannot be read back, and the message is never
reclaimed while its receipt reads `UNKNOWN_RECEIPT` — the `math.MaxInt64`
somebody writes to mean "never" is 292 years, so it did exactly that.
`Extend`'s duration never passes through `Validate`, so each broker checks the
extended instant itself and refuses before anything moves; the memory broker
refuses the same `by` although `time.Time` could hold it, because it is the
durable broker's double. (Amended 2026-09-11: neither bound existed at first.)
- `MaxMessageBytes` zero means `DefaultMaxMessageBytes` (1 MiB — NATS's
  default, Kafka's default `message.max.bytes`, four times SQS's maximum).
  Negative is refused, because it is not a bound and the caller who wrote it
  meant something. The bound exists at all because without one a single
  producer can fill a device every other producer shares.
- `ConsumerConfig.Parallelism` and `BatchSize` clamp to 1; `PollInterval`
  clamps to 100 ms.

One refusal is not about a zero at all: `Receive(ctx, 0)` is
`INVALID_BATCH_SIZE` rather than an empty slice, because a consumer loop asking
for zero messages spins forever, processes nothing, reports no error, and looks
exactly like an idle queue — ADR 0031's inert policy in its purest form.

`MaxDeliveries: 1` is a legitimate configuration meaning "one attempt, then the
dead-letter store", pinned by a named test so nobody "fixes" an off-by-one that
is not one.

### D6 — The durable broker's state is a FILENAME, and every transition is one `rename(2)`.

Three directories are the three states — `ready/`, `inflight/`, `dead/` — and a
message is in exactly one of them. Which messages are queued, which are leased,
when each lease lapses and how many times each has been delivered are all
encoded in directory ENTRIES.

`rename(2)` is the only operation a POSIX filesystem offers that changes a fact
indivisibly **and refuses for the loser**. Three properties fall out, and they
are the reason the design is this and not a state file:

- **Exclusion is free and there is NO LOCK.** Two consumers reaching for the
  same message both attempt the same rename; the kernel gives it to one and
  gives the other `ENOENT`, which the loser reads as "somebody else took it".
  Nothing is held, so nothing can be held by a dead process. And it resolves
  identically for two goroutines and for two processes — which ADR 0052
  **measured** that `flock(2)` emphatically does not: eight goroutines sharing
  one open file description were all inside one counted section, every run. The
  lock domain had to compose `flock` with an in-process gate; this domain does
  not need either.
- **Recovery needs no daemon.** There is no sweeper goroutine, no timer and no
  background reclaim. A lapsed lease is noticed by the next `Receive`, in
  whatever process makes it. A sweeper would be a fourth thing that can die,
  and it would have to be elected between processes to stop all of them
  sweeping at once.
- **A lexicographic sort is a chronological sort.** Every name opens with a
  19-digit zero-padded instant, so `fs.ReadDir`'s ordering is FIFO for free.

The alternative — a state file, or a header rewritten in place — needs a lock
to be read-modify-written, and a lock needs an owner that can die. The whole
point of this broker is that its owner can die.

The receipt IS the in-flight filename, and the name grammar admits only digits
and lower-case hex, so no receipt a caller can construct names a file outside
the in-flight directory. `os.Root` refuses it a second time regardless.

### D7 — `internal/service/vfs` publishes; `os.Root` moves. The boundary is `Rename`, and it is ADR 0039's.

**`vfs` IS used**, for the two writes that must be atomic AND durable — the
enqueue, and the dead-letter record — and it is the right call rather than a
convenience. `corevfs.AtomicWriter.WriteAtomic` is ADR 0056's five steps
(temporary in the same directory so `EXDEV` is unreachable, write, flush the
FILE, `rename(2)`, flush the DIRECTORY), whose failure paths were tested by
injecting a failure at each one and comparing the destination's SHA-256.
Rewriting that here would be eighty lines of subtle duplication, and the first
thing to rot would be the failure paths — which are the only part that matters.

**It stops at `Rename`, which `corevfs.WritableFS` does not have.** That is not
worked around and the port is not widened: `WritableFS` is FROZEN (ADR 0039),
`pkg/v1/vfs` aliases it, Go interfaces are structural, and a fifth method to
carry this domain's state machine would break every downstream implementation
at compile time with no deprecation window. The transitions therefore go
through `os.Root` directly — the same confinement mechanism `vfs` itself uses,
`openat2` with `RESOLVE_BENEATH` on Linux — and the boundary between the two is
exactly the boundary between "publish a whole file" and "move one".

It is recorded here because "why is there a second filesystem handle in this
package" is the first question a reader will have, and because the alternative
(widen the port) is the one somebody will propose.

**The flushes are only on `Publish`, and that is a decision.** Every
non-durable outcome of a receive, an ack or a nack degrades into a
REDELIVERY — which at-least-once already permits and every consumer is already
obliged to survive. Paying a device round trip to make a duplicate slightly
less likely would buy nothing the contract does not already give away.
`BENCH.md` says what the one flush costs: **3.3 ms of a 3.3 ms round trip** —
the enqueue IS the round trip, to within the measurement's noise.

**What durability does NOT mean.** `Publish` returning nil means this process
asked the kernel to flush and the kernel said it did. A lying disk cache, a
virtualised host that acknowledges early, or a filesystem mounted with barriers
off will still lose the message, and no library can detect that. The domain
promises `fsync`, not physics.

### D8 — Frozen ports, two capability siblings, no registry.

`Broker` is FROZEN at four methods: `Publish`, `Receive`, `Ack`, `Nack`.
`Handler` is a FUNC port, so it cannot grow a method at all — the shape ADR
0041 and ADR 0050 use, satisfying ADR 0039 structurally.

Two capabilities are siblings reached by type assertion, and both brokers
implement both:

- **`DeadLetterReader`** — not every broker exposes its dead letters in-band. A
  connector onto a third-party system usually dead-letters into a second queue
  read through this same `Broker` interface, and one that dead-letters into an
  operator's console cannot return anything at all.
- **`LeaseExtender`** — the honest answer to the one question
  `VisibilityTimeout` cannot settle. The timeout must exceed the slowest
  handler, and the slowest handler is not known in advance; rather than
  inviting an hour-long timeout — which turns every consumer crash into an hour
  of silence — a handler that legitimately needs longer says so.

`Extend` returns a **new** `LeaseValue`, receipt included, and the old receipt
is void. That is not ceremony: the durable broker's deadline lives in the name,
the only atomic way to change a name is to replace it, and a receipt that
silently kept meaning something after the thing it named had moved would be the
one bug this domain is built to avoid. The memory broker mints a new receipt
too, so the two agree.

Unlike ADR 0052's `Deadliner`, the absence of a sibling here is not a signal:
both SDK brokers implement both. The assertion earns its keep at the boundary
where a third-party connector arrives, and saying so is more useful than
inventing an asymmetry.

**No registry.** One `Broker` value is one queue, and the queue's identity is
the implementation's configuration — a directory. A registry would hold one
entry per queue and add a way to misconfigure a wiring at runtime, which is the
argument `proc`, `resilience`, `scheduler`, `lifecycle` and `events` all make.

**The routing key is a NAME, and it has to be.** ADR 0053 §D2 routes on
`reflect.Type` because the compiler mints it and it cannot collide across
packages. That argument is *process-local*: a Go type has no identity in
another process, so a domain whose entire point is crossing that boundary
cannot use one. This is a direct consequence of the frontier rather than a
weaker choice, and ADR 0053 said as much when it wrote "an event that crosses a
process boundary has no identity in this scheme".

### D9 — The consumer-death test kills a real process with a real SIGKILL.

A test that calls `Nack` proves that `Nack` works. It says nothing about the
case the domain exists for, because a consumer that nacks is still running: it
reached its error path, it had a stack, it had a deferred function.

`TestAKilledConsumerLosesItsLeaseAndTheMessageComesBack` removes all three:

1. The parent publishes one message and re-executes the test binary as a
   **child process**, running only the victim test.
2. The child leases the message and announces the fact on stdout, then blocks
   in a `read(2)` on a pipe the parent holds open — a syscall, not a channel,
   because a channel with nothing to send it would be a deadlock the Go runtime
   detects and *panics* on, and a panicking child is a child that ran a
   deferred function.
3. The parent asserts its OWN `Receive` returns nothing. This is the
   inter-process claim, asserted rather than assumed: a different process is
   obeying a lease it never took, and the only place that lease exists is the
   filesystem.
4. The parent sends **SIGKILL** — not `Interrupt`, not a cancelled context, not
   a closed pipe. It cannot be caught, blocked or handled: no deferred
   function, no signal handler, no buffered write flushed, no graceful shutdown
   path. The child does not get to participate in its own cleanup.
5. The parent asserts `Receive` still returns nothing — the dead consumer's
   lease is honoured until its deadline, which is why redelivery is bounded
   rather than immediate.
6. After the visibility timeout, the parent receives the **same message** with
   **`Deliveries == 2`**.

**What that proves, exactly:** every fact the recovery needed — the lease, its
deadline, the delivery count, the payload — was on the disk before the kill,
because the process that knew them no longer exists and nothing in the parent's
heap ever held them. The delivery count in particular could not have come from
anywhere else.

The test waits on the REAL clock, which is the one place in the package that
does. That is deliberate and recorded: the lease deadline is a wall-clock
instant written into a filename precisely so another process can read it, and
another process does not share this one's `ManualClock`. A fake clock here
would prove that the parent agrees with itself, which is exactly the thing the
test exists not to settle.

The victim self-skips unless the parent set its environment variable, so it is
DISCOVERED by `go test ./...` like any other test and needs no build tag, no
`manual` target, no `-test.run=^$` and no compensating lane — CLAUDE.md rule 12
is satisfied by not creating an exclusion in the first place.

### D10 — A handler error is recorded unmodified; a handler panic is not. A departure from `service/events`, with its reason.

`service/events` joins its `ListenerFailed` verdict **beside** the listener's
cause, because `Publish` RETURNS the aggregate and a caller must be able to ask
"did anything fail?" without knowing every code every listener might produce.

Here the aggregate's destination is a **dead-letter record**, which already
says "a handler failed" by existing and has structured fields for the rest. A
verdict wrapped or joined around the cause would only displace the reason an
investigator actually needs — so there is deliberately **no `HandlerFailed`
sentinel**, and `TestAFailingHandlerIsRetriedAndThenDeadLettered` asserts that
the handler's own `Reason` is what reaches the store. This was found by writing
the test, watching it record `HANDLER_FAILED`, and deciding the test was right.

A handler **panic** does get a sentinel. Left alone it would kill the consumer
PROCESS, abandoning every other in-flight lease — each of which would then have
to time out, one visibility timeout at a time, before anything moved again.
Recovered, it becomes an ordinary nack: this message is retried and eventually
dead-lettered, its siblings are untouched. The recovered value and the
originating stack travel as FIELDS, never as the wrap origin, so a
`panic(someSentinel)` cannot hijack `HANDLER_PANICKED` — the `service/events`
and `service/lifecycle` rule.

`Consume` returns nil when its context ends — a cancelled consumer is a stopped
consumer, not a failed one — and returns the broker's error when the STORAGE
fails, because a queue whose medium has stopped answering is not something a
consumer can poll its way out of. A `LEASE_EXPIRED` from an acknowledgement is
the one thing swallowed, and by name: the message is already elsewhere and
there is nothing the worker can do but keep working.

### D11 — Four layers, no new kernel primitive, and one kernel primitive reused because the benchmark demanded it.

`internal/core/queue` (contract, values, policy guard, sentinels — `0.2.23.*`),
`internal/service/queue` (two brokers + the consumer engine — `0.3.53.*`),
`pkg/v1/queue` (aliases + three delegations). No new kernel package.

`internal/kernel/heap` is composed by the memory broker, and it is there for a
measured reason rather than a stylistic one — see the next section.

## Consequences / Semantics

- **A producer and a consumer stop sharing a process, a goroutine and a
  transaction.** That is the whole point, and everything expensive about this
  domain is the price of it.
- **Durability costs 1 865× on a round trip** on the measurement device: 1 766
  ns in memory against 3 293 139 ns on disk. Essentially all of it is
  `Publish`, and `Publish` is two `fsync` calls.
- **The cost of a durable publish is independent of the payload.** 64 B, 4 KiB
  and 64 KiB all cost ~3.5 ms, and 4 KiB measures MORE than 64 KiB — which is
  the giveaway that the spread is variance rather than a size effect.
  Batching helps; shrinking does not. This independently reproduces ADR 0056's
  finding on the same device, which is the expected result — this domain's
  `Publish` IS `vfs.WriteAtomic` plus a name.
- **One durable producer tops out at ~300 messages/second on this device**, and
  no code change moves that. More throughput comes from more producers, or from
  a connector over a system that batches `fsync` across publishers.
- **The durable broker's cost scales with the BACKLOG, and that is its stated
  limit.** A `Receive` reads and sorts the whole queued directory: 122 µs at a
  backlog of 10, 392 µs at 100, 3.0 ms at 1 000. This is a durable queue for
  hundreds to low thousands. A caller with a persistent backlog of a million
  wants a broker over a system built for that, behind this same port — which is
  the reason the port is frozen and small.
- **Batching amortises the scan by 6.5× at a batch of 16.** The trade-off is
  stated where the field is: a batch is leased all at once and processed one at
  a time, so the last message of a batch of 16 has already spent fifteen
  handlers' worth of its visibility timeout.
- **Two defects were found by the benchmark and fixed, with both numbers
  published** (`BENCH.md`):
  1. The memory `Publish` cloned the payload twice — once into the queue's own
     record, which it must, and once for the returned `MessageValue`, which the
     caller already held. `pprof -sample_index=alloc_space` put `slices.Clone`
     at **99.78 %** of the bytes on the path. Fixed: 131 284 → 65 762 B/op at
     64 KiB, 5 → 4 allocs, and 23 % less time.
  2. The memory `Receive` was O(in-flight): `reclaimExpired` ranged over the
     whole in-flight MAP, and a map has no order, so there was nothing to stop
     early on. **105 µs per lease.** The file broker never had it, because its
     in-flight directory sorts by deadline and its scan breaks at the first
     lease still held. Fixed with `internal/kernel/heap` keyed on the deadline,
     with lazy deletion so an `Ack` stays O(1): **105 µs → 1.83 µs**, and the
     memory column of the backlog benchmark is now flat across a 100× range.
- **`Publish/file` allocates 41 times and is deliberately not optimised.** Two
  kilobytes of garbage sits next to 3 ms of `fsync`; removing all of it would
  improve the verb by under one part in a thousand and would cost the
  straightforward code that makes the failure paths auditable. Recorded in
  `BENCH.md` rather than left silent.
- **An in-memory queue holding 64 KiB messages is a garbage-collection
  problem**, and the benchmark is unstable for that reason (25 µs at
  `-benchtime=300ms`, 124 µs at 1s, allocating ~8 GB). That instability is
  itself the finding, and it is one more reason `NewMemory` is a test double.

## What this domain does NOT guarantee

Collected in one place on purpose, because each of them is something a reader
may otherwise assume:

1. **Exactly-once delivery.** It does not exist. You will see duplicates.
2. **Ordering under retry.** One failure reorders the stream, with one
   consumer and no concurrency. More than one consumer removes ordering
   entirely. There is no per-key ordering.
3. **Durability beyond what `fsync` actually does.** A lying cache or a
   barrier-less mount loses acknowledged writes and nothing here can tell.
4. **Mutual exclusion between a consumer and its own lease deadline.** After
   `ExpiresAt`, another consumer may hold the message while the first is still
   running. `LeaseExtender` is the way to avoid producing a duplicate on
   purpose; it is not a way to be sure you have not.
5. **Scale.** See the backlog measurement. This is a directory.
6. **Anything at all from `NewMemory` across a restart.** It is a test double.

## Breaking changes

None. `queue` is a new domain in this change set: no existing package changed,
no published shape moved, and the ADR 0040 v0 licence is not used — said out
loud rather than left silent. The only edits outside the three new packages are
the two `codeRangeOwners` rows, the three `audit_srcs` entries, two additive
`.ktn-linter.yaml` vocabulary entries, the regenerated `docs/error-codes.yaml`,
and the documentation the change is obliged to keep in step (rule 11).

## Alternatives considered

- **An async mode on `events` instead of a second domain.** Rejected by ADR
  0053 §D1 before this ADR existed, and this domain is the reason that
  rejection was affordable.
- **An append-only log plus an index.** Rejected in favour of one file per
  message: the log needs compaction, the index needs a lock, and the lock needs
  an owner that can die. A file per message makes every state transition a
  `rename(2)` and needs neither.
- **A `flock`-based lock over the queue directory.** Rejected: `rename(2)`
  already provides the exclusion, and ADR 0052 measured that `flock` would
  additionally need an in-process gate to exclude goroutines. A lock nobody
  needs is a lock a dead process can hold.
- **Adding `Rename` to `corevfs.WritableFS`.** Rejected in §D7: the port is
  frozen, and this domain's convenience is not a reason to break every
  downstream implementation.
- **A sweeper goroutine reclaiming lapsed leases in the background.**
  Rejected: it is a fourth thing that can die, it needs electing between
  processes, and the reclaim it would perform is already performed by whoever
  looks next — which is the only party that cares.
- **`Nack(ctx, receipt, cause) error` without a return value.** Rejected: a
  consumer that cannot tell "retried" from "dead-lettered" cannot log the one
  that matters, so `NackValue` says which.
- **Storing the delivery count in the consumer engine.** Rejected in §D3: the
  consumer is the thing that dies.
- **An exactly-once mode, or a `DeliveryGuarantee` enum.** Rejected: the first
  does not exist, and the second would make the guarantee a construction-time
  variable no call site can be read against — ADR 0053 §D1's mistake, one
  domain over.
- **A `HandlerFailed` verdict joined beside the handler's cause**, as
  `service/events` does. Rejected in §D10, with the test that decided it.
- **Fsyncing on `Ack`.** Rejected in §D7: it buys a marginally lower duplicate
  rate that the contract already gives away.
- **A NATS, AMQP or Redis connector in this change.** Out of scope by
  doctrine: `internal/*` takes no dependency, and a connector to a third-party
  system belongs under `third-party/` where it is isolated. The port was frozen
  small precisely so one can be added behind it later.

## Deferred

- **A `third-party/` connector** (NATS, AMQP, Kafka, SQS) behind this same
  `Broker` port. It is the reason the port is four methods and the reason the
  two capabilities are siblings rather than methods.
- **Per-key (partitioned) ordering.** Deferred by name because the honest
  version of it means a failed message BLOCKS its key — head-of-line blocking —
  which is a different product decision and not a feature flag on this one.
- **Delayed/scheduled publication** (`PublishAt`). The visibility instant is
  already in the durable representation, so the mechanism exists; the decision
  about whether a queue should also be a scheduler does not, and ADR 0041 is
  where scheduling decisions live.
- **Priority queues.** The name grammar sorts on one instant; a priority would
  be a second sort key and a starvation policy, and starvation policies are not
  something to add silently.
- **A metrics binding** (published, delivered, dead-lettered, lease expiries).
  `NackValue` and `DeliveryValue` carry what such a binding would need; wiring
  it here would make `queue` depend on `metrics` for everyone who does not want
  it — ADR 0053's reasoning, unchanged.
- **Batch acknowledgement** (`AckAll`). It would amortise the durable broker's
  syscalls the way `BatchSize` amortises its directory scan; it needs a
  partial-failure semantic first, and that deserves its own decision.

## References

- `internal/core/queue/CLAUDE.md` (the frontier table, the five decisions, the Do-NOT list)
- `internal/service/queue/CLAUDE.md` (the name grammar, where `vfs` stops, why there is no lock)
- `internal/service/queue/BENCH.md` (the cost of durability, the backlog limit, the two defects the numbers found)
- `pkg/v1/queue/README.md` (consumer-facing, generated from the package doc comment — ADR 0008)
- `docs/adr/0053-sdk-events-domain.md` §D1 (the frontier, written before this domain existed)
- `docs/adr/0052-sdk-lock-domain.md` (the lapsed-lease rule, and the `flock(2)` measurement this domain avoids needing)
- `docs/adr/0056-sdk-vfs-domain.md` (`WriteAtomic`'s five steps and their tested failure paths)
- `internal/service/queue/death_external_test.go` (the SIGKILL)
