// Package docstore — the store's construction parameters, and the refusals a
// configuration no store could honour gets, the index declarations' included:
// an IndexSpec is core/data/docstore's, which both engines take (ADR 0160).
//
// Package docstore is a typed, keyed store of JSON documents with secondary
// indexes, kept in memory and, when given a filesystem, persisted to it —
// every write durable before it returns. ADR 0110.
//
// # What it is
//
// A Store[T] holds documents of one Go type, each under the key a function of
// the document returns. Every read returns a copy and every write stores one:
// a document is kept as its JSON encoding, so what a caller holds can never
// alias what the store holds, and the memory and file backends behave alike.
// Writes come in three modes — Put creates or replaces, Insert refuses an
// existing key, Replace refuses a missing one and never resurrects a deleted
// document — plus Update, a read-modify-write, and Delete.
//
// Secondary indexes are declared at Open: Unique files at most one document
// per key and refuses a write that would file a second, Index files any
// number. Lookup reads a unique index, Find reads any index, Filter reads
// every document. The indexes are rebuilt when the store opens, and a load
// whose documents break a unique index is refused rather than served. So is
// a load holding a document the type no longer decodes, or one stored under
// another key than its own Key gives: a store opens only over documents it
// can serve.
//
// # How it persists, and what that costs
//
// The resting state is ONE file, the snapshot: a JSON object mapping each key
// to its document. A write does not rewrite it. It publishes one small file —
// an overlay entry, named by the SHA-256 of the key, holding the key and its
// document or its deletion — atomically, through vfs.AtomicWriter, before it
// returns. A write therefore costs what one document costs, whatever the
// store holds. When the overlay holds as many entries as the store holds
// documents (at least DefaultFoldAt), the write that reaches that count folds
// it: the snapshot is rewritten once and the entries it now contains are
// removed. Folding N documents once per N writes keeps the average write a
// constant. Close folds, so a store that was closed rests as a single
// snapshot a person can read.
//
// Loading reads the snapshot and replays the overlay on top of it. Replay
// order does not matter — one entry per key, each holding that key's whole
// latest state — and an entry the last fold already folded holds exactly what
// the snapshot holds, so a crash anywhere in a fold, or between a fold and
// the removals that follow it, loads the same documents.
//
// # Concurrency
//
// Readers never wait for a disk. Writers are serialised by one lock and do
// their filesystem work under it alone; the documents and the indexes are
// changed only once the write is durable, under a second lock readers share.
// A reader sees the state before the write or after it, never a document and
// an index that disagree.
//
// # What it is not
//
// One process. The store detects no second process opening the same files,
// and two would each believe their own memory. It keeps every document in
// memory and sorts the keys for every List. It has no transaction across
// documents. It is a store for the documents a service owns, not a database.
//
// # The same store over SQL
//
// OpenSQL builds the second engine, SQLStore, for when a store must outgrow
// that: the same documents, keys, index declarations, write modes, hooks and
// refusals, kept in two tables of a PostgreSQL, MySQL or SQLite database the
// caller owns and hands over as a core/data/sql Transactor. Every call takes a
// context and runs on the transaction it carries — a write in a savepoint of
// it, its hooks after its commit — and nothing is held in memory. ADR 0139.
//
// Package docstore — hosts the compile-time interface assertions, keeping them
// out of the production source so the runtime binary carries no
// diagnostic-only declarations.
//
// Package docstore — the functions told about a write or a deletion once it
// is durable, outside every lock.
//
// Package docstore — the secondary indexes, kept beside the documents under
// the store's own locks.
//
// An index is rebuilt from the documents when the store opens, and every
// write — Put, Insert, Replace, Update, Delete — maintains it in the same
// critical section that changes the document. A reader therefore never sees a
// document and an index that disagree, and a write a unique index refuses
// leaves both exactly as they were.
//
// Package docstore — opening a persistent store: the snapshot and the
// versions file read, the overlay replayed on top, and the resting state
// restored.
//
// Package docstore — opening a store: the configuration checked, the indexes
// declared, and a persistent store loaded.
//
// Package docstore — the files: one snapshot at rest, and an overlay of one
// entry per key written since, folded back into the snapshot.
//
// Package docstore — the SQL store's construction parameters, and the
// refusals a configuration no SQL store could honour gets.
//
// Package docstore — the only place the SQL store renders SQL. Every statement
// it sends is built here, once, at OpenSQL, for its dialect and its tables,
// spelled with the vocabulary core/data/sql's Dialect owns: the bind markers, the
// quoting and the row lock.
//
// Package docstore — the SQL store's tables, as a migration the caller runs
// under its own version table.
//
// Package docstore — the SQL store's index rows rebuilt from its documents,
// for when the declarations that filed them changed.
//
// Package docstore — the SQL store: the same documents, keys, indexes and
// refusals as Store, kept in a database the caller owns, each call on the
// transaction its context carries (ADR 0139).
//
// Package docstore — the SQL store's versions: a third table, written by the
// statements of the write that stores the document, in its transaction
// (ADR 0143).
//
// Package docstore — the SQL store's writes: each one atomic on its own, in a
// transaction of the store's or a savepoint of the caller's, and announced
// once the transaction that holds it has committed.
//
// Package docstore — a document's versions: the current one, which is the
// document itself, and the former ones kept beside it, recorded and pruned in
// the write that stores the document (ADR 0143).
//
// Package docstore — the writes: prepared outside every lock, checked and
// persisted under the writers' lock, applied under both.
package docstore
