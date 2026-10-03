//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/data/docstore .

// Package docstore is the public facade for the SDK's document store: typed,
// keyed JSON documents with unique and multi-valued secondary indexes, kept in
// memory or persisted to a filesystem, every write durable before it returns.
//
//	type Account struct {
//	    ID    string   `json:"id"`
//	    Email string   `json:"email"`
//	    Teams []string `json:"teams"`
//	}
//
//	data, err := vfs.NewOS("/var/lib/app") // or vfs.NewMem(), or nil FS for memory
//	accounts, err := docstore.Open(
//	    docstore.Config[Account]{
//	        Key:  func(a Account) string { return a.ID },
//	        FS:   data,
//	        Path: "members/accounts.json",
//	    },
//	    docstore.Unique("email", func(a Account) string { return a.Email }),
//	    docstore.Index("team", func(a Account) []string { return a.Teams }),
//	)
//	defer accounts.Close()
//
//	err = accounts.Insert(Account{ID: "acc_1", Email: "ada@example.com"})
//	ada, err := accounts.Lookup("email", "ada@example.com")
//	red, err := accounts.Find("team", "red")
//
// # Documents, keys and the write modes
//
// A document is kept as its JSON encoding, so every read returns a copy and a
// caller can never alias what the store holds. Its key is what [Config].Key
// returns; an empty key is refused. Put creates or replaces; Insert refuses a
// key that is taken (DocumentExists); Replace refuses a key that is not
// (DocumentNotFound), so a document deleted meanwhile is never brought back.
// Update applies a function to a copy and stores the result atomically with
// respect to every other write — its function may read the store, and must not
// write to it. Delete removes.
//
// # Indexes
//
// [Unique] files at most one document per key: a write that would file a
// second is refused with UniqueKeyTaken and changes nothing. [Index] files any
// number. Empty keys are not indexed. Lookup reads a unique index; Find reads
// any index, in store-key order, and answers an empty slice rather than an
// error; Filter reads every document. The indexes are rebuilt when the store
// opens, and documents that break a unique index refuse the open
// (IndexBroken) rather than answer lies.
//
// No refusal ever quotes a key: a store key is routinely an e-mail address, an
// index key the hash of a token.
//
// # How it persists, and what a write costs
//
// At rest the store is ONE file, the snapshot — a JSON object from key to
// document, the file a person can open. A write publishes one small file
// beside it, in the directory Path + ".d", atomically, before it returns — so
// a write costs one document, whatever the store holds. When that overlay
// holds as many entries as the store holds documents (at least
// [DefaultFoldAt]), the write that got it there folds it back into the
// snapshot: one rewrite per N writes, a constant on average. Close folds too.
// Opening replays the overlay over the snapshot, and a crash at any point of a
// fold loads the same documents. BENCH.md in internal/service/data/docstore has the
// numbers: a write at 100 000 documents costs what it costs at 100, where
// rewriting the whole file costs 70 ms.
//
// A publication that fails is PersistFailed and changes nothing. One whose
// rename happened but whose directory flush failed is WriteUnconfirmed: the
// write stands — the file shows it, and so does the store — and only its
// survival across a power loss is in doubt.
//
// # Concurrency, and what it is not
//
// A Store is safe for concurrent use, and a reader never waits for a disk:
// writers are serialised among themselves and do their filesystem work alone,
// and the documents change only once a write is durable. OnWrite and OnDelete
// tell a caller about each write once it is durable, outside every lock.
//
// It is one process's store: a second process opening the same files is not
// detected. It keeps every document in memory. It has no transaction across
// documents. It is a store for the documents a service owns, not a database.
//
// # The same store over SQL
//
// [OpenSQL] keeps the same documents — the same keys, indexes, write modes,
// hooks and refusals, the same codes — in two tables of a PostgreSQL, MySQL or
// SQLite database the caller opened and hands over as a [sql.Transactor].
// Nothing is held in memory: the database is the source of truth, and every
// call is a round trip that takes a context (ADR 0139).
//
//	tm, err := sql.NewTransactor(sql.Config{DB: pool, Dialect: sql.DialectPostgres, Pool: sql.PoolConfig{MaxOpen: 10}})
//	create, err := docstore.SQLMigration(sql.DialectPostgres, "members__accounts", 20260927120000) // run by your Migrator
//	accounts, err := docstore.OpenSQL(
//	    docstore.SQLConfig[Account]{Key: func(a Account) string { return a.ID },
//	        Transactor: tm, Dialect: sql.DialectPostgres, Table: "members__accounts"},
//	    docstore.Unique("email", func(a Account) string { return a.Email }),
//	)
//	err = sql.Transact(ctx, tm, func(ctx context.Context, _ sql.Executor) error {
//	    return accounts.Insert(ctx, Account{ID: "acc_1", Email: "ada@example.com"}) // joins this transaction
//	})
//
// A call runs on the transaction its context carries for that transactor, and
// on the pool otherwise. A write runs in a savepoint of the caller's
// transaction — or a transaction of its own — so it is atomic on its own, and
// a refused or failed one leaves the caller's transaction usable. Its hooks run
// once the transaction that holds it has committed, and never for one rolled
// back.
//
// The document is kept as the bytes the store encoded, never as the engine's
// JSON type, which reorders members and respells numbers, so it reads back
// byte for byte. Keys are compared as bytes — binary key columns, no
// collation — so "a", "A" and "a " are three keys, as they are in Go. A key
// longer than [MaxSQLKeyLen] bytes is refused with KeyTooLong. Index keys may
// reach the table hashed: [SQLConfig].IndexKey transforms every one at a write
// and at every Lookup and Find, and an index is an equality lookup, which a
// keyed hash keeps. Index rows are kept, not rebuilt at every open: after a
// declaration changes, [SQLStore].Reindex files the stored documents again.
//
// A failure of the database is StatementFailed, which a caller answers with a
// 503: the driver's error is joined beside it for errors.Is and errors.As,
// and withheld from its text, because a driver quotes the row a constraint
// refused and that row holds a key.
//
// # The ports, for a double
//
// [Store] and [SQLStore] are engines. A caller that must stand a double in for
// one depends instead on the port it implements: [Collection] and [Versioned]
// for a [Store], whose calls take no context, [CollectionContext] and
// [VersionedContext] for a [SQLStore], whose every call takes one, and
// [Announcer] — OnWrite and OnDelete — for either. Each pair carries the same
// methods, the same documents and the same refusals; there is no single port
// over both, because the file engine waits on nothing a context could abandon
// while the SQL engine waits on a database (ADR 0160).
//
// # Versions
//
// A store opened with [Config].Versions — or [SQLConfig].Versions — keeps the
// last versions of each document, as a content system keeps a page's
// revisions (ADR 0143):
//
//	pages, err := docstore.Open(docstore.Config[Page]{Key: Page.Key, FS: data, Path: "cms/pages.json", Versions: 20})
//	err = pages.PutStamped(page, docstore.Stamp{Meta: map[string]string{"by": userID, "command": "pages.Edit"}})
//	revisions, err := pages.Versions(page.ID) // newest first: the page as it is, then the 20 before it
//	third, err := pages.Version(page.ID, revisions[3].Number)
//
// A creation is version 1, and every write that changes the document makes
// the next one, stamped with the instant of [Config].Clock and the metadata
// the Stamped writes carry — who, which command; the plain writes carry none.
// A number is never given twice while the document exists. A write storing
// the JSON already stored makes no version, and neither does one stamped
// InPlace: the document changes and its current version keeps its number. The
// former versions beyond Versions are pruned by the write that makes a newer
// one, in the same durable write as the document — the same overlay entry, the
// same transaction — so no crash ever leaves the versions ahead of or behind
// their document. [Config].Held keeps a document's versions from pruning, for
// a legal hold, until a write finds it released. RewriteVersions rewrites a
// document's former versions under the writers' lock, for an erasure: it may
// clear or drop them, never renumber or add one. A deletion takes every
// version with it. Versions are never indexed: Lookup and Find read the
// current version.
//
// The file store keeps them in memory, in each write's overlay entry and, at
// rest, in the file Path + ".versions"; a store opened without versions over
// files that keep them is refused, rather than left to drop them. The SQL
// store keeps them in a third table, which [SQLVersionsMigration] creates.
// [Version].JSON is compact JSON; a version predating a change of the type
// may no longer decode into it, which is why it is JSON and not a T.
package docstore

import (
	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcdocstore "github.com/kitsunium/sdk/internal/service/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
)

// DefaultFoldAt is the least number of overlay entries that folds the overlay
// into the snapshot when [Config].FoldAt is zero.
const DefaultFoldAt int = svcdocstore.DefaultFoldAt

// MaxSQLTableLen is the longest table name [OpenSQL] and [SQLMigration]
// accept, in bytes: PostgreSQL's limit of 63, less what the store appends to
// derive its index table's name.
const MaxSQLTableLen int = svcdocstore.MaxSQLTableLen

// MaxSQLKeyLen is the longest store key, and the longest index key as it
// reaches the table, a SQL store accepts, in bytes.
const MaxSQLKeyLen int = svcdocstore.MaxSQLKeyLen

// MaxSQLIndexNameLen is the longest index name a SQL store accepts, in bytes.
const MaxSQLIndexNameLen int = svcdocstore.MaxSQLIndexNameLen

// The codes of the store's refusals (0.3.80.*), for errs.HasCode.
const (
	// CodeDocumentNotFound: no document under the key, or nobody holds the
	// key in a unique index.
	CodeDocumentNotFound errs.Code = coredocstore.CodeDocumentNotFound
	// CodeDocumentExists: Insert over a taken key.
	CodeDocumentExists errs.Code = coredocstore.CodeDocumentExists
	// CodeUniqueKeyTaken: a unique index refused the write.
	CodeUniqueKeyTaken errs.Code = coredocstore.CodeUniqueKeyTaken
	// CodeDocumentKeyEmpty: the key function returned the empty string.
	CodeDocumentKeyEmpty errs.Code = coredocstore.CodeDocumentKeyEmpty
	// CodeDocumentKeyChanged: an Update's function changed the key.
	CodeDocumentKeyChanged errs.Code = coredocstore.CodeDocumentKeyChanged
	// CodeIndexUnknown: a read named an index the store never declared.
	CodeIndexUnknown errs.Code = coredocstore.CodeIndexUnknown
	// CodeIndexNotUnique: Lookup on a multi-valued index.
	CodeIndexNotUnique errs.Code = coredocstore.CodeIndexNotUnique
	// CodeDocumentUndecodable: a stored document no longer fits the type.
	CodeDocumentUndecodable errs.Code = coredocstore.CodeDocumentUndecodable
	// CodeDocumentUnencodable: encoding/json refused the value.
	CodeDocumentUnencodable errs.Code = coredocstore.CodeDocumentUnencodable
	// CodePersistFailed: the filesystem refused the write; nothing changed.
	CodePersistFailed errs.Code = coredocstore.CodePersistFailed
	// CodeLoadFailed: the store's files cannot be read or are not a store's.
	CodeLoadFailed errs.Code = coredocstore.CodeLoadFailed
	// CodeIndexBroken: the stored documents break a unique index.
	CodeIndexBroken errs.Code = coredocstore.CodeIndexBroken
	// CodeStoreClosed: a call after Close.
	CodeStoreClosed errs.Code = coredocstore.CodeStoreClosed
	// CodeStoreMisconfigured: Open refused its configuration.
	CodeStoreMisconfigured errs.Code = coredocstore.CodeStoreMisconfigured
	// CodeWriteUnconfirmed: the write stands, its durability is in doubt.
	CodeWriteUnconfirmed errs.Code = coredocstore.CodeWriteUnconfirmed
	// CodeStatementFailed: the SQL store's database did not complete a call.
	CodeStatementFailed errs.Code = coredocstore.CodeStatementFailed
	// CodeKeyTooLong: a key is longer than the SQL store's key columns hold.
	CodeKeyTooLong errs.Code = coredocstore.CodeKeyTooLong
	// CodeVersionsNotKept: a call on versions to a store that keeps none.
	CodeVersionsNotKept errs.Code = coredocstore.CodeVersionsNotKept
	// CodeVersionNotFound: the document keeps no version of that number.
	CodeVersionNotFound errs.Code = coredocstore.CodeVersionNotFound
	// CodeVersionsRewriteRefused: a rewrite returned what a rewrite may not.
	CodeVersionsRewriteRefused errs.Code = coredocstore.CodeVersionsRewriteRefused
)

// The store's sentinels, for errors.Is.
var (
	// DocumentNotFound is a miss.
	DocumentNotFound = coredocstore.DocumentNotFound
	// DocumentExists refuses an insertion over a taken key.
	DocumentExists = coredocstore.DocumentExists
	// UniqueKeyTaken refuses a write a unique index cannot file.
	UniqueKeyTaken = coredocstore.UniqueKeyTaken
	// DocumentKeyEmpty refuses a document whose key is empty.
	DocumentKeyEmpty = coredocstore.DocumentKeyEmpty
	// DocumentKeyChanged refuses an update that renames.
	DocumentKeyChanged = coredocstore.DocumentKeyChanged
	// IndexUnknown refuses a read of an undeclared index.
	IndexUnknown = coredocstore.IndexUnknown
	// IndexNotUnique refuses a Lookup on a multi-valued index.
	IndexNotUnique = coredocstore.IndexNotUnique
	// DocumentUndecodable reports a stored document the type no longer fits.
	DocumentUndecodable = coredocstore.DocumentUndecodable
	// DocumentUnencodable refuses a value encoding/json cannot encode.
	DocumentUnencodable = coredocstore.DocumentUnencodable
	// PersistFailed reports a write the filesystem refused.
	PersistFailed = coredocstore.PersistFailed
	// LoadFailed refuses files that are not a store's.
	LoadFailed = coredocstore.LoadFailed
	// IndexBroken refuses documents that break a unique index.
	IndexBroken = coredocstore.IndexBroken
	// StoreClosed refuses a call after Close.
	StoreClosed = coredocstore.StoreClosed
	// StoreMisconfigured refuses a configuration no store could honour.
	StoreMisconfigured = coredocstore.StoreMisconfigured
	// WriteUnconfirmed reports a write that stands but may not survive a
	// power loss.
	WriteUnconfirmed = coredocstore.WriteUnconfirmed
	// StatementFailed reports a call the SQL store's database did not
	// complete; the driver's error is joined beside it, its text withheld.
	StatementFailed = coredocstore.StatementFailed
	// KeyTooLong refuses a key longer than the SQL store's key columns hold.
	KeyTooLong = coredocstore.KeyTooLong
	// VersionsNotKept refuses a call on versions to a store that keeps none.
	VersionsNotKept = coredocstore.VersionsNotKept
	// VersionNotFound is a miss on a version: never made, or pruned.
	VersionNotFound = coredocstore.VersionNotFound
	// VersionsRewriteRefused refuses what a rewrite's function returned.
	VersionsRewriteRefused = coredocstore.VersionsRewriteRefused
)

// Store is the public alias for the document store: Get, List, Filter,
// Entries, Lookup, Find and Stats read; Put, Insert, Replace, Update and
// Delete write, and their Stamped forms say what the version they make
// records; Versions and Version read a document's versions, RewriteVersions
// rewrites its former ones; OnWrite and OnDelete announce; Fold and Close
// bring it to rest.
type Store[T any] = svcdocstore.Store[T]

// Config is the public alias for a store's configuration: Key (required), FS
// and Path (both, or neither for a memory store), FoldAt, and Versions with
// its Clock and Held.
type Config[T any] = svcdocstore.Config[T]

// Version is the public alias for one version of a document: its Number from
// 1, the instant At the write that made it ran, the Meta that write carried,
// and the document as JSON.
type Version = coredocstore.VersionValue

// Stamp is the public alias for what a Stamped write says about the version
// it makes: the Meta the version records, or InPlace — no version.
type Stamp = coredocstore.StampValue

// IndexSpec is the public alias for one secondary index's declaration, built
// by [Unique] or [Index] and given to [Open].
type IndexSpec[T any] = coredocstore.IndexSpec[T]

// Entry is the public alias for one stored document as JSON, with its key.
type Entry = coredocstore.EntryValue

// Stats is the public alias for what a store says about itself: documents,
// pending overlay entries, folds, and the last automatic fold's failure.
type Stats = svcdocstore.StatsValue

// SQLStore is the public alias for the document store over SQL: Get, List,
// Filter, Entries, Count, Lookup and Find read; Put, Insert, Replace, Update
// and Delete write, and their Stamped forms say what the version they make
// records; Versions, Version and RewriteVersions as for [Store]; OnWrite and
// OnDelete announce once the write's transaction commits; Reindex files the
// stored documents again. Every call takes a context.
type SQLStore[T any] = svcdocstore.SQLStore[T]

// SQLConfig is the public alias for a SQL store's configuration: Key,
// Transactor, Dialect and Table (required), IndexKey, and Versions with its
// Clock and Held.
type SQLConfig[T any] = svcdocstore.SQLConfig[T]

// Collection is the public alias for the port of a store whose calls take no
// context, which [Store] implements: Get, List, Filter, Entries, Lookup and
// Find; Put, Insert, Replace, Update and Delete; OnWrite and OnDelete. It is
// what a caller depends on to stand a double in for a [Store]; Stats, Fold
// and Close are the engine's own.
type Collection[T any] = coredocstore.Collection[T]

// Versioned is the public alias for the sibling of [Collection] that reads and
// writes a document's versions without a context, which [Store] implements:
// the four Stamped writes, Versions, Version and RewriteVersions.
type Versioned[T any] = coredocstore.Versioned[T]

// CollectionContext is the public alias for [Collection] with a context on
// every call, which [SQLStore] implements; Count and Reindex are the engine's
// own.
type CollectionContext[T any] = coredocstore.CollectionContext[T]

// VersionedContext is the public alias for [Versioned] with a context on every
// call, which [SQLStore] implements beside [CollectionContext].
type VersionedContext[T any] = coredocstore.VersionedContext[T]

// Announcer is the public alias for the pair of hooks both engines answer
// exactly as they are: OnWrite and OnDelete.
type Announcer = coredocstore.Announcer

// Open builds a store from cfg with the secondary indexes given: in memory
// without a filesystem, otherwise loaded from it — the snapshot, the overlay
// replayed on top, the indexes rebuilt. It creates the directories the store
// lives in, 0700, and writes its files 0600.
func Open[T any](cfg Config[T], indexes ...IndexSpec[T]) (*Store[T], error) {
	//: delegate verbatim to the service constructor.
	return svcdocstore.Open(cfg, indexes...)
}

// OpenSQL builds a store over SQL from cfg with the secondary indexes given,
// declared as for [Open]. It sends no statement: its tables are
// [SQLMigration]'s, run by the caller's Migrator before the store is used.
func OpenSQL[T any](cfg SQLConfig[T], indexes ...IndexSpec[T]) (*SQLStore[T], error) {
	//: delegate verbatim to the service constructor.
	return svcdocstore.OpenSQL(cfg, indexes...)
}

// SQLMigration returns the migration that creates the two tables a SQL store
// named table keeps on dialect, numbered version for the caller's own version
// table. Its Down drops both, and every document in them. Every statement does
// nothing when its table exists, so a run MySQL's implicit commit stopped
// halfway completes when it runs again.
func SQLMigration(dialect sql.Dialect, table string, version uint64) (sql.Migration, error) {
	//: delegate verbatim to the service constructor.
	return svcdocstore.SQLMigration(dialect, table, version)
}

// SQLVersionsMigration returns the migration that creates the table a SQL
// store named table keeps its versions in on dialect, numbered version for the
// caller's own version table, beside [SQLMigration]'s. A store opened with
// [SQLConfig].Versions needs it. Its Down drops the table, and every version
// in it: run it to turn versions off for good.
func SQLVersionsMigration(dialect sql.Dialect, table string, version uint64) (sql.Migration, error) {
	//: delegate verbatim to the service constructor.
	return svcdocstore.SQLVersionsMigration(dialect, table, version)
}

// Unique declares a unique index over the one key key returns. An empty key is
// not indexed, so any number of documents may have none.
func Unique[T any](name string, key func(T) string) IndexSpec[T] {
	//: delegate verbatim to the core declaration.
	return coredocstore.Unique(name, key)
}

// Index declares an index where a document may have several keys and a key
// several documents. Empty keys are not indexed.
func Index[T any](name string, keys func(T) []string) IndexSpec[T] {
	//: delegate verbatim to the core declaration.
	return coredocstore.Index(name, keys)
}
