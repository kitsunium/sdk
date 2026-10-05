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
