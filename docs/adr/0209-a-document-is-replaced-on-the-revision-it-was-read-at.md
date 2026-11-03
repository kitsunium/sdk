# ADR 0209 — a document is replaced on the revision it was read at

- **Status**: Accepted
- **Date**: 2026-11-02
- **Deciders**: SDK maintainers
- **Amended**: 2026-11-02 — several writes as one, `Batcher` / `BatcherContext` (see the dated section at the end); 2026-11-03 — the documents a page at a time and an ordered index, `Pager` / `PagerContext`, `Ranged` / `RangedContext`, `Ordered` and `INDEX_NOT_ORDERED` `0.3.80.24` (see the second dated section)
- **Amends**: nothing published but two sibling ports of
  `core/data/docstore` (ADR 0039), the value `RevisionValue`, the code
  `0.3.80.23` `REVISION_MISMATCH`, and their aliases in `pkg/v1/data/docstore`.
- **Related**: [ADR 0110](0110-a-document-store-writes-one-entry-and-rests-as-one-file.md)
  (the file engine, its snapshot the bare `{key: document}` object),
  [ADR 0139](0139-a-document-store-over-sql-joins-the-transaction-its-context-carries.md)
  (the SQL engine, its `rev` column),
  [ADR 0143](0143-a-document-keeps-its-versions-in-its-own-write.md)
  (a replacement makes a version), kitsunium/sdk#1055, #958, #996

## Context

A caller that reads a document, shows it to a person and writes it back a
minute later has two writes today, and neither fits. `Replace` overwrites
whatever landed in between: the person's edit silently undoes another's.
`Update` is atomic, but its function runs under the file engine's writers'
lock or the SQL engine's row lock, so it cannot span a request. Every SQL
row already carries a `rev` that moves on each write; no port returns it.
`app/statemachine` (#958) and the framework's stores (#996) each need a
compare-and-swap the store does not offer, and would write one per engine.

## Decision

**D1 — two siblings, the ports frozen.** `Revisioned[T]` — `Revision(key)
(T, RevisionValue, error)` and `ReplaceIf(v T, rev RevisionValue) error` —
and `RevisionedContext[T]`, the same with a context first. `Store[T]`
implements the first, `SQLStore[T]` the second; `docstore_compliance.go`
asserts both. `Collection` and `CollectionContext` are not widened (ADR
0039).

**D2 — the token is opaque and an engine's own.** `RevisionValue` is a
string compared only for equality.

- **File engine**: the SHA-256, in hexadecimal, of the document's JSON as
  `json.Marshal` writes it — compact and HTML-escaped. A write stores exactly
  that; a fold writes it indented, an operator may leave it unescaped, and
  both name the same revision. Nothing is persisted: the snapshot stays the
  bare `{key: document}` object ADR 0110 promised, and the token holds across
  a fold, a reopen and the memory store. A write that stores the same JSON
  keeps the token, and so does a document written back to what it was (A, B,
  A): what the caller replaces is then exactly what it read, so no write is
  lost.
- **SQL engine**: the row's `rev`, in decimal. Every write moves it, one that
  stores the same JSON included: a mismatch is never a lost write, and may be
  a write that changed nothing.

**D3 — `ReplaceIf` is `Replace` on a condition.** It writes what `Replace`
writes — the document, its index keys, its version, `OnWrite` — when the
stored document's revision is still `rev`. File engine: the comparison runs
under the writers' lock, which the write then keeps. SQL engine, without
versions: one `UPDATE … WHERE doc_key = ? AND rev = ?`; with versions, which
read what they replace: `lockDoc` on the same condition (`FOR UPDATE`, or on
SQLite the write that takes the lock), then the write under it. When no row
matched, the row is read again with `exists` — MySQL's shared lock reads past
a REPEATABLE READ snapshot — to say which refusal it is.

**D4 — the refusals.** `REVISION_MISMATCH` (`0.3.80.23`, HTTP 409) when the
document is there with another revision; `DOCUMENT_NOT_FOUND` when there is
none — a deleted or trashed document is never brought back by `ReplaceIf`.
`Revision` of a missing or trashed key is `DOCUMENT_NOT_FOUND`. A token the
engine never gave — another engine's, or garbage — matches no document. No
refusal names the key or the token.

## Consequences

- A caller holds a document across a request and writes it back only if
  nobody else did; on `REVISION_MISMATCH` it reads again and decides again.
- Both engines answer the same contract (`revision_external_test.go`: memory,
  file, and SQL on the three dialects, with and without versions; eight
  replacements racing one token have one winner).
- The tokens of the two engines are not comparable, and a token does not
  survive a copy from one engine to the other: it is a read's, not a
  document's.

## Breaking changes

None. Two ports, a value, a code and a sentinel are added.

## Alternatives considered

- **Exposing `rev` as a number.** The file engine persists no counter, and
  persisting one would change the snapshot ADR 0110 keeps as the bare object
  a framework's files already are.
- **A conditional `Update`.** It would still hold the lock while the caller
  decides; the point is not to.
- **Multi-key compare-and-swap, a stamped `ReplaceIf`.** Later siblings, when
  a caller needs them.

## References

- `internal/core/data/docstore` (`Revisioned`, `RevisionedContext`,
  `RevisionValue`, `RevisionMismatch`)
- `internal/service/data/docstore/revision.go`, `sql_revision.go`,
  `revision_external_test.go`, `sql_statements_external_test.go`
  (`TestSQLTheRevisionStatements`)

## Amendment (2026-11-02): several writes as one

**Why (kitsunium/sdk#1057).** On the SQL engine two writes under one caller
transaction commit or roll back together (ADR 0139 §D5); on the file engine a
write is one entry (ADR 0110 §D2), so two related documents — an order and
its stock line, a user and the reservation of its e-mail — could be left
half-written by a crash between them, and kit compensated by writing former
values back, which makes one more version and no atomicity. A bulk import paid
two flushes per document.

**Decision.** Two more siblings and the writer they hand out, the ports
frozen as in D1:

- `BatchWriter[T]` — `Put`, `Insert`, `Replace`, `Delete` — records a batch's
  writes in order. Each call prepares its document at once, outside every
  lock, and answers at once only what the document alone decides
  (`DOCUMENT_KEY_EMPTY`, `DOCUMENT_UNENCODABLE`, `KEY_TOO_LONG`). What the
  store decides — `DOCUMENT_EXISTS`, `DOCUMENT_NOT_FOUND`, `UNIQUE_KEY_TAKEN`
  — is decided when the batch commits, in order, each write seeing the
  batch's own writes before it; the first refusal refuses the batch, nothing
  changed. fn's own error is returned as it is, nothing written.
- `Batcher[T]` — `Batch(fn)` — on `Store[T]`, `BatcherContext[T]` on
  `SQLStore[T]`. Each write makes its version and files its index keys as it
  would alone; `OnWrite` / `OnDelete` are told about every write, in order,
  once the whole batch took effect.

**SQL engine.** One transaction — a savepoint of the caller's, or one of the
store's own — each write sending the statements it sends alone (`writeIn`,
shared with the single writes); a refusal or a failure rolls the whole batch
back, a collision asked about after the rollback as a single write's is. No
new statement.

**File engine.** Under the writers' lock the batch is planned over a view —
the store, and under each key the batch already wrote its state — with the
unique keys the batch frees and takes counted; versions computed as a single
write computes them (`nextVersionsFrom`; `Held` asked under the writers' lock
only, never under the read lock). Then:

1. ONE group entry, `<overlay>/batch.group.json`, published by
   `WriteAtomic` — whole or not at all — holds every written key's final
   state as its own entry would. Refused: nothing changed.
2. The batch is applied to the maps: it is durable.
3. Each key's own entry is published, then the group entry is removed.
4. When step 3 does not finish, the batch is still applied and `Batch`
   returns nil — the group entry covers it —, and the store keeps it as
   lingering: the next write, or batch, completes it BEFORE publishing
   anything, and refuses with `PERSIST_FAILED` while it cannot; a fold, whose
   snapshot holds it, removes the group entry and ends it. So no key's entry
   is ever newer than a group entry on disk.

`load` replays the keys' entries — order-free, ADR 0110 §D3 unchanged — then
a surviving group entry over them, each record checked as an entry is, and
marks both pending, so the fold at `Open` absorbs it and removes it. The
group entry's name is no key's digest: a release that predates batches
ignores it, as it ignores any file of somebody else's. A memory store plans
and applies, and publishes nothing.

**Verification.** `batch_external_test.go`: on every engine, a batch whose
last write is refused changes nothing and tells no hook; a batch sees its own
writes (an insertion then a replacement, a unique key freed then taken, a
deletion then a creation, versions numbered as alone); every refusal in its
place; on SQL a batch inside the caller's rolled-back transaction leaves
nothing. File engine: a crash after the group entry reopens with the whole
batch, one before it with none, and a write after an incomplete batch
completes it first, so the reopen keeps the newer write.

**Out of scope, and what it does not buy.** Batches across stores; a batch
larger than memory; the coalesced flush #803 brings. A file-engine batch buys
atomicity, not speed: it pays its group entry, then one publication per key,
then the removal — more than the same writes alone. Paying the keys' entries
once per batch needs #803's commit group, or a fold that absorbs the group
entry instead of the per-key entries; neither is this change, and no
`BENCH.md` row claims otherwise.

## Amendment (2026-11-03): the documents a page at a time, and an ordered index

**Why (kitsunium/sdk#836).** `List` returned every document, `Entries` a
first page with no cursor, and only the keys could be paged (`KeyLister`);
both index kinds were equality on strings, so "the orders between two dates"
decoded every document through `Filter`. A framework filtering a table on
SQL read it whole (#1153).

**Decision.** Two more siblings, frozen as in D1, and a third index kind:

- `Pager[T]` — `Documents(after, limit) iter.Seq2[EntryValue, error]`, the
  documents above a key with their JSON, and `FindAfter(index, key, after,
  limit) iter.Seq2[T, error]`, Find a page at a time — on `Store[T]`;
  `PagerContext[T]` on `SQLStore[T]`. `limit` caps what is yielded; not
  positive, every one.
- `Ranged[T]` — `Range(index, lo, hi, limit) iter.Seq2[T, error]`: the
  documents under a key in `[lo, hi)` — no upper bound when `hi` is empty —
  in index-key order, then store-key order, decoding only what it yields;
  `RangedContext[T]` on `SQLStore[T]`.
- `Ordered(name, key)` declares the index Range reads: `IndexSpec` gains
  `Ordered bool`, added last so a field-by-field declaration still compiles.
  Keys compare as bytes; a caller pads numbers and writes instants in RFC
  3339 UTC. Range of an index declared without it is `INDEX_NOT_ORDERED`
  `0.3.80.24` (HTTP 400).

**File engine.** Nothing persisted changes: indexes live in memory. An
ordered index keeps a sorted list of (index key, store key) beside its maps,
moved by every write by binary search, and built once at `Open` (sorted
after every loaded document is filed, not insertion by insertion). A page of
`Documents` is chosen in one pass over the keys keeping at most twice the
page — never a sort or a copy of the store's keys —, taken under the read
lock, its JSON copied outside it.

**SQL engine.** No column, no migration, no new index: an ordered index's
rows are the index table's rows, and its primary key `(index_name,
index_key, doc_key)` answers `FindAfter` and `Range`. `Documents` sends
Reindex's `pageAfter`. Range resumes on the (index key, document key) of the
last row, written without a row value and bounded below on the index key
alone too, so the scan starts at the cursor; every placeholder appears once,
in order, for MySQL. Each page is read whole and its rows closed before the
first of it is yielded, so a caller may write in its loop. `IndexKey` applies
to an ordered index's keys and to Range's bounds as to any lookup: a
transform that does not keep their order leaves Range none to read.

**Verification.** `page_external_test.go`: six rows of the conformance table
on memory, a filesystem and the three dialects (each document once by pages
of 100, a write during a listing, FindAfter equal to Find, Range's bounds and
ties across pages, Range after every kind of write, the refusals); a page
allocates the same at 1 000 and 10 000 documents; Range and FindAfter decode
what they yield and nothing else; the sorted list across a reopen; one
statement per page, and the statements pinned as text. `BENCH.md` of the
service package gains the page and pass rows.

**Out of scope.** A cursor for Range inside one key's run of documents
(Range resumes by bounds); Tombstones and Versions by pages; a `Where`
pushed to SQL by the framework (#1153's second step).

