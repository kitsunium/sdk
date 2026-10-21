# internal/service/data/queue/

## Purpose

The three brokers implementing `internal/core/data/queue` (ADR 0054) and the
consumer engine that drives a `Handler` against any of them: `NewFile` (durable,
its state a directory), `NewSQL` (durable, its state one table of the caller's
own database, every call on the transaction its context carries — ADR 0151),
`NewMemory` (the test double), and `Consume` (the pull loop that runs handlers
on goroutines it owns).

All three implement every capability sibling: `DeadLetterReader`,
`LeaseExtender`, `Waker`, since ADR 0151 `Rejecter` and
`DeadLetterManager`, since ADR 0178 `DeadLetterWriter`, `MessageReader` and `MessageWriter`, since kitsunium/sdk#474 `Releaser`, and since ADR 0183 `DelayedNacker`, `DelayedPublisher` and `OncePublisher`; and all three wait the same growing retry delay when the
policy's `MaxRetryDelay` asks for one (`retry.go`).

Code range: `0.3.53.*` (ADR 0054) — `QUEUE_BACKEND_FAILED`,
`QUEUE_DIRECTORY_UNUSABLE`, `CONSUMER_MISCONFIGURED`, `HANDLER_PANICKED` and
`SQL_QUEUE_MISCONFIGURED`, declared since ADR 0160 with the port's `0.2.23.*` in
`internal/core/data/queue`, under the values this package allocated. This
package declares no code.

## Contents

| File | Holds |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `queue.go` | the dead-letter `causeValue` reduction, `randomHex`, the shared guards (`checkBatch`, `checkSize`, `checkDeadLetter` and `checkLiveMessage` — the refusals of `PutDeadLetter` and `PutMessage`, identical on the three brokers, ADR 0178; `nackDeadline`, `publishVisibleAt` and `onceUntil` — the arguments of `NackAfter`, `PublishAt` and `PublishOnce`, read and refused identically on the three, and `onceDigest`, the SHA-256 the durable brokers keep of a key, ADR 0183), `deadLetterNotFound` |
| `retry.go` | `retryDelay`: RetryDelay, or the `kernel/backoff` curve from it to `MaxRetryDelay` — one function the three brokers call (ADR 0151) |
| `memory.go` | `NewMemory` and the in-heap broker: the heap-ordered lease expiry, the ready list ordered at insertion; `Reject`, `Release`, `NackAfter`, `PublishAt`, `PublishOnce` (a map of the keys reserved and a heap of their windows' ends, `dropPassedOnce`), `ReplayDeadLetter`, `DeleteDeadLetter`, `PutDeadLetter`, `Messages`, `PutMessage` |
| `memory_config.go` / `mem_record.go` / `lease_expiry.go` / `once_end.go` | `MemoryConfig` and the three values the memory broker keeps |
| `file.go` | `NewFile`, `Publish`, `PublishAt` (a queued name whose visibility is the instant), `write` (the durable publication both share, waking nobody), `Ack`, receipt resolution, `entriesOf` |
| `file_once.go` | `PublishOnce` (ADR 0183): the message written first, then the key reserved — `once/<sha256>.key`, written under a name of its own with `O_EXCL` and `link(2)`ed to the key's —, a racer's message withdrawn before anybody is woken (`withdraw`), a passed reservation set aside and read again before it is removed (`dropPassed`), and the sweep, at most once a minute per broker (`sweepOnce`) |
| `file_config.go` | `FileConfig`, the directory preparation, and the refusals it runs on the queue directory AND each state directory — the shape half (a link, a reparse point, a non-directory), shared by every platform |
| `dirtrust_posix.go` / `dirtrust_windows.go` | the permission half of those refusals: the mode bits on Unix, the directory's DACL on Windows, read through the kernel's reader, `internal/kernel/fs/winacl` |
| `file_name.go` | the NAME grammar — the durable broker's entire state machine — and `nameable`, the range of instants a name can carry. Every field is held to the exact width and spelling the renderers write (entropy and lease as wide as `randomHex` makes them, the count as `padCount` spells it), so a stray file of the right shape is skipped rather than delivered |
| `file_receive.go` | `Receive`, the reclaim scan, the rename that IS the exclusion |
| `file_dead.go` | `Nack` and `NackAfter` (one `nackWith`, the delay the policy's or the caller's), `Reject`, `Release` (one rename back into `ready/`, visible now, the count one lower), `Extend`, `DeadLetters`, the burial, the dead-letter record's encoding |
| `file_replay.go` | `ReplayDeadLetter`, `DeleteDeadLetter`, `PutDeadLetter` (a burial's record, published atomically and durably), `Messages` (`liveFiles`, `parseLive`: the two state directories' names, sorted by identifier) and `PutMessage` (a `ready/` file, as a retry leaves one), `deadRecordsOf` — every record of one message by its ID |
| `sql.go` | `NewSQL`, the SQL broker, `Publish` and `PublishAt` (one INSERT, `due` the instant), `Ack`, `Nack` and `NackAfter` (one `nackWith`), `Release`, `Extend`, `Wake`; `run` (a savepoint of the caller's transaction, a transaction of the broker's own, or one statement on the pool), `announce` (the wake through `Deferrer`), the receipt grammar, `failed` (QUEUE_BACKEND_FAILED + the driver's error through `service/data/sql`'s `Withheld`, its text out of every rendering), the process-wide `sqlWakes` table |
| `sql_once.go` | `PublishOnce` (ADR 0183): one transaction — the key's passed reservation dropped, the key claimed by an insertion that affects no row when it is taken, the message inserted when the claim took, and up to 100 passed reservations of other keys read `SKIP LOCKED` and removed (`claimOnce`, `sweepOnce`) |
| `sql_receive.go` | `Receive`: the probe, then — only when something is due — the lease transaction: `pickDue`, `buryLapsed`, `leaseRows`, `minDue` |
| `sql_dead.go` | `Reject`, `DeadLetters`, `ReplayDeadLetter`, `DeleteDeadLetter`, `PutDeadLetter` (one INSERT of a dead row, in the caller's transaction when its context carries one), `Messages` (one unlocked read of the live rows after an identifier) and `PutMessage` (one INSERT of a live row with its count) on the table |
| `sql_config.go` | `SQLConfig`, `MaxSQLTableLen`, the table-name rule, the refusals (`SQL_QUEUE_MISCONFIGURED`) |
| `sql_dialect.go` | **the only place the SQL broker renders SQL**: every statement per dialect, `leaseRows` / `buryRows` / `onceSweepRows` rendered per call, `onceClaimStatement` (`ON CONFLICT (id) DO NOTHING`, MySQL's `INSERT IGNORE`), the DDL; every marker, quoted name and the lease's `FOR UPDATE SKIP LOCKED` is `core/data/sql.Dialect`'s (`Placeholder`, `QuoteIdent`, `ForUpdateSkipLocked`) |
| `sql_migration.go` | `SQLMigration`: the one table as an idempotent `core/data/sql` migration the caller numbers |
| `consume.go` | `Consume` (its workers in a `kernel/concur/group` joined group) and `consumer`, the siblings asked once; the pull loop, the batch (`settleBatch`, `prepare`: a message whose lease ran down is handed back or renewed before it starts), the outcomes (`settle` — `NackAfter` for a `RetryAfter` failure when the broker has the sibling, ADR 0183 —, `handBack` — `Release`, or `Nack` without the sibling — `nack`), the panic guard, and the idle wait on a `Waker` (`idleFor`, `idle`, `armUntil`: the wait armed until an instant measured before the broker is asked, re-armed when the clock moved across its registration — a ManualClock advanced while a worker armed its wait fired it that much late, and a spool retry was missed on OpenBSD's CI; `TestAnIdleConsumerWakesAtTheDueInstantWhenTheClockMovesWhileItArms`) |
| `consume_lease.go` | the handler's watch (kitsunium/sdk#474): `leasePlan` (the lease as granted, its margin — a tenth, at most `SettleTimeout` — and AutoExtend's third), `watch`/`oversee`/`finish` (one goroutine per handled message that ends its context on the stop, after `DrainTimeout`, and a margin before the lease ends, and renews it under AutoExtend), `renew`, and `bounded`/`bound` — every outcome on its context without the cancellation, bounded by `SettleTimeout` from the stop on, a renewal always |
| `wake.go` | `wakeSignal` (the broadcast every broker closes on Publish, Nack and a replay, and a durable broker's recorded due instant), `wakeValue`, `earliest`, and `wakeTable[K]` — the process-wide, weakly-held table that makes every durable broker over one queue share one signal: `fileWakes` by directory, `sqlWakes` by pool and table |
| `consume_config.go` | `ConsumerConfig`, the idempotence assertion, the clamps |

## Why the durable broker's state is a FILENAME

Because `rename(2)` is the only operation a POSIX filesystem offers that
changes a fact indivisibly and refuses for the loser. Three directories are the
three states — `ready/`, `inflight/`, `dead/` — and every transition is one
rename. So:

- **Exclusion is free and needs no lock.** Two consumers reaching for the same
  message both attempt the same rename; the kernel gives it to one and gives
  the other `ENOENT`. There is no lock file, nothing a dead process could hold,
  and — unlike `flock(2)`, which ADR 0052 MEASURED giving zero exclusion
  between goroutines sharing one open file description — it resolves identically
  for two goroutines and for two processes.
- **A name a rename left behind is removed (`dropStale`).** Not every
  filesystem renames in one step: OpenBSD's FFS links the new name and then
  unlinks the old, and two consumers racing on one message there left it
  under two names — queued, and leased by nobody (#398, where a queued
  command's restart test then saw it delivered twice). After each transition
  the broker removes the name it no longer owns: the source it just renamed
  away (`takeLease`, `Nack`, `recoverOne`), or, when its rename lost with
  `ENOENT`, the target carrying its own fresh lease (`takeLease`). On a
  one-step kernel each removal answers `ENOENT` and costs one syscall. What it
  cannot undo is two renames that both report success; delivery stays at
  least once, as it always was.
- **Recovery needs no daemon.** A lease deadline and a delivery count are both
  in the name, so a consumer that is SIGKILLed leaves a directory entry any
  other process can read and act on. There is no sweeper goroutine: a lapsed
  lease is noticed by the next `Receive`, in whatever process makes it. A
  sweeper would be a fourth thing that can die, and it would need electing.
- **A lexicographic sort is a chronological sort.** Every name opens with a
  19-digit zero-padded instant, so `fs.ReadDir`'s ordering is FIFO for free.

Two consequences of "the state is a name" are enforced rather than assumed:

- **The states are checked like the root, one notch stricter.**
  `prepareState` runs the root's rule (`unusableBecause`) on each of `ready/`,
  `inflight/` and `dead/` through `os.Mkdir` + `os.Lstat` — never `MkdirAll`
  or `Stat`, which both follow a symlink — plus one refusal of its own
  (`stateUnusableBecause`): a state any account outside the owner and group
  can write is refused EVEN with the sticky bit. At the root, sticky stops the
  states being replaced, which is all the root needs; in a state it only stops
  an unlink, and a planted entry is a delivered message. Only the root used to
  be checked, so under a root the rule accepts (a group share, a sticky
  `/tmp`-like directory) another account could pre-create `ready/` and plant or
  unlink messages; then the root's rule was applied as it stood, and a sticky
  world-writable `ready/` still passed. A state that is a symlink is refused
  whatever it points at. `TestTheDurableBrokerRefusesAStateDirectoryItCannotTrust`.
  What the rule still does NOT see is an owner inside the group: a
  group-writable state another member made is trusted, as the group share
  itself is.
- **An instant a name cannot carry is never written.** A name's instants are
  19 digits of a non-negative int64, so the grammar ends on 2262-04-11; past
  it `pad` wrote a sign, `parseNano` refused the name, and the message was
  stranded. `core/data/queue.MaxDeadlineOffset` bounds the policy's durations for
  both brokers; `Extend`'s `by` never passes through `Validate`, so the file
  broker checks the deadline itself (`nameable`) and refuses before anything
  is renamed. The memory broker keeps `time.Time`, which would hold such a
  deadline, and refuses the same `by` anyway, in the same order: it is the
  double, and a double more permissive than the durable broker is a test that
  passes where production strands the message.
  `TestEveryBrokerRefusesAnExtensionANameCannotCarry` runs on all three — the
  SQL broker keeps its deadlines as the same int64 of Unix nanoseconds.

## On Windows: the directory rules read the DACL, and the broker is then refused

The permission half of the refusals used to read mode bits on every platform.
Windows has none — `os.Stat` synthesises `0777` with no sticky bit for every
writable directory — so the rule refused EVERY queue directory there as
`QUEUE_DIRECTORY_UNUSABLE`, a verdict that blames the deployment for the
platform (ADR 0018 §(a)); the first Windows run of the whole suite found it on
every `/file` case (ADR 0095). The two questions stayed; the vocabulary moved to
the DACL, read by the one reader this repository has —
`internal/kernel/fs/winacl.GrantsAnyone` (ADR 0084/0086): the lock domain's
reader, exported from `lock` for exactly this rather than copied, and moved to
the kernel so that the queue no longer imports another service to call it
(ADR 0159):

| | Unix (mode) | Windows (DACL, an identifier meaning anybody) |
|---|---|---|
| root refused when anybody can | unlink an entry: other-write without sticky | `winacl.ReplaceRights` — delete a child, rewrite the list, take ownership |
| state refused when anybody can | write at all: other-write, sticky or not | add a file (a planted message) or a directory/junction, or `ReplaceRights`; or write a FILE created there (`winacl.ContentRights`, through what it inherits — on Unix a message is 0600 whatever the directory allows) |
| an indirection | `ModeSymlink` | `ModeSymlink`, or any reparse point — a junction reads as a plain directory through `os.Lstat` |

A permission refusal names what it was read from in an `observed` field — the
mode on Unix, the entry on Windows (`S-1-1-0=0x40`), where there is no mode to
look at. A list the reader could not read, or read only in part, is refused as
`why=unverifiable` with the reader's status in `observed`
(`GetNamedSecurityInfoW=5`, `GetAce#3`). That is the one place the queue parts
from the lock, which accepts the same case and logs it (ADR 0084 §D5): a lock
directory wrongly accepted costs the hardening, a queue directory wrongly
accepted is a planted message a consumer acts on — and the queue refused every
Windows directory before this rule, so the refusal takes away nothing that
worked (ADR 0095). `TestAWindowsDirectoryNobodyCouldInspectIsRefused` pins the
verdict on every answer; `TestAWindowsDirectoryWhoseListCannotBeReadIsRefused`
drives both rules over a REAL reader failure — `GetNamedSecurityInfoW` on a
directory that is not there. A list made unreadable with `icacls` (READ_CONTROL
denied to Everyone and to OWNER RIGHTS) was still read on the windows-latest
runner, measured, so that fixture is not one a test can rely on.

Two consequences, stated rather than discovered:

- **Windows has no "created owner-only".** A state the broker makes INHERITS
  its parent's list, so a root that lets anybody ADD entries — accepted at the
  root, as sticky is on Unix — hands that grant to `ready/`, which is then
  refused as a planted-message risk. On Unix the state would have been made
  `0700` whatever the parent allowed. Closing that needs a security descriptor
  at creation; it is not done.
- **The broker still does not run on Windows.** `internal/service/data/vfs` refuses
  the platform by design (no flushable directory handle, a mode that is not an
  ACL), so once the directory rules accept, `NewFile` returns
  `UNSUPPORTED_PLATFORM` — the platform's refusal, where it used to be a false
  verdict on the caller's directory. The rules run first anyway, so they are
  right on the day vfs gains a Windows backend. The suite asserts that refusal
  and then skips every case that needs a broker (`requireFileBroker`), and
  `TestTheWindowsQueueDirectoryRuleIsTheDACL` /
  `TestAWindowsStateDirectoryIsCheckedWhoeverMadeIt` pin both rules through
  real ACLs (`icacls`) and a real junction (`mklink /J`).

## The queue directory's rule, beside the other four

`app/lock`, `proc/ipc`, `security/secret` and `security/session` each refuse a
directory by a rule of their own, compared side by side in
`internal/kernel/fs/CLAUDE.md` §Five directory rules. They share the kernel's
measurements (`winacl` here; `pathchain` elsewhere) and no rule. This one's,
and why it is not a neighbour's:

- **Two levels, one notch apart.** The root takes `lock`'s decision —
  other-write without sticky refused, group-writable accepted — because
  nothing lives there but the three states; a STATE is refused as soon as an
  outsider can write it at all, sticky or not, because there an entry IS a
  message. No other domain has a directory whose entries are acted upon.
- **Group-writable is accepted at both levels**, as `lock` accepts it: a queue
  shared between two service accounts through a group is a deployment, and a
  state another member of that group made is trusted as the share itself is.
  `ipc`, `secret` and `session` refuse a group bit; their rule would refuse
  the shared queue.
- **An unreadable Windows list is REFUSED** (`why=unverifiable`) where `lock`
  accepts and logs the same answer from the same reader — the asymmetry runs
  the other way here (ADR 0095).
- **The root's path is not walked**, and a link configured as `Dir` is
  followed (`os.Stat`), where `lock`, `ipc` and `session` audit every
  component through `pathchain`. The states are `Lstat`ed, so a link or a
  junction planted AT a state is refused; one planted at a PARENT of `Dir`, in
  a directory anybody can write, is not looked for. That is the gap
  `pathchain` closed for the other three (ADR 0083), named here rather than
  implied closed.
- **Nothing is narrowed**: a state this broker made is checked like one it
  found, and an existing directory is refused rather than chmod'ed.

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

## The SQL broker (ADR 0151)

`NewSQL` keeps the queue in ONE table of a PostgreSQL, MySQL or SQLite database
the caller owns and hands over as a `core/data/sql` transactor — which must also be
a `Joiner` and a `Deferrer`, as the SDK's is. It exists for one property the
other two cannot have: **a message published inside the caller's transaction
exists if and only if that transaction commits** — the transactional outbox.

- **Every call runs where its context says**, exactly as docstore's SQL engine
  (ADR 0139 §D5): inside the caller's transaction every call that writes is a
  SAVEPOINT of it — a failed statement undoes itself alone and, on PostgreSQL,
  clears the aborted state — and outside one, a call of one statement runs on
  the pool and a Receive that leases runs in a transaction of the broker's
  own, READ COMMITTED on MySQL. A publication's wake goes through `Defer`, so
  consumers are woken after the commit and never for a rollback.
- **One table, three columns of state.** `dead` (0 live, 1 dead letter),
  `lease` (NULL queued, the lease's random half when leased) and `due` — the
  instant the row next matters on its own: a queued message's visibility, a
  leased one's deadline, a dead letter's failure. So every live row whose `due`
  has passed is either receivable or a lease whose holder may have died, and
  ONE ordered read over the `UNIQUE (dead, due, id)` index finds both. That
  constraint can never be broken — `id` is the key — and exists to build the
  index, which PostgreSQL and SQLite declare no other way inside CREATE TABLE.
- **An idle Receive is one read and takes no lock**: `SELECT MIN(due)` over the
  live rows (the probe). Only when something is due does it open the lease
  transaction: on SQLite a write that writes nothing first (ADR 0140's
  statement, `service/data/sql`'s `FileLockSQL`, the migration runner's too —
  SQLite's lock is taken by a transaction's first WRITE, and a lease that
  read first could be refused busy at its update), then the due
  rows in `(due, id)` order `FOR UPDATE SKIP LOCKED` on PostgreSQL and MySQL,
  then one UPDATE leasing them all under one deadline and one random half, 500
  identifiers a statement. A lapsed lease with no attempt left is buried with
  `LEASE_EXPIRED` by the same read; one with attempts left is leased again,
  its count moved on. A Receive that did not fill its batch asks when the next
  row is due (`MIN(due) > now`) and records for `Wake` the earlier of that and
  a lease's deadline taken now — a concurrent consumer's lease on a row the
  locked read skipped is invisible to it; one that did records now. The
  probe keeps NULL apart from an instant of zero. No sweeper, as for the
  other two.
- **Ack, Nack, Extend, Reject and Release are ONE statement each**, matching the
  receipt's identifier, lease and delivery count, a live row, and a deadline
  still ahead of the instant the call read — so a lapsed lease is refused
  whether or not anybody reclaimed it, as ADR 0054 §D2 requires, and zero rows
  is `LEASE_EXPIRED`. Every UPDATE changes a column, which keeps MySQL's
  affected-row count exact.
- **A receipt is `<id>.<count>.<lease>`**, refused `UNKNOWN_RECEIPT` by SHAPE
  as the file broker's is: two brokers over one table, in one process or in
  twenty, are one queue, so a receipt minted elsewhere is legitimate.
- **The instants are the broker's clock, never the database's `NOW()`**, so a
  test drives every deadline with a `ManualClock` — and brokers in several
  processes compare instants their own clocks wrote. A reading 64-bit Unix
  nanoseconds cannot hold, or whose lease or retry deadline passes 2262, is
  refused `QUEUE_MISCONFIGURED` (field `Clock`) before any statement.
- **A storage failure is `QUEUE_BACKEND_FAILED`**, JOINED with the driver's
  error — never wrapped, so the verdict stays the origin — whose text is
  WITHHELD from every rendering (through `service/data/sql`'s `Withheld`, which
  docstore joins its own failures through too): a driver quotes the row a
  statement touched. The transactor's own verdicts pass through.
- **`SQLMigration(dialect, table, version)`** creates the table in one
  `CREATE TABLE IF NOT EXISTS`; the SDK numbers nothing (ADR 0055 §D12).
- **Two brokers over one pool and table share a wake** (`sqlWakes`), keyed by
  the executor `Join` answers outside a transaction and the table; a joiner
  whose pool is nil or incomparable gets a signal of its own.

What is NOT guaranteed, and says so in the ADR: a publication from another
process is found by the poll (no LISTEN/NOTIFY — a driver feature
`database/sql` does not carry); a call inside the caller's transaction runs at
that transaction's isolation — on PostgreSQL REPEATABLE READ a leased row
another transaction changed since the snapshot is a serialization failure,
which the caller gets as `QUEUE_BACKEND_FAILED`; SKIP LOCKED needs MySQL 8.0.1
or MariaDB 10.6, and an older server fails the lease the same way. And a
database fails transiently where a directory rarely does, while `Consume` still
returns on a storage failure: a consumer over the SQL broker runs under
`lifecycle`'s supervisor (ADR 0112), and SQLite is opened with a busy timeout.

## Failures no retry can fix, the dead letters' two decisions (ADR 0151), and a dead letter written as it lies (ADR 0178)

- **`Consume` rejects a `DoNotRetry` failure.** `settle` asks for the
  `Rejecter` sibling and `errs.HasCode(failure, CodeNotRetryable)`; both true,
  the message is dead-lettered at once with the handler's cause, at the count
  it had. A broker without the sibling is nacked as before. A panic is never
  the mark: `guard` carries the recovered value as a FIELD of
  `HANDLER_PANICKED`, never as its origin.
- **`ReplayDeadLetter` and `DeleteDeadLetter`**, by `MessageValue.ID`, on all
  three brokers. The memory broker moves the record back into its ready list.
  The file broker publishes the payload into `ready/` — atomically and
  durably, as `Publish` does — and removes every record of the ID AFTER, the
  order that degrades into a duplicate rather than a loss; two replays racing
  can therefore queue it twice, which at-least-once permits (a record cannot be
  renamed into the queue: a queued message is its payload alone). A record
  whose header names another message than its file name does is never
  queued. The SQL
  broker's replay is one UPDATE of the row's state, so the second of two racing
  replays is refused `DEAD_LETTER_NOT_FOUND`.
- **`PutDeadLetter` writes a dead letter as it lies (ADR 0178)**, on all three
  brokers, for a copy of a queue from one broker to another: payload, both
  instants, count, reason, cause and code — 0 included — kept, under an
  identifier the broker mints from the enqueue instant (the memory broker
  from it and its sequence). `checkDeadLetter` refuses first, identically
  everywhere: an identifier set, a count below 1 or above 2³¹−1 (MySQL's
  `INT`), an instant `nameable` refuses, a reason or cause `oneLine` would
  rewrite — `INVALID_DEAD_LETTER`, naming the field — then a payload over the
  bound, `MESSAGE_TOO_LARGE`. The file broker writes the record a burial
  writes, through the same `WriteAtomic`; the SQL broker one INSERT with
  `dead = 1` and the failure instant as `due`, a savepoint of the caller's
  transaction when there is one. A dead letter is never receivable, so none
  wakes a consumer. `deadletter_write_external_test.go` holds it on every
  broker, and the SQL one in and out of a rolled-back transaction.
- **`Messages` and `PutMessage` move a live message as it lies (ADR 0178)**,
  on all three brokers, for the same copy. `Messages(ctx, after, max)` lists
  the queued and leased messages in identifier order, resuming after `after`:
  the file broker reads `ready/` and `inflight/` — a name already says a
  message's state, deadline and count —, then the payloads, renaming nothing;
  the SQL broker sends one unlocked read, `liveList` (`WHERE dead = 0 AND id >
  ? ORDER BY id LIMIT ?`); the memory broker copies under its read lock. None
  leases, counts or reclaims. `PutMessage` refuses through
  `checkLiveMessage`, shared — an identifier set, `Leased`, a count below 0
  or above 2³¹−1, an instant `nameable` refuses: `INVALID_MESSAGE`, naming the
  field; then `MESSAGE_TOO_LARGE` — and writes a queued message visible at
  `VisibleAt` with its count: the file broker a `ready/` file named by both
  instants and the count, the state a retry leaves; the SQL broker one
  `putLive` INSERT, a savepoint of the caller's transaction, its consumers
  woken after the commit; the memory broker a record in its ready list. Its
  next delivery counts one more than the count given.
  `message_copy_external_test.go` holds it on every broker.
- **The growing retry delay is `retry.go`'s one function**, so the double waits
  exactly what the durable brokers wait. The curve's jitter stays at zero: the
  retry instant is what `Wake` reports and what a test asserts.

## Consume, and the one piece of ceremony

`ConsumerConfig.HandlerIsIdempotent` must be `true`; its zero value is refused
with `ConsumerMisconfigured`. This is `resilience.HedgeConfig.Idempotent`'s
instrument for its reason: the queue delivers at least once, the SDK cannot
check whether a handler survives that, so the caller asserts it where a
reviewer sees it — and `grep -rn HandlerIsIdempotent` enumerates every place in
a codebase where somebody promised it.

`Parallelism`, `BatchSize` and `PollInterval` are all CLAMPED, because each has
one sensible reading at zero and none of them is dangerous.

The `Parallelism` workers run in a `kernel/concur/group` JOINED group
(`group.NewJoined`): the first worker the storage refuses cancels the others —
they all poll the same broken medium — and `Consume` returns once every worker
has, with each worker's own failure joined in worker order (a sibling that was
merely cancelled reports nothing). A worker's panic outside the handler — the
handler's own is recovered into a nack — is re-raised in `Consume`'s goroutine
after its siblings have stopped. `TestAStorageFailureStopsEveryWorkerAndIsReturned`
pins the report.

Every outcome — an acknowledgement, a nack, a rejection, a release — is sent
on the consumer's context WITHOUT its cancellation (`bounded`): a stop landing
between the handler's return and the `Ack` used to abandon the acknowledgement
on every broker — each one's `Ack` honours its context — so the message stayed
leased and came back after the visibility timeout, a duplicate a graceful stop
caused (kitsunium/sdk#472, a kept letter the framework's re-seal test found
still at rest after `Stop`). `TestAHandledMessageIsAcknowledgedWhenTheStopLandsBeforeItsAck`
cancels from inside the handler, so it is that interleaving every time, on
every broker.

## A stop, a drain and a lease (kitsunium/sdk#474)

A stop used to reach the handler as its context and nothing else, and a batch
was handled whatever its leases had left. Reproduced first, on every broker,
by `consume_lease_external_test.go` — each case failed before the change:

- **A stop in the middle of a batch hands the rest back, uncounted.** Before,
  the rest of the batch went to the handler under a dead context and was
  nacked on it; every broker refused that nack, so each message stayed leased
  for a whole visibility timeout and came back one delivery closer to the
  dead-letter store — a deployment burnt a delivery of work nobody tried.
  Now `settleBatch` starts nothing once the context is done and `handBackAll`
  releases what is left (`TestAStopInTheMiddleOfABatchHandsTheUnstartedMessagesBackUncounted`).
- **Handed back is `Releaser.Release`**: receivable at once, the delivery
  count put back, never dead-lettered — SQS's `ChangeMessageVisibility(0)`.
  Each broker does it in one move: the memory broker re-queues the record with
  its count one lower, the file broker renames `inflight/` back to `ready/`
  under a name visible now with the count one lower (a `ready/` name at zero
  is the one `PutMessage` writes for a message never tried), the SQL broker
  sends one `UPDATE … SET due = now, lease = NULL, deliveries = deliveries - 1`
  under the condition every lease-ending statement carries. A broker without
  the sibling is nacked instead, with no cause: retried after its delay, the
  delivery counted (`TestAStopFallsBackToANackWithoutReleaser`).
- **The message in hand drains.** `DrainTimeout` (River's `SoftStopTimeout`)
  keeps the stop from the handler that long; what finishes is acknowledged,
  what the drain cuts short is released, and a failure once the consumer is
  stopping is the stop's, not the message's — released, unless it is a panic
  or `DoNotRetry`; that holds when the lease's margin ends the handler before
  the drain does (`TestALeaseThatEndsDuringADrainStillHandsTheMessageBackUncounted`).
  Zero cancels at once, as a stop always did — and that handler's message is
  released too, where its nack used to be refused on the dead context
  (`TestADrain…`, `TestAStopWithNoDrainHandsTheInterruptedMessageBack`).
- **A handler that extends its own lease is not tracked.** The bound stays the
  lease it was handed and the outcome goes on that lease's void receipt, so
  such a handler parks its message and returns, as the mail spool does; one
  that must outlive its lease sets `AutoExtend` instead.
- **Every outcome past the stop is bounded by `SettleTimeout`** (zero is five
  seconds), on the consumer's clock, from the instant the consumer stops — a
  medium that stopped answering cannot hold a stop forever. The cut is
  `QUEUE_BACKEND_FAILED` naming the operation, which the pump reads as the
  stop it happened during (`TestAnOutcomeSentPastTheStopIsBoundedBySettleTimeout`).
  While the consumer runs an outcome is NOT timed, deliberately: nothing waits
  on it but the worker, which the medium's own failure reaches, and an idle
  worker's clock then holds no wait but its poll — what a test driving a
  `ManualClock` with `BlockUntil(1)` counts on. A renewal is always bounded.
- **A handler ends a margin before its lease.** Its context is cancelled with
  `LEASE_EXPIRED` as the cause at `ExpiresAt − margin`, the margin a tenth of
  the lease as granted, at most `SettleTimeout` — room for the nack to land
  while the lease holds. The lease is the broker's instant read on the
  consumer's clock: they must be one clock, and a lease that already looks
  ended on arrival — two clocks that disagree — is not bounded at all rather
  than never run (`TestAHandlerIsStoppedBeforeItsLeaseEnds`). A message of a
  batch whose lease is down to the margin when its turn comes is released, not
  started (`TestAMessageWhoseLeaseRanOutInItsBatchIsNotHandedToTheHandler`).
- **`AutoExtend` renews instead** (the heartbeat of SQS, JetStream's `+WPI`,
  Temporal's): every third of the lease while the handler runs, by the length
  the broker granted, moving the margin with it, and before a batch message
  starts once a third of its lease is spent. A refused renewal cancels the
  handler with the refusal as its cause. The engine acknowledges on the
  newest receipt (`TestAutoExtend…`, `TestARefusedRenewalStopsTheHandler`).

The watch is one goroutine per handled message (`oversee`), joined before the
outcome is sent; its timers — the margin, AutoExtend's ticker — are armed
before the handler starts, so a test sees them armed once it runs, and every
one is stopped before the outcome. When there is nothing to watch — no lease
to bound, no drain — the handler gets the consumer's own context, as before.
A test that waits for an idle consumer with `BlockUntil(1)` must therefore do
so once the handler has RETURNED: while it runs, its margin timer is armed
(`TestARetryIsDeliveredWhenItsDelayEndsNotAtTheNextPoll` waits for the nack's
wake first).

## Waking an idle consumer (ADR 0104)

`Consume` used to find work only by polling, so latency and idle cost were one
knob: a downstream framework ran a dozen consumers at 50 ms and paid ~3 % of a
core for nothing. Every broker here implements `core/data/queue.Waker`, an ADR 0039
sibling — `Broker` keeps its four methods — and `Consume` waits on it. The SQL
broker's due instant is recorded by its Receive, as the file broker's is, and
its signal is closed after the COMMIT of the publication it announces.

- **A signal, closed and replaced.** `Publish` and `Nack` close the channel
  every idle worker holds; closing is what makes it a broadcast, where a send
  would wake one receiver or nobody. A worker takes the channel BEFORE its
  `Receive`, so a publication landing between an empty `Receive` and the wait
  has already closed it — there is no lost wake-up window.
- **A due instant, because nothing happens when a retry becomes due.** A
  nacked message turns visible `RetryDelay` later and a dead consumer's lease
  lapses `VisibilityTimeout` later; no call marks either moment, so no signal
  can. `Wake().In` says how long until the earliest one, and the worker sleeps
  `min(PollInterval, In)`. The memory broker reads it from its ready list and
  its expiry heap under its lock; the durable broker cannot read a directory
  without a `Receive`, so each `Receive` RECORDS what its two scans stopped
  on — they already stop at the first future instant — and `Wake` reads the
  record. It can only be stale early (another process took the message, a
  lease was extended), which costs one empty `Receive`.
- **In is a duration, not an instant.** The broker and the consumer each take
  a clock, and nothing forces them to be one. A duration read on the broker's
  clock is waited on the consumer's; an instant compared across two clocks
  that disagree could be permanently in the past and spin the loop.
- **The durable signal belongs to the directory.** Two brokers over one `Dir`
  are one queue, so the signal is looked up in a process-wide table keyed by
  the directory resolved through its links, held WEAKLY with a
  `runtime.AddCleanup` that drops the entry once no broker references it. A
  publication from another process closes nothing; that is what the poll is
  for, and it is why `PollInterval` stays and becomes a bound.
- **Still no timer and no goroutine in a broker.** The wait is the consumer's;
  the broker only answers.

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

## A retry after the handler's delay, a message for later, a publication once (ADR 0183)

Three siblings, on all three brokers, each one statement, one rename or one
write beside what the broker already did:

- **`NackAfter` is `Nack` with another delay.** Each broker's `Nack` became
  `nackWith(wait)`: the policy's `retryDelay` for `Nack`, the caller's delay for
  `NackAfter`. The count, the burial on the last attempt and the wake are the
  same. A negative delay is now, and an instant no name or column carries is
  `QUEUE_MISCONFIGURED`, checked before the lease is touched (`nackDeadline`).
  `Consume`'s `settle` reads `RetryAfterOf` after `DoNotRetry` and after a
  stop, and nacks without the sibling.
- **`PublishAt` is `Publish` with a later visibility.** Memory: the record's
  `visibleAt`. File: the queued name's first field, which is what the
  directory sorts on. SQL: the `due` the INSERT already binds (`Publish` is
  `PublishAt` at the zero instant). A past instant is now, one past 2262 is
  refused (`publishVisibleAt`). The idle consumer's wake already reports the
  earliest due, so it sleeps until the instant.
- **`PublishOnce` reserves a key for a window.** A duplicate returns `false`
  and the zero message, never an error. The refusals are `onceUntil`'s: an
  empty key, a window that is not positive, or one past 2262.
  - Memory: `once` maps a key to its window's end, and `onceEnds`, a heap,
    drops passed ones at the next `PublishOnce`.
  - SQL: a row of the queue's own table, `dead = 2`, id the key's SHA-256 (64
    hex bytes, never a message identifier), `due` the window's end. The
    primary key is the unique constraint, so a table that predates this needs
    no migration. Every older statement names `dead = 0` or `1`, so none ever
    reads such a row: `TestSQLAReservationIsNoMessage`. One transaction, the
    caller's as a savepoint, so the reservation and the message commit or roll
    back together: `TestSQLAReservationExistsIfAndOnlyIfItsTransactionCommits`.
  - File: `once/`, a fourth directory under the states' rule. An account that
    could plant `once/<digest>.key` could silence that key:
    `TestTheDurableBrokerRefusesAStateDirectoryItCannotTrust`. The message is
    written first and the key reserved after, so a crash costs a duplicate and
    never a lost message. The reservation is written under `<digest>.<hex>.new`
    with `O_EXCL`, then `link(2)`ed to `<digest>.key`. A create-then-write was
    read empty, as passed, by a racing producer, which published too: three of
    eight producers published under `-race`
    (`TestProducersRacingOnOneKeyPublishOnce`). A producer that loses the link
    unlinks its message before waking anybody (`withdraw`). If a consumer
    leased it in that instant, the call reports a publication. A passed
    reservation is renamed to `<digest>.<hex>.gone`, read again, and put back
    by `link(2)` if it turns out to be running. The sweep runs at most once a
    minute per broker: it removes passed reservations, passed `.gone`
    leftovers and passed `.new` leftovers. It never removes a `.new` whose end
    it cannot read, since that may be another producer still writing. The
    reservation is not flushed: a power cut may forget it, which is a
    duplicate. `TestAFileReservationIsOneFilePerKeySweptOnceItsWindowPassed`,
    `TestAHalfWrittenReservationReadsAsPassed`,
    `TestTwoFileBrokersOverOneDirectoryShareTheirReservations`.

`TestSQLStatementsPerCall` pins the costs. `PublishAt` and `NackAfter` are one
statement each. `PublishOnce` is one transaction: `onceDrop`, `onceClaim`,
`insert` when claimed, `oncePick`, and `onceSweepRows` only when something
passed.

## Tests worth knowing about

- **`TestAKilledConsumerLosesItsLeaseAndTheMessageComesBack`** is the domain's
  central proof and it is not a `Nack` test. It re-executes this test binary as
  a child process, waits for the child to announce a lease, asserts that the
  PARENT (a different process) can see nothing, then SIGKILLs the child — no
  deferred function, no signal handler, no flush — and asserts the message
  comes back with `Deliveries == 2`. Every fact that recovery needs had to have
  been on the disk, because the process that knew them is gone.
  `TestTheVictimConsumer` is the child; it self-skips unless
  `KTNQ_VICTIM_QUEUE_DIR` is set, so it is discovered by `go test ./...` like
  any other test and needs no build tag, no `manual` target and no compensating
  lane (CLAUDE.md rule 12).
- **`TestASingleRetryReordersTheStream`** demonstrates the thing everyone
  discovers in production, on purpose, so it is documented rather than found.
- **The conformance suite is table-driven over EVERY broker** — memory, file,
  and SQL on each dialect's statements (`everyBroker`). A double nobody checks
  against the real thing is a double that has already drifted.
- **`sqlfake_external_test.go` is a SQL engine** that understands exactly the
  SQL broker's statements, as docstore's does its store's: the real pool,
  `*sql.Tx`, savepoints, `Join` and `Defer` above a table in a map, serialised
  transactions, PostgreSQL's aborted state, a statement log, and failures at a
  named statement. It verifies neither the SQL an engine accepts nor SKIP
  LOCKED; `sql_statements_external_test.go` pins the text, and
  `e2e/integration/sql` runs the broker on the three real engines under the
  `integration` tag — see its CLAUDE.md.
- **`TestSQLAPublicationExistsIfAndOnlyIfItsTransactionCommits`** is the SQL
  broker's reason to exist, and **`TestSQLStatementsPerCall`** what each call
  costs: one read for an idle Receive, one statement per Ack, Nack, NackAfter,
  Extend, Reject or Release, one INSERT per Publish or PublishAt, one
  transaction per PublishOnce.
- **`TestAStrayFileInTheQueueDirectoryIsNeverDelivered`** plants a
  `.vfs-<hex>.tmp` in `ready/` — the temporary a CONCURRENT atomic publication
  is genuinely writing there — and asserts the scan skips it. Delivering it
  would hand a consumer a truncated payload under a receipt naming a file about
  to be renamed away.

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
- Optimise `Publish/file`'s 41 allocations. They sit next to 3 ms of `fsync`;
  see `BENCH.md`.
- Range over the in-flight map in `memory.go` looking for expiries. That was
  the original code and it measured 105 µs per lease; the `kernel/collections/heap` min-heap
  that replaced it measures 1.83 µs. `BENCH.md` has both numbers.

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
