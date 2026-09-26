//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/docstore .

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
// fold loads the same documents. BENCH.md in internal/service/docstore has the
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
package docstore

import (
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcdocstore "github.com/kitsunium/sdk/internal/service/docstore"
)

// DefaultFoldAt is the least number of overlay entries that folds the overlay
// into the snapshot when [Config].FoldAt is zero.
const DefaultFoldAt int = svcdocstore.DefaultFoldAt

// The codes of the store's refusals (0.3.80.*), for errs.HasCode.
const (
	// CodeDocumentNotFound: no document under the key, or nobody holds the
	// key in a unique index.
	CodeDocumentNotFound errs.Code = svcdocstore.CodeDocumentNotFound
	// CodeDocumentExists: Insert over a taken key.
	CodeDocumentExists errs.Code = svcdocstore.CodeDocumentExists
	// CodeUniqueKeyTaken: a unique index refused the write.
	CodeUniqueKeyTaken errs.Code = svcdocstore.CodeUniqueKeyTaken
	// CodeDocumentKeyEmpty: the key function returned the empty string.
	CodeDocumentKeyEmpty errs.Code = svcdocstore.CodeDocumentKeyEmpty
	// CodeDocumentKeyChanged: an Update's function changed the key.
	CodeDocumentKeyChanged errs.Code = svcdocstore.CodeDocumentKeyChanged
	// CodeIndexUnknown: a read named an index the store never declared.
	CodeIndexUnknown errs.Code = svcdocstore.CodeIndexUnknown
	// CodeIndexNotUnique: Lookup on a multi-valued index.
	CodeIndexNotUnique errs.Code = svcdocstore.CodeIndexNotUnique
	// CodeDocumentUndecodable: a stored document no longer fits the type.
	CodeDocumentUndecodable errs.Code = svcdocstore.CodeDocumentUndecodable
	// CodeDocumentUnencodable: encoding/json refused the value.
	CodeDocumentUnencodable errs.Code = svcdocstore.CodeDocumentUnencodable
	// CodePersistFailed: the filesystem refused the write; nothing changed.
	CodePersistFailed errs.Code = svcdocstore.CodePersistFailed
	// CodeLoadFailed: the store's files cannot be read or are not a store's.
	CodeLoadFailed errs.Code = svcdocstore.CodeLoadFailed
	// CodeIndexBroken: the stored documents break a unique index.
	CodeIndexBroken errs.Code = svcdocstore.CodeIndexBroken
	// CodeStoreClosed: a call after Close.
	CodeStoreClosed errs.Code = svcdocstore.CodeStoreClosed
	// CodeStoreMisconfigured: Open refused its configuration.
	CodeStoreMisconfigured errs.Code = svcdocstore.CodeStoreMisconfigured
	// CodeWriteUnconfirmed: the write stands, its durability is in doubt.
	CodeWriteUnconfirmed errs.Code = svcdocstore.CodeWriteUnconfirmed
)

// The store's sentinels, for errors.Is.
var (
	// DocumentNotFound is a miss.
	DocumentNotFound = svcdocstore.DocumentNotFound
	// DocumentExists refuses an insertion over a taken key.
	DocumentExists = svcdocstore.DocumentExists
	// UniqueKeyTaken refuses a write a unique index cannot file.
	UniqueKeyTaken = svcdocstore.UniqueKeyTaken
	// DocumentKeyEmpty refuses a document whose key is empty.
	DocumentKeyEmpty = svcdocstore.DocumentKeyEmpty
	// DocumentKeyChanged refuses an update that renames.
	DocumentKeyChanged = svcdocstore.DocumentKeyChanged
	// IndexUnknown refuses a read of an undeclared index.
	IndexUnknown = svcdocstore.IndexUnknown
	// IndexNotUnique refuses a Lookup on a multi-valued index.
	IndexNotUnique = svcdocstore.IndexNotUnique
	// DocumentUndecodable reports a stored document the type no longer fits.
	DocumentUndecodable = svcdocstore.DocumentUndecodable
	// DocumentUnencodable refuses a value encoding/json cannot encode.
	DocumentUnencodable = svcdocstore.DocumentUnencodable
	// PersistFailed reports a write the filesystem refused.
	PersistFailed = svcdocstore.PersistFailed
	// LoadFailed refuses files that are not a store's.
	LoadFailed = svcdocstore.LoadFailed
	// IndexBroken refuses documents that break a unique index.
	IndexBroken = svcdocstore.IndexBroken
	// StoreClosed refuses a call after Close.
	StoreClosed = svcdocstore.StoreClosed
	// StoreMisconfigured refuses a configuration no store could honour.
	StoreMisconfigured = svcdocstore.StoreMisconfigured
	// WriteUnconfirmed reports a write that stands but may not survive a
	// power loss.
	WriteUnconfirmed = svcdocstore.WriteUnconfirmed
)

// Store is the public alias for the document store: Get, List, Filter,
// Entries, Lookup, Find and Stats read; Put, Insert, Replace, Update and
// Delete write; OnWrite and OnDelete announce; Fold and Close bring it to rest.
type Store[T any] = svcdocstore.Store[T]

// Config is the public alias for a store's configuration: Key (required), FS
// and Path (both, or neither for a memory store), and FoldAt.
type Config[T any] = svcdocstore.Config[T]

// IndexSpec is the public alias for one secondary index's declaration, built
// by [Unique] or [Index] and given to [Open].
type IndexSpec[T any] = svcdocstore.IndexSpec[T]

// Entry is the public alias for one stored document as JSON, with its key.
type Entry = svcdocstore.EntryValue

// Stats is the public alias for what a store says about itself: documents,
// pending overlay entries, folds, and the last automatic fold's failure.
type Stats = svcdocstore.StatsValue

// Open builds a store from cfg with the secondary indexes given: in memory
// without a filesystem, otherwise loaded from it — the snapshot, the overlay
// replayed on top, the indexes rebuilt. It creates the directories the store
// lives in, 0700, and writes its files 0600.
func Open[T any](cfg Config[T], indexes ...IndexSpec[T]) (*Store[T], error) {
	//: delegate verbatim to the service constructor.
	return svcdocstore.Open(cfg, indexes...)
}

// Unique declares a unique index over the one key key returns. An empty key is
// not indexed, so any number of documents may have none.
func Unique[T any](name string, key func(T) string) IndexSpec[T] {
	//: delegate verbatim to the service declaration.
	return svcdocstore.Unique(name, key)
}

// Index declares an index where a document may have several keys and a key
// several documents. Empty keys are not indexed.
func Index[T any](name string, keys func(T) []string) IndexSpec[T] {
	//: delegate verbatim to the service declaration.
	return svcdocstore.Index(name, keys)
}
