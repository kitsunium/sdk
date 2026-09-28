# ADR 0143 — a document keeps its versions in its own write

- **Status**: Accepted
- **Date**: 2026-09-28
- **Deciders**: SDK maintainers
- **Closes**: [ADR 0139](0139-a-document-store-over-sql-joins-the-transaction-its-context-carries.md) §D10 and §Deferred, for versions (side values stay deferred)
- **Related**: [ADR 0110](0110-a-document-store-writes-one-entry-and-rests-as-one-file.md) (the overlay entry a write publishes, the order-free replay), [ADR 0056](0056-sdk-vfs-domain.md) (the atomic publication), [ADR 0055](0055-sdk-sql-domain.md) (the transactions and savepoints), [ADR 0074](0074-what-a-public-alias-may-point-at.md) (no port, so the values are the engines'), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (the refused settings), [ADR 0040](0040-changing-a-published-shape-while-v0.md) (the fields added to two published configurations), [ADR 0090](0090-a-port-named-in-public-must-be-implementable-in-public.md) (the clock port the versions are stamped with, public); kitsunium/platform ADR 0007 §3 and "Implementation, in order", step 3

## Context

The framework built on this SDK (kit) records, in its ADR 0007, that "data
remembers": a field keeps its former values, and a record keeps its versions,
"as WordPress keeps them for its blocks" — the owner's demand. Steps 1 and 2 of
that record, a field's history and the password policy, needed no SDK change
and are built. Step 4, record revisions — `kit.Revisions(n)`, `Revisions`,
`Revision`, `Restore`, `Diff` — waits for step 3, which is this SDK's:

- `docstore` must keep the last n versions of each document in the same durable
  write as the document, and prune in that write;
- let its caller rewrite the versions under the writers' lock (an erasure, kit's
  ADR 0006);
- let its caller keep them from pruning (a legal hold);
- versions are never indexed: `Lookup` and `Find` read the current version;
- a version records its number — from 1, never reused — and when it was made,
  and kit adds who made it and which command: its `Revision{Number, At, By,
  Command, Value}`.

kit's own record rejects the alternative it would build without the SDK — a
sibling store of revisions — because it costs two durable writes per change
(about 22 ms instead of 11 on the measured Mac) and an erasure then rewrites
two stores in step. Its seams are ready: every write of a store that remembers
goes through one wrapper of its engine, which sees the value it replaces and
the caller's context.

What the SDK had. The file engine (ADR 0110) publishes ONE overlay entry per
write, holding the key's whole latest state, and folds the overlay into one
snapshot; replay is order-free and idempotent because an entry holds a key's
whole state. The SQL engine (ADR 0139) runs every write in one transaction —
a savepoint of the caller's — and numbers every write in `rev`; its §D10
sketched a third table keyed `(doc_key, rev)` for versions. Neither kept
anything but the latest document.

## Decision

### D1 — a version is a numbered state, and the current one is the document

A document's versions are its CURRENT version — the document itself, with the
number, the instant and the metadata of the write that made it — and up to
`Versions` FORMER ones, newest first. That is kit's `Revisions(n)`: n previous
versions, "a store with `Revisions(n)` holds up to n+1 times its documents".

- A creation is version 1.
- A write that changes the document makes the next version: the current one
  becomes the newest former one, holding the document it held, and the new
  document becomes the current version.
- A write that stores the JSON already stored makes none — kit's first
  exception. The file engine compares compact JSON, because a snapshot read back
  holds its documents indented.
- A write stamped `InPlace` (D2) makes none either — kit's second exception, a
  workflow's transition, which its journal already records. The document
  changes and keeps its current version's number, instant and metadata. A
  creation stamped `InPlace` is version 1 all the same.
- A number is never given twice while the document exists: pruning frees no
  number. A deletion takes every version with it, and a document inserted
  later under the key is a new document, numbered from 1 — keeping a number
  across a deletion would need a tombstone in both engines, and a deleted
  record's history is exactly what kit's ADR 0007 defers as a trash.
- A document stored before its store kept versions is version 1 made at an
  unknown instant (`At` zero, no metadata), and its first write makes version 2.

The numbers are the store's own, consecutive, in both engines. The SQL
engine's `rev` is not used: it counts every write, those that make no version
included, and it must, because MySQL's affected-row count reads it.

### D2 — a write says who made it: a stamp, on four new methods

`StampValue{Meta map[string]string, InPlace bool}` is what a write says about
the version it makes. `PutStamped`, `InsertStamped`, `ReplaceStamped` and
`UpdateStamped` take one, on both engines; `Put`, `Insert`, `Replace` and
`Update` are the same writes stamped with nothing, so a plain caller gets
versions stamped with the instant alone. `Meta` is the caller's — kit's `By`
and `Command` — kept with the version and never read. It is a map of strings
because every value a version needs is one, and a map of strings always
encodes: there is no refusal to define for it. On a store that keeps no
versions a stamp records nothing.

The stamp is a parameter, not a context value, because the file engine takes
no context (ADR 0110 §D6), and not a variadic option on the existing writes,
because widening `Put(v T) error` breaks every method value and every
interface a caller wrote over it.

### D3 — the versions travel in the document's own write

**File engine.** The overlay entry a write publishes holds the key's document
AND its versions, already pruned: `{key, doc, versions: {current, former}}`.
One atomic publication carries both, so after a crash the key is wholly before
the write or wholly after it — never a document without its versions or the
other way round. A deletion's entry takes both. Replay is still order-free and
idempotent: an entry still holds its key's whole latest state.

At rest the versions live in a file of their own, `Path + ".versions"`,
`{key: {current, former}}`, indented like the snapshot, which stays the bare
`{key: document}` object a framework's files already are. The fold publishes
the versions file, THEN the snapshot, and removes the entries only after both.
A crash between the two publications, or a publication refused, leaves every
entry in place; a key without an entry was not written since the last fold,
so it holds the same in the old files and the new — whichever file got
written, the next open loads the same documents with the same versions. Every
document and every version is kept in memory, as every document already was.

**SQL engine.** A third table, `<table>___vs` — `doc_key`, `num`, `made_at`
(nanoseconds since 1970 UTC, NULL when unknown), `meta` (a JSON object, NULL
when empty), `doc`, keyed `(doc_key, num)` — which `SQLVersionsMigration`
creates beside `SQLMigration`'s two, as ADR 0139 §D10 foresaw. The current
version's row holds NULL, because its document is the documents' table's; a
former version's holds the document it was. The version rows are statements of
the write's own transaction — a savepoint of the caller's — so a failure in any
of them rolls the document and its index rows back with them, and the caller's
transaction stays usable.

A Put on a store that keeps versions must read the document it replaces, and
no writer may land, or create the key, between that read and its write. So it
begins with a CLAIM: an upsert that leaves the stored document as it is and
moves its revision — `ON CONFLICT … DO UPDATE SET rev = rev + 1 RETURNING
rev, doc` on PostgreSQL and SQLite, `ON DUPLICATE KEY UPDATE rev = rev + 1` and
a locking read on MySQL — which creates the key or locks its row in one
statement. `Replace` and `Update` read under the lock they already took. The
version rows are read `FOR UPDATE` on PostgreSQL and MySQL: inside a caller's
REPEATABLE READ transaction on MySQL a plain read answers from the snapshot,
and would number a version already taken. SQLite serialises writers and needs
no clause.

What a write sends, pinned by `TestSQLVersionsRoundTrips`: a creation adds the
current version's row; a write that makes a version adds five statements — the
current number, read locked; its row given the document it held; the new row;
the question of what to prune; the pruning — four when there is nothing to
prune; a write that makes none asks what to prune. On PostgreSQL across Docker's
network an indexed `Put` goes from 1.89 ms to 3.79 ms, which is those round
trips; on SQLite, where they share one commit, from 145 µs to 215 µs
(`third-party/db/sql/BENCH.md`). In the file engine a write costs what its
versions weigh — 2.7 µs with none, 8.1 µs with ten former versions of a
hundred-byte document, 42 µs with a hundred — and nothing more on a disk, where
the entry's two flushes are still the cost (`internal/service/docstore/BENCH.md`).

### D4 — pruned in the write that makes a newer one, unless held

The former versions beyond `Versions` — the oldest — are pruned by the write
itself, in its entry or its transaction; nothing prunes later, and nothing
runs in the background.

`Held` keeps a document's versions from pruning: `func(key) bool` on the file
engine, `func(ctx, key) bool` on the SQL engine. It is asked only by a write
that would prune something, under the writers' lock or inside the write's
transaction with its context — so a framework reads its holds there, joined to
the same transaction when they live in the same database, and a hold cannot
land between the question and the pruning. While it answers true the versions
pile up beyond `Versions`; the first write after the release prunes them, a
write that makes no version included — kit's "pruned at its first write after
the release". A framework that cannot tell whether a hold applies answers
true, which prunes nothing. The SDK keeps no hold of its own: kit's holds are
records of its own, placed per record, per person, or until an instant a
record carries, and a copy of them in the SDK would be a second source of
truth to reconcile.

### D5 — an erasure rewrites the former versions, under the writers' lock

`RewriteVersions(key, fn)` hands `fn` copies of the former versions, newest
first, and keeps what it returns, in one durable write under the writers'
lock (the file engine) or in one transaction with the document locked as a
write locks it (the SQL engine). `fn` may drop a version and change what one
holds and what its writer said; it may not add one, reorder them, change a
number or an instant, or return JSON that is not JSON —
`VERSIONS_REWRITE_REFUSED`, naming the problem and never a version, and
nothing changes. `fn`'s own error is returned as it is. The current version is
not handed over: it is the document, which a write changes, with its indexes
and its hooks. A rewrite calls no hook, because the document did not change.

That is kit's erasure: the former versions cleared like the record, or dropped
— "so that neither Restore nor the Studio can bring back an erased value" —
after its key destruction already made them unreadable.

### D6 — reading versions

`Versions(key)` answers every version kept, newest first, the current one
first; `Version(key, n)` one of them, `VERSION_NOT_FOUND` when the document
keeps none of that number — never made, or pruned. `Version.JSON` is compact
JSON, a copy, and not a `T`: a version made before the type changed may no
longer decode into it, and a diff reads JSON. On the SQL engine the read is one
statement, a LEFT JOIN of the documents' table and the versions', on the
transaction the context carries. Versions are never indexed: `Lookup`, `Find`
and `Filter` read the current version, and a unique key a former version holds
is free.

A store that keeps no versions answers `VERSIONS_NOT_KEPT` to `Versions`,
`Version` and `RewriteVersions` — rather than the document as its one version,
which would tell a caller a history exists and is empty.

### D7 — turning versions on, lowering them, and turning them off

- **On.** Every stored document is version 1 at an unknown instant, until its
  first write. The file engine writes its versions file at its next fold; the
  SQL engine needs `SQLVersionsMigration` run first.
- **Lowered.** Each document is pruned to the new number at its next write,
  unless held.
- **Off, on the file engine.** A store opened with `Versions` zero over files
  that keep versions — the versions file, or an entry a crash left — is refused,
  `LOAD_FAILED` naming the file: it would neither maintain the versions nor write
  them back, so a deletion would leave a document's versions to the next
  document stored under its key, and the next fold would drop them all. The
  operator removes the file to drop them, or opens with `Versions`.
- **Off, on the SQL engine.** `OpenSQL` sends no statement and cannot see a
  versions table it was told nothing about. A store opened without versions
  never touches it; the migration's Down drops it. Opened with versions again
  over a table it left, a document deleted and written again in between would
  inherit the rows of the one it replaced — the one gap the SQL engine cannot
  close alone, stated here and in `SQLConfig.Versions`.

A store that keeps no versions writes exactly what it wrote before: the same
entries, no versions file, the same SQL statements — pinned by
`TestAStoreWithoutVersions` and `TestSQLRoundTripsPerCall`.

### D8 — three codes, in the range the store owns

- `0.3.80.18` `VERSIONS_NOT_KEPT` (400): a call on versions to a store that
  keeps none.
- `0.3.80.19` `VERSION_NOT_FOUND` (404): the document keeps no version of that
  number.
- `0.3.80.20` `VERSIONS_REWRITE_REFUSED` (400): a rewrite returned what a
  rewrite may not.

Two refusals already defined grow: `STORE_MISCONFIGURED` for a negative
`Versions` and for a `Held` on a store that keeps no versions (ADR 0031: a hold
on nothing is an inert setting the caller believes works), and `LOAD_FAILED`
for versions files and entries no store wrote — numbered 0, out of order,
without a document, of a document the store does not hold, beside a deletion
— or that the store does not keep. No refusal quotes a key, a document or a
version.

## Consequences / Semantics

- kit's revisions map onto the store: `Revision{Number, At, By, Command,
  Value}` is `Version{Number, At, Meta["by"], Meta["command"], JSON}`;
  `kit.Revisions(n)` is `Versions: n`; `Restore` is `Version` then
  `UpdateStamped`, a new version, refused like any write by a unique index;
  the workflow's transition is `ReplaceStamped` with `InPlace`; an erasure is a
  write clearing the record and `RewriteVersions` clearing the former versions;
  a hold is `Held`.
- A store with `Versions: n` holds up to n+1 times its documents, in memory for
  the file engine, as kit's record says.
- `pkg/v1/docstore` publishes `Version`, `Stamp`, `SQLVersionsMigration`, the
  three codes and sentinels, and the new methods and fields through its aliases.

## Breaking changes

None. The methods, the types, the migration and the codes are additions.
`Config` and `SQLConfig` gain `Versions`, `Clock` and `Held`: a keyed literal
compiles unchanged, and an unkeyed one — which `go vet` already reports across
packages — would not, under the v0 licence of ADR 0040. A store that keeps no
versions behaves, and writes, as before.

## Alternatives considered

- **A sibling store of revisions, kept by the framework.** kit's own fallback,
  rejected by its record: two durable writes per change and two stores for an
  erasure to rewrite in step.
- **Deltas instead of whole versions**, as Doctrine's Loggable keeps the changed
  fields. A version would then be rebuilt by replaying a chain, and one damaged
  entry would lose every older version; kit keeps whole versions, as WordPress
  does. A diff of two versions is computed when asked.
- **The versions inside the snapshot**, as `{key: {doc, versions}}`. The
  snapshot would stop being the bare object a framework's files are, and a
  store opened without versions would read versions as documents.
- **One file per version.** Two publications per write, so a crash could
  separate a document from its version: the one outcome step 3 forbids.
- **`rev` as the version number** (ADR 0139 §D10's sketch). It moves on writes
  that make no version, and the file engine has no such counter: the two
  engines would number the same history differently.
- **The stamp in the context, or as a variadic option** (D2).
- **A hold kept by the SDK** — `Hold(key)`, `Release(key)` — or a hold flag on
  each write. The first duplicates holds the framework already keeps, some of
  which depend on an instant a record carries; the second asks the framework
  to read its holds before every write rather than when something would be
  pruned (D4).
- **Dropping the versions when a store is opened without them.** A tool
  opened with the wrong configuration once would erase a history; refusing costs
  the operator one explicit removal (D7).
- **An age limit on versions** — kit defers it; a record's retention bounds its
  history.

## Deferred

- **What the versions weigh**, per store — kit's Studio wants it (its ADR 0007
  §5). A counter in `Stats` for the file engine, a query for the SQL engine,
  when the Studio asks.
- **A trash**: a deleted document's last version kept for some days. kit defers
  it too; it is the one thing that would need a number kept across a deletion.
- **Paged versions** for a held document whose history has grown: `Versions`
  reads them all.
- **Detecting a stale SQL versions table** (D7), which needs a statement at
  `OpenSQL` or a column in the documents' table.
- **Side values** in the same write (ADR 0139 §D10): an ordered index for kit's
  retention, still a sibling of `IndexSpec` of its own.

## Verification

- `internal/service/docstore`:
  - `version_external_test.go`, memory and file engines: numbers, instants,
    stamps and pruning; the same JSON and `InPlace` making no version; versions
    never indexed; a deletion taking them; a hold asked only when something
    would go, and released; the rewrite and its four refusals; a store without
    versions refusing their reads and writing its entry byte for byte as
    before; the refused settings.
  - `version_persist_external_test.go`: versions durable with their document and
    pruned in the write's own entry; a fold stopped after the versions file,
    before it, or with its entries left, loading the same documents and
    versions; a refused publication changing neither; the versions file and a
    reopen; a document stored before versions; files that keep versions a store
    does not keep, refused and left as they were; eleven files no store wrote.
  - `sql_version_external_test.go`, on the three dialects over the fake engine:
    the same contract; the claim, the locked reads and the pruning between one
    BEGIN and COMMIT, or inside one savepoint of the caller's; every version
    statement's failure rolling the document back; sixteen writers leaving
    consecutive numbers; a hold whose read joins the write's transaction.
  - `sql_statements_external_test.go`: the new statements and the versions
    table's DDL as text, per dialect.
- `pkg/v1/docstore`: `TestFacadeVersions`, through public names.
- `third-party/db/sql/docstore_versions_integration_test.go`, under `-tags
  integration`, on SQLite, PostgreSQL 17 and MySQL 8.4: the versions and their
  pruning counted in the table; a write over a versions table nobody created
  writing neither its document nor its index rows, alone or inside the
  caller's transaction; sixteen writers creating and updating one document; a
  hold read from the same database inside the write's transaction; the
  rewrite; a document stored before versions, read through the LEFT JOIN.
- `BENCH.md` in both places: the costs quoted in D3.

## References

- `internal/service/docstore/version.go`, `sql_version.go`, `sql_dialect.go`,
  `load.go`, `persist.go`
- kitsunium/platform `docs/adr/0007-data-remembers-its-versions.md` (§3 and
  "Implementation, in order", step 3), `0006-data-is-classified-field-by-field.md`
  (erasure, holds)
- WordPress revisions and `WP_POST_REVISIONS`, as kit's record cites them:
  https://wordpress.org/documentation/article/revisions/
- PostgreSQL `INSERT … ON CONFLICT … RETURNING`, `SELECT … FOR UPDATE`;
  MySQL 8.4 `INSERT … ON DUPLICATE KEY UPDATE` (affected rows), locking reads
  under REPEATABLE READ; SQLite UPSERT and `RETURNING` (3.35)
