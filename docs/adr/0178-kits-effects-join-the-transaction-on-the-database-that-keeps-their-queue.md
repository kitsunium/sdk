# ADR 0178 — kit's effects join the transaction on the database that keeps their queue

- **Status**: Accepted on 2026-10-14 by the owner, who retained the recommendation of each of the seven choices (C1 to C7), as written; no text changed but the status, each choice's *Decision* line, two facts #323 established (marked **corrected by #323**), C1's assumption about the spool, refuted after acceptance, with its remedy inserted in C7's order as item 4, and C6's assumption about the file engine's key order, refuted after acceptance by a measurement, with its remedy inserted as item 8. Implemented: C5 (PR 2 of C7's order, the subject in a list); C3 in #323 (PR 3, the monthly journal key); the SDK's items 4 and 5 in #325 (`sql.Aborter`, `mailspool.Config.Broker` and `Config.Deferrer`); C1 and C2 for topics, queued commands and watches in #327 (item 6); C1 for the mailer in item 7 — the outbox on the database that keeps the mailer's service, sealed under data-key, its mail written in the transaction; the SDK's tombstone, item 10 of C7's order, in #329 (ADR 0143 amended on 2026-10-15: `Trash`, `Restore`, `Purge`, `Tombstones`), which C4 — kit's item 11 — builds on; C4 itself, the trash, in item 11 (`StoreService.Untrash` and `Trashed`, the setting `<store>-trash`, `CodeTrash` 0.4.2.77). The other choices are decided and not implemented yet; each lands in the pull request C7 gives it.
- **Date**: 2026-10-14
- **Accepted**: 2026-10-14
- **Deciders**: SDK maintainers (issue #274); arbitrated by the owner
- **Depends on**: [ADR 0151](0151-a-message-published-in-a-transaction-exists-if-and-only-if-it-commits.md) (the SQL broker: a message published in a transaction exists if and only if it commits), [ADR 0139](0139-a-document-store-over-sql-joins-the-transaction-its-context-carries.md) (`Joiner`, `Deferrer`, every call runs where its context says), [ADR 0143](0143-a-document-keeps-its-versions-in-its-own-write.md) (versions, and the trash it deferred)
- **Related**: [ADR 0140](0140-sqlites-migration-lock-is-the-database-files-write-lock.md) (SQLite's write lock), [ADR 0147](0147-the-framework-is-a-module-of-the-sdk-above-pkg.md) (kit's engine is `framework/`), [ADR 0111](0111-a-mail-spool-retries-on-a-backoff-and-resends-only-after-a-crash.md) and [ADR 0141](0141-a-mail-spool-keeps-an-identifier-its-caller-minted.md) (the mail spool), [ADR 0112](0112-a-loop-that-must-keep-running-is-supervised-beside-the-lifecycle.md) (the supervisor), [ADR 0172](0172-a-product-initialises-what-it-imports-and-the-stores-stay-in-the-core.md), [ADR 0175](0175-a-products-tests-reach-the-disk-with-one-option.md) (`kittest.NewDisk`, which the tests of this work run on), [ADR 0040](0040-changing-a-published-shape-while-v0.md); kitsunium/platform#34 decision 4, kitsunium/platform#35, kodmain/moderation#16

## Context

kit's engine moved from files to a SQL database store by store (ADR 0139,
ADR 0143). Its queues did not. Issue #274 lists what is left; this ADR
designs the part of it that touches the transaction and what rests on
disk, and the order to ship it in.

Every line marked **(verified)** was read in the tree at `55c542da`
(`origin/main` on 2026-10-14). Every line marked **(assumed)** was not
checked, and says what would check it.

### What kit does today

- **A transaction holds every effect that leaves the process, and lets it go
  after the commit.** `hold` appends a `heldEffect` to the transaction
  (`framework/internal/kit/transact.go:397-413`); the outermost level's
  `end` closes the database scope — the commit — and only then calls
  `let`, which runs each effect in order and logs a failure without undoing
  the commit (`transact.go:237-265`, `transact.go:436-442`). A rollback runs
  the effects' `drop` (`transact.go:286-290`). The package doc says it out
  loud: "a process that dies between the commit and the release loses what
  was held" (`transact.go:55-56`). **(verified)**
- **Four effects of #274 go through it**: a topic's publication
  (`topic.go:52-74`), a queued command's dispatch (`queued.go:131-161`,
  whose `drop` gives the command's key back), a mail (`mailer.go:257-270`,
  the outbox ID minted before the commit), a watch's notice
  (`watch.go:272-289`). The privacy journal's entry is held the same way
  (`journal.go:72-90`). **(verified)**
- **Every queue is a file or memory queue.** `openQueue` builds
  `queue.NewMemory` without a data directory and `queue.NewFile` with one
  (`consumer.go:46-61`). No call to `queue.NewSQL` exists in `framework/`.
  **(verified)**
- **Part of #274's adoption list is already done**: `openQueue` sets
  `MaxRetryDelay` (`consumer.go:47`); a queued command and a watch
  dead-letter an undecodable message with `queue.DoNotRetry`
  (`queued.go:189-195`, `watch_run.go:92-96`); both consumers run under
  `lifecycle`'s supervisor (`queued.go:268-273`, `watch_run.go:53`); the
  SQLite connector opens every file with `_busy_timeout=5000`,
  `_journal_mode=WAL` and `_txlock=immediate`
  (`framework/connectors/sqlite/sqlite.go:29-33`), parameters the pinned
  driver honours (`modernc.org/sqlite@v1.59.0/sqlite.go:307,328,385`).
  **(verified)**
- **The mail spool builds its own queue.** `mailspool.New` makes a memory
  broker without `Config.Dir` and a file broker with it
  (`internal/service/app/mail/spool/spool.go:101-105`); its `Config` has no
  field that takes a broker. The spool writes each mail as JSON — the
  addresses, the subject, the body — in that queue (`spool.go:22-32`): the
  outbox under `<data>/<service>/outbox/<mailer>` (`mailer.go:435-438`) is
  personal data in clear. **(verified)**
- **A topic's and a queued command's members kit seals are sealed for a
  queue on disk** (`seal_message.go:62-80`, `seal.go:519`: `sealsOnDisk`
  reads the data directory). A watch's notice carries no value
  (`watch_run.go:19-27`). **(verified)**
- **The journal seals every entry that carries a reason under a data key of
  its own.** A `journalRecord` has no subject tag (`journal.go:37-50`), and a
  record without a subject is sealed under its own key, `"r:"` and its
  reference (`seal_store.go:291-302`). Every entry with a reason therefore
  files one more wrapped key in `kit.privacy/store/keys` — a hold placed,
  `EraseFields`, an erasure or a deletion on request —, and the journal is
  never pruned. **(corrected by #323)** This line first said that every
  `Records.Get` a module makes (`records.go:153`) files a key, so that a
  watch reading each record it is told of files one per notice. It does
  not: a read is journaled without a reason, so nothing of it is sealed and
  no key is made. #323 measured it: 20 `EraseFields` with a reason and 20
  `Get` in one month add 20 keys before C3, and 1 after.
- **A subject in a list is refused in every type a declaration carries.**
  `judgeSubject` refuses a subject reached through a list or a map
  (`classify_plan.go:180-188`), and the type judge runs on every type of
  every declaration — an endpoint's request and response included
  (`classify_walk.go:275-277`, `classify_walk.go:331-344`). A subject is
  read only by stores and by message sealing (`seal_message.go:71`,
  `seal_store.go:297`, `privacy_keys.go:224`, `hold.go`). Vigie works around
  the refusal with JSON written by hand in a `personal` field
  (kodmain/moderation#16). **(verified)**
- **Records has `Get`, `EraseFields` and `Delete`, no way to list**
  (`records.go:86-124`). **(verified)**
- **A deletion takes every version with it**, and a trash was deferred
  because it needs a number kept across a deletion — a tombstone in both
  engines (ADR 0143, lines 70-74 and 422-423). An age limit on versions was
  deferred too: "a record's retention bounds its history" (ADR 0143,
  line 396). **(verified)**
- **A transaction belongs to the database its first store call reaches.**
  `joinCall` claims the transaction for that database and refuses a write
  to another (`transact_sql.go:114-145`). **(verified)**
- **kit checks no server version.** Neither `database_check.go` nor the
  MySQL connector reads `VERSION()`. **(verified)** ADR 0151's broker needs
  `SKIP LOCKED`: MySQL 8.0.1 or MariaDB 10.6 (ADR 0151, "does NOT
  guarantee" 3); docstore's floor is MySQL 5.7.9 and MariaDB 10.2 (ADR 0139
  §D9). **(verified in the ADRs)**

## Decision

Seven choices. Each states the question, the options with their concrete
cost, the recommendation, and the option the owner retained on 2026-10-14
— in every choice, the recommendation.

### C1 — how the outbox adopts `queue.NewSQL`, and what becomes of holding

**Question.** A queue on SQL makes a publication atomic with the
transaction — but only with a transaction on the same database. A kit
transaction belongs to the database of its first store call, a queue has one
table on one database, and a topic's subscriptions or a watch's feeding
stores may be kept by different databases (`Keeps`). Where does each queue
live, and what becomes of `hold` at the four call sites?

**Options.**

- **A. Each queue lives on the database that keeps its consumer**, by the
  rules a store follows (`database_place.go:47-80`: by name, service,
  module, default database; the data directory, then memory). A
  publication whose transaction belongs to that database is written in it;
  any other — a transaction on another database, on the data directory, in
  memory, or no transaction — keeps today's path (held, released after the
  commit). *Migration*: a queue table per consumer, created by kit's own
  migration set as kit's other tables are (`store_sql_migrate.go`, one
  generation-numbered version per table); messages waiting in file queues
  must be drained or copied (C7, `data copy`). *Compatibility*: nothing
  changes for a product with no database; the common product — one default
  database — gets every effect atomic. *Security*: the queue table holds
  sealed payloads as a file queue does (C1 consequences). *Performance*: one
  `INSERT` per subscription in the transaction, in a savepoint (ADR 0151
  §D2); the consumer polls its table every 5 s (`consumer.go:26`) besides the
  in-process wake.
- **B. One outbox table per database, relayed into the consumers' queues.**
  Every publication, whatever its target, is one row in `kit_outbox` of the
  transaction's database; a relay loop moves rows into each consumer's
  queue. *Cost*: always atomic, but two writes and a second hop per
  message, a relay that can itself duplicate, one more loop to supervise,
  and the latency of its poll. ADR 0151 rejected exactly this shape in the
  SDK ("an outbox table relayed into the file broker").
- **C. One queue per consumer and per publishing database.** Always atomic;
  a consumer reads N brokers, tables multiply as consumers × databases, and
  the Studio draws N queues for one subscription.

**And holding, under A.** The release becomes two phases in one place,
`unit.end`:

1. **In the transaction, just before the commit**: the outermost level's
   held effects whose queue lives on the transaction's database are
   published in its scope (`u.scope.ctx`), before `u.scope.close(nil)`. A
   publication refused there fails the transaction — the outbox's own
   semantics: the message and the write stand or fall together. Because the
   flush happens at the commit, a publication made *before* the
   transaction's first write still joins it; nothing about the order of a
   handler's calls matters, and a publication never claims the transaction
   for a database (it would turn a later write elsewhere into
   `TRANSACTION_SPAN`).
2. **After the commit**: everything else, as today — a queue on another
   database, the data directory or memory, and every effect that stays in
   the process (`inside`: a store's write hooks, a workflow's hooks, a
   topic's loop wake, the queued command's key).

At the call sites:

- `put` (`queued.go:131-161`): the effect names its queue's database and
  keeps its `drop` — the rollback still gives the key back. The in-process
  `pending` set stays in-process: with a queue shared by several processes it
  deduplicates within one process only, as today.
- `Publish` (`topic.go:52-74`): one held effect per subscription instead of
  one for all, each tagged with its queue's database; `t.wake()` stays an
  `inside` effect after the commit (the SDK's `Deferrer` wakes the
  consumer's queue itself, ADR 0151 §D2).
- `enqueue` (`mailer.go:248-277`): see C1's SDK change below.
- `notify` (`watch.go:272-289`): one held effect per watch, tagged likewise.
  The stale paragraph "The gap" (`watch.go:36-42`, which still says the
  notice is queued outside the transaction) is rewritten.

**The SDK change the mailer needs.** `mailspool.Config` grows a `Broker`
field (an addition, ADR 0040), used instead of `Dir` when set; and the
spool's `EventQueued` — today told when `Send` returns — is told through the
transaction's `Deferrer`, after the commit, when `Send` ran inside one, so
kit's mailbox never shows a mail a rollback took back. Design first
(`design/app/mail.yaml`, `kit gen`), which only the owner can run. **(assumed:
that the spool's announcement can be deferred without breaking ADR 0141's
"a delivery waits for its announcement"; to be proved by the SDK PR's
tests.)**

**(refuted, as the SDK stands — measured after acceptance.)** Deferring
`EventQueued` alone breaks ADR 0141. A `Send`'s registration
(`internal/service/app/mail/spool/spool.go:273`) must stay open until the
commit: otherwise the consumer, woken by the wake the SQL broker holds
(`internal/service/data/queue/sql.go:365`) before the held functions run,
can announce `sent` before `queued` between the settle and the held
functions (`internal/service/data/sql/tx.go:97-102`). But a rollback calls
nothing (`txstate.go`, `drain(false)`; `tx.go:80-81`): the registration
stays open forever and, the identifier being reusable (ADR 0141 D3), every
later delivery of that identifier waits for ever — with `Parallelism: 1`
the whole spool stops (a temporary test: `sent=[]` after 2 s).

**The remedy retained (way A).** A cancellation signal in `core/data/sql`,
a sibling interface of `Deferrer` (ADR 0039), told when the transaction
rolls back; it amends ADR 0139. The spool closes its registration there,
and ADR 0141 is unchanged. The spool reaches the `Deferrer` and its sibling
through a second optional field, `mailspool.Config.Deferrer` — never by
probing the `Broker`. It is its own pull request, before the spool's (C7,
item 4). Ways set aside: **B**, a registration per `Send` — it replaces ADR
0141 D4 and leaves one entry in memory per cancelled `Send`; **C**, giving
up the order `queued` before `sent` — it weakens ADR 0111 D5 and ADR 0141
D4.

**Recommendation: A, with the two-phase release.** It makes the product kit
is built for — one default database — atomic end to end with no new loop
and no new table shape, keeps one mechanism (`hold`) instead of four, and
degrades to exactly today's behaviour where atomicity cannot exist. B is the
right answer only for a product whose publishers and consumers sit on
different databases, which `Keeps` makes possible but no product we know
does; it can be added later behind the same tag without changing a call
site. The analyser should warn when a declared publishes/delivers edge
crosses two databases (the graph has both ends), so the non-atomic case is
said at the declaration rather than discovered after a crash.

**Decision.** A, with the two-phase release — the outbox inside the
transaction — by the owner's decision, 2026-10-14.

### C2 — the floors: MySQL `SKIP LOCKED`, SQLite's busy timeout

**Question.** The SQL broker needs MySQL 8.0.1 or MariaDB 10.6; an older
server fails every lease with `QUEUE_BACKEND_FAILED` (ADR 0151). SQLite
answers busy at once without a busy timeout. What does kit require, and
when does it say so?

**Options.**

- **A. One floor for every MySQL database kit opens, checked at start.**
  `SELECT VERSION()` once, at the database's start, refused below
  8.0.1 / MariaDB 10.6 with a diagnostic naming the version found.
  *Cost*: a product on MySQL 5.7 (end of life October 2023) or MariaDB
  10.5 (end of life June 2025) stops starting even if it publishes nothing.
  One round trip per start.
- **B. The floor only where a queue lives on the database.** Same check,
  refused only when C1 places a queue there. *Cost*: a product may start on
  an old server and fail later, at its first `Keeps` change that moves a
  queue there; two floors to document.
- **C. No check.** The broker's error at the first lease, in a supervised
  consumer that restarts forever. *Cost*: an outage that looks like a
  transient failure.

For SQLite, the busy timeout already exists (5 s, `sqlite.go:32`), and a URL
may write its own. The open question is its relation to kit's own bound:
`<name>-timeout` defaults to 5 s too (`transact_sql.go:196-203`). With both at
5 s, a call can reach its context's deadline exactly as SQLite gives up.

**Recommendation: A for MySQL; for SQLite, a busy timeout strictly under
`<name>-timeout`, by a fixed rule.** One floor is one sentence in the docs and
one refusal at start, and both servers below it are out of support. PostgreSQL
needs no floor beyond docstore's.

The SQLite rule, with `T` the resolved `<name>-timeout`:

- **The margin** is `m = max(250 ms, T / 10)`, and the busy timeout kit gives
  the engine when the URL writes none is `B = T − m`: 4.5 s for the default
  `T` of 5 s, 27 s for 30 s, 250 ms for 500 ms. `B < T` holds for every `T`,
  so a lost race on the write lock comes back as `SQLITE_BUSY` — a storage
  failure the supervisor restarts the consumer after — before the call's
  context ends, never as a cancelled context indistinguishable from a slow
  caller. The margin is what the call still needs once the lock is granted:
  250 ms covers a statement on a local file, and a tenth of `T` keeps the
  ratio for long timeouts.
- **A `T` below 500 ms is refused at start for a SQLite database**
  (`B` would fall under the 250 ms margin itself), with a diagnostic naming the
  setting and the floor.
- **A busy timeout the URL writes itself** (`_busy_timeout` or `_timeout`, the
  two spellings the driver reads) **that is `≥ T` is refused at start**, with
  a diagnostic naming the database, the value the URL gives and the bound it
  must stay under (`database.sqlite-busy-timeout`). kit never rewrites it: a
  value the operator wrote and kit silently replaced would be an inert
  setting, which ADR 0031 refuses. A URL value under `T` is kept as written.
- The connector is what writes the parameter today (`sqlite.go:29-33`), and it
  does not know `T`: kit hands `B` to it through a sibling of `Engine`
  (`database_interface.go`, sibling interfaces, never a method), so an engine
  that does not implement it keeps its own default and the check above still
  runs on the URL it is given.

**Decision.** A for MySQL — MySQL 8.0.1+ or MariaDB 10.6+, checked at
start — and, for SQLite, the busy timeout `B = T − max(250 ms, T / 10)` with
its two start refusals, by the owner's decision, 2026-10-14.

### C3 — the journal's key: its own, or derived

**Question.** One data key per journal entry that carries a reason grows
`kit.privacy/store/keys` by one wrapped key per such entry, forever (a read,
journaled without a reason, adds none — corrected by #323). What does the
journal seal under?

**Options.**

- **A. A journal key of its own, one per calendar month**, `"j:2026-10"`, a
  data key like the others: 32 random bytes, wrapped under `data-key`,
  filed in the keys store, re-wrapped by `data-key`'s rotation. *Cost*:
  twelve keys a year; a new key reference kind. *Migration*: none — every
  box names its key (`seal_erase.go:228-243`, `keyOfBox`), so entries sealed
  under their own key keep opening, and their keys stay until a later
  clean-up folds them. *Security*: destroying a month's key is a
  cryptographic erase of that month's reasons — the journal's own retention,
  when one is decided, costs one destruction, and the hash chain still
  verifies (the chain hashes before sealing, `journal.go:29-31`).
  **(corrected by #323)** The month is the UTC month of the entry's
  instant, read on the app's injected clock — "a calendar month" named no
  time zone.
- **B. A key derived from `index-key` or `data-key` by HKDF.** No storage.
  *Cost*: a derived key cannot be destroyed without destroying its parent;
  derived from `data-key`'s keyring, every rotation needs `InUse` to keep
  each version the journal needs, which is every version ever.
- **C. Seal under `data-key` itself** (a `root:` box, as a message without
  subject is sealed). *Cost*: `data-key` keeps three versions over 30-day
  rotations (`seal.go:40-46`): a reason older than ~90 days stops opening
  unless `InUse` pins every version, which defeats the rotation.

**Recommendation: A.** It is the only option whose growth is bounded, whose
erasure is possible and whose rotation needs nothing new; the period is a
month so that a journal retention, when the owner sets one, has a grain
finer than a year and coarser than a day.

**Decision.** A — a journal key of its own per month, `"j:YYYY-MM"`, the
month being the UTC month of the entry's instant on the app's injected clock
— by the owner's decision, 2026-10-14. Implemented in #323 (PR 3 of C7's
order).

### C4 — the trash: a default age limit, and its value

**Question.** ADR 0143 deferred a trash — a deleted document's last version
kept for some days — and an age limit on versions. Does kit keep one by
default, for how long, and what does privacy do with it?

**Options.**

- **A. A trash for every store with `Revisions(n)`, 30 days by default**,
  a setting `<store>-trash` (0 turns it off). A deletion by the product
  keeps the record's last version and its versions for 30 days, restorable;
  a deletion kit makes for privacy — retention's `DeleteAfter`, an erasure,
  `Records.Delete` — bypasses the trash, and a person's erasure shreds what
  the trash holds because it is sealed under the same key. *Cost*: the SDK's
  docstore needs the tombstone ADR 0143 deferred, in both engines — an SDK
  PR, design first; a store with revisions holds deleted records for a
  month (space); a re-insert under a trashed key continues its numbering.
- **B. A trash opt-in only (`kit.Trash(d)`), no default.** *Cost*: the same
  SDK work for a feature most products will not turn on; an accidental
  deletion is unrecoverable by default, as today.
- **C. 7 days by default.** Shorter exposure; too short to notice a
  deletion made on a Friday by a job nobody watches.

The age limit on versions stays deferred, as ADR 0143 argued: the count
bound `n` and the record's retention already bound a history, and an age
pruning needs a sweep or an agenda per store.

**Recommendation: A, 30 days, and only for stores that already keep
revisions.** A store that asked for history is the one whose owner expects
to undo; 30 days is WordPress's trash, the model `Revisions` already
follows, and the privacy bypass keeps retention and erasure exactly as
strict as today. **(verified in item 11: WordPress's documentation of
`wp-config.php`, "Empty Trash" — `EMPTY_TRASH_DAYS` "controls the number of
days before WordPress permanently deletes posts, pages, attachments, and
comments"; "The default is 30 days".)**

**Decision.** A — a 30-day trash, `<store>-trash`, for stores with
`Revisions(n)` only, privacy's deletions bypassing it — by the owner's
decision, 2026-10-14.

**Implemented (item 11).** What the product sees:

- `Store.Delete` on a store with `Revisions(n)` moves the record to the
  SDK's trash (`docstore.Trashable` / `TrashableContext`); on any other
  store, or with `<store>-trash` at `0`, it deletes at once, as before.
- `StoreService.Untrash(ctx, key) (T, error)` brings it back as it was —
  versions kept, no version made; `NotFound` when nothing is trashed under
  the key, `Conflict` when another record took one of its unique keys
  (the SDK frees them), `Invalid` on a store without revisions. The name is
  WordPress's (`wp_untrash_post`): `Restore` already writes a version back.
  `StoreService.Trashed(ctx) ([]TombstoneEvent, error)` — `kit.Tombstone`:
  `Key`, `At`, `PurgeAt` — lists the trash in key order.
- The length is a setting kit declares with the store's revisions,
  `<store>-trash` (the store's name lower-cased, `_` and `.` as dashes; a
  duration, 30 days by default; negative refused at start; a name no setting
  may take refused at start). No code option: the length is the operator's.
- **No sweep.** What is past due is purged when the app starts, the rest at
  its own instant on the app's injected clock: an agenda per store, filled
  from `Tombstones` at start and from each deletion, a loop of kind
  `retention` named `<store> trash` that sleeps on the earliest instant —
  the retention loop's shape (ADR 0006 §5). A record another process
  trashed joins the agenda at the next start; one written again or restored
  meanwhile passes its instant doing nothing.
- **Privacy.** A retention's deletion, `Records.Delete`,
  `DeleteOnErasure` and an erasure call the engine's `Delete`, never `Trash`.
  `kit.Erase` first purges from the trash every record whose subject is
  being erased — opened to read it — and reports each in the store's
  `Deleted`, journaled as a deletion with the erasure's reason; then the
  person's key goes, as before. A trashed record past its retention's
  instant is purged then, before its 30 days (`PurgeAt` says which).
  While a record is in the trash its person's data key stays — a
  restoration must open it (`versionsSealUnder` reads the trash) —, and its
  own key too; the purge destroys both once nothing else needs them,
  journaled as `shred`. The product's deletion and the purge are not
  journaled, as a product's deletion never was.
- **A unique key** is freed by the SDK at the trash: another record may take
  it, and `Untrash` then answers `Conflict`, the record staying in the trash.
- In a transaction on the data directory or in memory, a rollback of a
  trash restores the record with its versions — where a rolled-back
  deletion used to bring it back without them —, a rollback of an untrash
  trashes it again; on a database the transaction does both.
- A store that also keeps former values (`history=N`, a password policy)
  keeps them while the record is in the trash, and drops them at the purge.
- `CodeTrash` (0.4.2.77, `TRASH`): the trash could not be read or emptied;
  the record stays in it and the loop tries again after a backoff.

### C5 — a `subject` in a list

**Question.** Vigie's endpoints return lists of records whose type has a
subject, and kit refuses the response type. A subject identifies the one
person a *record* or a *message at rest* is about; it decides a data key.
Does it mean anything in a list?

**Options.**

- **A. Refuse a subject in a list only where kit reads one identity**: a
  store's entity, a topic's message and a queued command's input — the
  types that rest and are sealed. Accept it in an endpoint's request and
  response, a query's result, a synchronous command's result and an
  authenticator's types, where the field is personal data for redaction and
  nothing else. *Cost*: the judge learns which role a type plays (it
  already knows the declaration it judges, `classify_walk.go:313`); a type
  used both as a store's entity and inside a response is judged per role.
  *Security*: redaction is unchanged — a subject is personal data wherever
  it sits.
- **B. Keep refusing; document `wire.Records[T]`** or a projection type
  without the tag. *Cost*: every product that lists records writes a second
  type, and Vigie's hand-written JSON in a `personal` field stays.
- **C. Accept a list of subjects everywhere**, sealing a message under
  `data-key` itself when it names several people. *Cost*: a person's erasure
  no longer shreds the messages that mention them among others — a
  weakening of ADR 0006's promise for a convenience.

**Recommendation: A.** The refusal protects a decision — which key seals
what rests — that a response never makes; keeping it where that decision is
made and lifting it where it is not removes Vigie's workaround without
weakening any erasure.

**Decision.** A — the subject targeted: refused in a list only where kit
reads an identity — by the owner's decision, 2026-10-14. **Implemented**
(PR 2): a type's plan keeps a subject inside a list or a map apart from its
other problems (`classPlan.resting`, `classify_plan.go`), and each
declaration says which of its types rest (`dataType.rests`,
`classify_walk.go`): a store's entity, a topic's message and a queued
command's input refuse it, said once per type at the first declaration where
the type rests; an endpoint's request and response, a query's input and
result, a synchronous command's input and result and an auth handler's
types accept it. A subject in a list is no longer counted as a type's second
subject. Redaction is unchanged. `TestASubjectInAListIsRefusedOnlyWhereKitReadsAnIdentity`
holds each place.

### C6 — `Records.Keys`, and the watches' catch-up at start

**Question.** A module has no way to list a store's records, and a watch
cannot recover the notices a crash lost where C1 cannot make them atomic
(a feeding store on another database than the watch's queue, the data
directory, memory). What does `Records.Keys` expose, under which
confidentiality, at what engine cost, and how does the catch-up use it?

**Options for the shape.**

- **A. `Keys(ctx) iter.Seq2[string, error]`**, every key in the store's key
  order, read in pages of a few hundred underneath. *Exposure*: the keys
  only, which a module already receives one at a time in
  `WrittenEvent.Key`; enumeration is new. *Engine cost*: the file engine
  keeps its documents in memory, so a page is a sorted slice; the SQL engine
  is `SELECT id … WHERE id > ? ORDER BY id LIMIT ?` on the primary key.
  **(assumed: that the file engine's key order is reachable without copying
  every document; to check in `internal/service/data/docstore`.)**
  **(refuted, as the SDK stood — measured after acceptance.)** kit's file
  engine reaches the SDK's `docstore.Store` only through what it exports, and
  none of it lists keys alone: `List` and `Filter` decode every document,
  `Entries(limit)` copies each document's JSON it returns
  (`internal/service/data/docstore/docstore.go`, `Entries`), and it has no
  `after`, so walking it page by page re-reads every page before. The cheap
  path — the sorted keys under the read lock (`sortedKeys`) — existed, but
  unexported. Measured on `cf51fe67` (Go 1.27.2, AMD EPYC 7351P, 8 threads),
  the store in memory, which the file engine reads the same way:

  | Listing the keys | Documents × size | ns/op | B/op |
  |---|---|---|---|
  | `Entries(0)`, the only exported way | 1 000 × 100 B | 0.33 ms | 221 KB |
  | | 1 000 × 4 KiB | 2.2 ms | 4.96 MB |
  | | 10 000 × 100 B | 5.5 ms | 2.5 MB |
  | | 10 000 × 4 KiB | 19.1 ms | 49.9 MB |
  | | 100 000 × 100 B | 55 ms | 27.3 MB |
  | `Entries(limit)` in pages of 256 | 10 000 × 100 B | 155 ms | 62 MB |
  | the sorted keys alone (unexported) | 10 000 × 100 B | 2.6 ms | 666 KB |
  | | 10 000 × 4 KiB | 2.8 ms | 666 KB |

  The memory a listing took grew with what the documents weigh, and paging
  made it quadratic; the keys alone cost the same whatever the documents
  weigh. The SQL half was not marked, and did not hold either: the SDK's
  `SQLStore` exported no keyed page — its one (`pageAfter`, for `Reindex`)
  reads `doc` too, and is unexported. **The remedy (C7, item 8)**: the SDK
  grows a read of the keys alone in both engines, sibling ports of
  `Collection` and `CollectionContext` (ADR 0039) — `KeyLister.Keys(after)`
  and `KeyListerContext.Keys(ctx, after)`, an `iter.Seq2[string, error]` in
  byte order resuming after the key named. The file engine takes the keys
  above `after` once, under the read lock, when the range starts, and sorts
  them — the map's own strings, no document copied; the SQL engine reads
  pages of 256 with `SELECT doc_key … WHERE doc_key > ? ORDER BY doc_key
  LIMIT ?`, each resuming after the last key read. After it, the same
  measure of `Keys("")` ranged to the end gives 10 000 documents in 2.3 ms
  and 164 KB at 100 B as at 4 KiB, in two allocations, and 100 000 in 28 ms
  and 1.6 MB. A document a store no longer holds is not listed: a trashed one
  (item 10) leaves the documents in both engines, so it is not either.
  `Records.Keys` (item 9) is written on that read.
- **B. `Keys(ctx, after string, limit int) ([]string, string, error)`**, the
  page made explicit. Same cost; the caller writes the loop.
- **C. `Changed(ctx, since Cursor)`**, the keys written since a cursor.
  Exact catch-up, but each engine needs a change sequence it does not keep
  today (the SQL engine's `rev` counts writes per row, not a store-wide
  order — ADR 0143), a schema change in both engines and a migration.

**Confidentiality, for A or B.** `Keys` is offered only on a store that
feeds one of the calling module's watches (the mark already decides what a
module may hear, `watch.go:181-190`) — any other store answers `Forbidden`.
A key can itself be personal data (an email as a key): each call — not each
key — journals one `list` entry, under C3's journal key, naming the store and
the caller.

**Catch-up.** At a watch's start, after a stop that was not clean — a
marker kit writes at start and removes at a clean stop — kit queues one
notice per key of each feeding store whose notices could not be atomic,
through `Keys`. A clean stop costs nothing; a crash costs one notice per
record of those stores, which the watch's handler already tolerates (at
least once, idempotent). A command, `<product> watch replay <watch>`, does
the same on demand.

**Recommendation: A, offered to watching modules only, and catch-up on an
unclean stop.** C is the precise tool, but it changes both engines for a
case C1 makes rare; A costs one method and is what a full re-scan needs
anyway.

**Decision.** A — `Records.Keys(ctx) iter.Seq2[string, error]`, offered to
watching modules only, one journal entry per call, and the catch-up on an
unclean stop — by the owner's decision, 2026-10-14.

### C7 — `data copy`, the pull requests, and their order

`data copy --to <database>` copies a product's data directory into a
database: stores, their histories and versions, kit's own stores but the
data keys when the target is SQLite (they never live there,
`database_place.go:40-46`), and the waiting and dead messages of each file
queue C1 moves. It runs with the product stopped, refuses a target whose
tables hold rows, and copies sealed boxes as they are (no key changes
hands). It comes last because it must copy what the other pull requests
place.

**A copy is declared done only when the fingerprints match, never on a
count.** Two equal counts say nothing about a truncated document, a version
written under the wrong number or a payload cut short. After writing, `data
copy` reads source and target back through the engines and compares:

- **Each document**: `SHA-256(store ‖ key ‖ bytes)`, each part
  length-prefixed, where `bytes` is the document as the store keeps it —
  sealed members as boxes, never opened. Both engines keep the document as
  written (ADR 0139 §D3), so a faithful copy has the same bytes.
- **Each version**: `SHA-256(store ‖ key ‖ number ‖ instant ‖ metadata ‖
  bytes)`, the metadata in sorted-key JSON — a renumbered or re-stamped
  version differs.
- **Each store**: a running SHA-256 over its records' digests in key order,
  which both engines read in that order. Equal store digests end the check
  for that store; unequal ones make `data copy` walk both sides again to
  name the first key that differs, and fail without marking the copy done.
- **Each queue**: the SQL broker mints its own message identifiers, so a
  message's identity cannot be compared; each message is
  `SHA-256(payload ‖ dead ‖ deliveries ‖ reason ‖ code)`, and the queue's
  digests are compared as a sorted multiset — the same messages, the same
  number of times, in any order.

*Cost*: one more full read of each side, streamed — constant memory for the
stores, 32 bytes per message for the queues' sort (32 MB for a million
waiting messages); SHA-256 runs at hundreds of MB/s per core, so the check
costs about what the copy's own read cost. *What it does not prove*: that
the boxes open under the target's keys — the keys are copied as boxes too,
and a sample of records is read through kit, opened, as the last step.
**(assumed: that a dead letter can be written into the SQL broker as dead,
with its reason and count; `DeadLetterManager` replays or deletes, it does
not insert — to check before PR 12, and an SDK addition if not.)**

**Options for the split.**

- **A. Small PRs, independent ones first.**
- **B. One framework PR after the SDK ones.** Easier to review as a whole,
  impossible to bisect, and blocks Vigie's fix behind the outbox.

**Recommendation: A, in this order.**

1. This ADR.
2. C5 — the subject in a list (framework only; unblocks Vigie).
3. C3 — the journal's key (framework only; stops the keys store's growth).
4. SDK — the transaction's cancellation signal in `core/data/sql`, a
   sibling of `Deferrer` (ADR 0039), amending ADR 0139 (design first;
   inserted after acceptance, C1's refuted assumption).
5. SDK — `mailspool.Config.Broker`, `mailspool.Config.Deferrer` and the
   deferred `EventQueued`, the registration closed on a rollback, after 4
   (design first, `kit gen`; released on its own as `pkg/v1/app` and
   `internal`).
6. C1 + C2 — kit's queues on SQL for topics, queued commands and watches;
   the two-phase release; the MySQL floor; the SQLite busy timeout tied to
   `<name>-timeout`; message sealing extended to SQL queues.
7. C1 for the mailer — the outbox on SQL, sealed (below), after 5.
8. SDK — a read of the keys alone in both engines of docstore,
   `KeyLister` and `KeyListerContext` (design first; inserted after
   acceptance, C6's refuted assumption).
9. C6 — `Records.Keys` and the watches' catch-up, after 8.
10. SDK — docstore's tombstone (design first).
11. C4 — the trash, after 10.
12. `data copy`, last.

**Decision.** A — small pull requests, in this order, `data copy` verified
by fingerprints — by the owner's decision, 2026-10-14. Item 4 was inserted
after acceptance, when C1's assumption about the spool was refuted (C1);
the items after it moved down by one. Item 8 was inserted the same way,
when C6's assumption about the file engine was refuted by a measurement
(C6); the items after it moved down by one again.

**Sealing the mail outbox (item 7).** kit seals the spool's payload in a
broker that wraps the one C1 gives the spool — the way `wrapQueue` already
stands in front of a queue in tests (`consumer.go:57-60`) — under `data-key`
itself: a mail names no subject kit can read, and a dead letter older than
`data-key`'s three kept versions stops opening, which suits mail. The
alternatives were the sending user's key (a system mail to a person would
not follow that person's erasure) and sealing hooks in the spool's `Config`
(an SDK surface for one consumer).

## Consequences / Semantics

- The product with one default database gets every topic publication,
  queued command, mail and watch notice atomic with its writes; the
  documented loss "between the commit and the release" remains only where a
  queue and the transaction are on two different places, and the analyser
  says where.
- A publication refused inside the transaction now fails the transaction,
  where today it is logged after a commit that stands. That is a behaviour
  change of `kit.Transact`'s doc (`transact.go:49-56`), rewritten in PR 6.
- `queue.SQLMigration` tables join kit's set and its registry; the
  database check's rule that two of kit's tables never take one name
  (`database_check.go:17-18`) covers them.
- A MySQL older than 8.0.1, or a MariaDB older than 10.6, no longer starts.
- The keys store stops growing with the journal; the journal can be given a
  retention later at the grain of a month.
- A store with revisions keeps deleted records 30 days unless its setting
  says 0.

### Risks

- **The flush at commit runs product code paths later than today**: a
  publication's encoding stays at the call, but its `INSERT` happens at the
  end of the transaction, so a slow queue table lengthens the transaction.
  Measured in PR 6 against `transact_test.go`'s timings.
- **Sealing an SQL queue reads `sealsOnDisk`**, which checks the data
  directory (`seal.go:519`). A product whose queues are all on a database
  and which has no data directory would seal nothing. **(assumed: that a
  product with databases always has a data directory outside dev; to check
  in `app.go` and `database.go` before PR 6.)**
- **The deferred `EventQueued`** interacts with ADR 0141's waiting for an
  announcement — no longer a risk but a fact: deferred alone, a rolled-back
  `Send` leaves its registration open and stalls every later delivery of
  its identifier (C1, refuted). The cancellation signal of item 4 is the
  answer; the spool's PR (item 5) proves it with a rollback test.

### Verified and not yet proved

Verified by reading the code at `55c542da`: every **(verified)** line of the
Context, with its file and line. Not proved, and owned by the PR that needs
it: two **(assumed)** lines — the data directory always present with a
database, a dead letter written as dead by `data copy`. WordPress's 30 days
was verified in item 11 (C4). A fourth, the file engine's key order, was **refuted** after
acceptance by a measurement (C6): no exported read of either engine listed
the keys without the documents, and the remedy is the SDK's `KeyLister`
(C7, item 8). A fifth, the spool's deferred
announcement, was **refuted** after acceptance (C1): a rolled-back `Send`
holds its registration open for ever, and the remedy is a cancellation
signal beside `Deferrer` (C7, item 4). Corrected by #323: a read is
journaled without a reason and files no key (Context), and C3's month is
the UTC month on the app's clock.

## Breaking changes

None so far: C5, the one choice implemented, only accepts types that were
refused. The pull requests still to come change:

- **`kit.Transact`'s outcome** (PR 6). Today a held publication, dispatch,
  mail or notice that its queue refuses after the commit is logged and
  reported, and the commit stands (`transact.go:49-56`, `transact.go:436-442`).
  Once its queue is on the transaction's database, the refusal happens
  inside the transaction, before the commit, and `kit.Transact` returns it
  with every write of the transaction rolled back. A caller that treated a
  nil return as "written, effects maybe lost" now gets an error where it got
  nil; one that relied on writes standing whatever the queue said must
  handle it. The doc of `kit.Transact` is rewritten in the same PR. Where
  the queue is elsewhere, the outcome is unchanged.
- **Start refusals** (PR 6): a MySQL older than 8.0.1, a MariaDB older than
  10.6, a SQLite `<name>-timeout` under 500 ms, and a SQLite URL whose busy
  timeout reaches `<name>-timeout` no longer start.
- **Deletion** (PR 11, implemented): on a store with `Revisions(n)`, a
  product's deletion keeps the record restorable 30 days unless
  `<store>-trash` is 0. Its versions stay readable through `Revisions`
  meanwhile; a record inserted again under its key continues their
  numbering instead of starting at 1; its person's data key is destroyed at
  the purge, not at the deletion; the app declares one more setting per
  such store, `<store>-trash`, so a product setting of that name, or two
  services' stores with revisions and the same name, now refuse the start
  (`setting.twice`); and the running app has one more component, `trash`,
  and one more loop per such store.
- C3 and C5 break nothing: entries sealed under their own keys keep
  opening, and C5 only accepts types that were refused.

## Revision history

| Version | Date | What changed |
|---|---|---|
| Proposed | 2026-10-14 | The design, merged in #320 |
| Accepted | the owner's decision, 2026-10-14 | The recommendation of each choice retained as written; a *Decision* line per choice. C5 implemented in the same pull request. Two facts #323 established written in: a read is journaled without a reason and files no key (the Context's growth claim corrected), and C3's month is the UTC month of the entry's instant on the app's injected clock. C1's assumption about the spool's deferred `EventQueued` refuted (a rolled-back `Send` keeps its registration open): the remedy, a cancellation signal in `core/data/sql` beside `Deferrer` (amends ADR 0139) reached through `mailspool.Config.Deferrer`, inserted as item 4 of C7's order; ways B and C set aside |
| Accepted, item 7 | 2026-10-15 | C1 for the mailer implemented (C7 item 7): the outbox on the database that keeps the mailer's service, `kit_queue__<service>__<mailer>__outbox`, each mail sealed under data-key bound to the mailer by a broker in front of the SQL one; a mail sent in a transaction on that database written in it before the commit, the database's transaction manager handed to the spool as `Config.Deferrer`; the Status line names the items implemented so far |
| Accepted, item 11 | 2026-10-15 | C4 implemented: the trash of a store with `Revisions(n)` — `Store.Delete` trashes, `Untrash` and `Trashed` (`kit.Tombstone`), the setting `<store>-trash` (30 days, 0 off), purged at start and at each record's instant on the app's clock, privacy's deletions and erasures bypassing it, an erasure purging the person's trashed records, `CodeTrash` 0.4.2.77; WordPress's 30 days cited; the Status line says so |
| Amended | 2026-10-15 | C6's assumption about the file engine's key order refuted by a measurement (every exported listing copied the documents, paging was quadratic, and the SQL engine exported no keyed page): the remedy, a read of the keys alone in both engines of docstore (`KeyLister`, `KeyListerContext`), inserted as item 8 of C7's order; the items after it renumbered |

## Alternatives considered

The options of each choice above, with why the recommendation was
preferred. Two more were set aside before the choices:

- **Keeping file queues and adding a crash-recovery scan for every
  effect.** It would recover watches (C6) but not publications, commands or
  mail, which carry values no scan can rebuild.
- **Moving the held effects into the SDK's transactor** (a `Deferrer` for
  everything). The SDK's `Deferrer` runs after a commit (ADR 0139 §D1); kit's
  transactions also exist on the data directory and in memory, where no
  SDK transactor runs.

## Deferred

- **An outbox relayed per database** (C1 option B), for a product whose
  publishers and consumers sit on different databases; addable behind the
  same held-effect tag without changing a call site.
- **`Records.Changed(since)`** (C6 option C), an exact catch-up, which needs
  a store-wide change sequence in both engines.
- **An age limit on versions** (C4), deferred again as ADR 0143 deferred it.
- **The trash in an export and in the Studio** (C4): `kit.Export` reads a
  person's records through the subject index, which a trashed record has
  left, so it does not carry their trashed records yet; the Studio shows no
  trash. Both read `Trashed` and `trashedValue` when they come.
- **A retention for the journal**, which C3's monthly key makes one
  destruction per month once the owner sets its length.
- **Cross-process deduplication of a queued command's key**: the `pending`
  set stays in one process.
- The rest of #274 that this ADR does not design: the oldest `data-key`
  version index, `privacy seal` on every store, the versions' weight and the
  revisions in the Studio and the static graph, and the modules' items.

## References

- Issue #274, and kitsunium/platform#35's hand-over comment it reproduces.
- `framework/internal/kit`: `transact.go`, `transact_sql.go`, `queued.go`,
  `topic.go`, `mailer.go`, `watch.go`, `watch_run.go`, `consumer.go`,
  `journal.go`, `seal.go`, `seal_store.go`, `seal_message.go`,
  `seal_erase.go`, `records.go`, `classify_plan.go`, `classify_walk.go`,
  `database_place.go`, `store_sql_migrate.go`.
- `internal/service/app/mail/spool/spool.go`.
- `framework/connectors/sqlite/sqlite.go`; `modernc.org/sqlite` v1.59.0,
  `sqlite.go` (the DSN parameters it reads).
- MySQL 8.0 Reference Manual, `SKIP LOCKED` (8.0.1); MariaDB 10.6 release
  notes, `SKIP LOCKED`.
