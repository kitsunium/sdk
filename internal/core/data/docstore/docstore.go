// Package docstore declares the document-store domain: typed, keyed JSON
// documents with unique and multi-valued secondary indexes, kept by two
// engines under one contract — the file engine in memory or through
// core/data/vfs (ADR 0110), the SQL engine in a database the caller owns
// (ADR 0139), both keeping a document's versions in its own write when asked
// (ADR 0143).
//
// It holds what the two engines share and nothing only one of them has:
//
//   - the ports. [Collection] is a store whose calls take no context, which
//     the file engine is; [CollectionContext] is the same calls, each taking
//     a context first, which the SQL engine is. [Versioned] and
//     [VersionedContext] are their siblings for a document's versions, and
//     [Announcer] is the pair of hooks both engines answer exactly as they
//     are;
//   - the values both engines read and return — [EntryValue], [VersionValue],
//     [StampValue] — and [IndexSpec], the declaration both take, with its two
//     constructors [Unique] and [Index];
//   - the codes of the 0.3.80.* range and their sentinels: both engines answer
//     the same refusals under the same codes (ADR 0160).
//
// There are two ports and not one because the engines differ on the one
// thing a single interface would have to fix: the file engine waits on
// nothing a caller could abandon and takes no context (ADR 0110 §D6), while
// every call of the SQL engine waits on a database and takes one. A single
// port would give the file engine a context it cannot honour, or take the SQL
// engine's away (ADR 0139 §D2). So each port is the contract of one shape of
// call, with the same methods, the same documents and the same refusals.
//
// The engines, their configurations, the file engine's statistics and every
// statement live in internal/service/data/docstore.
package docstore

// Announcer is the pair of hooks a document store answers: both engines
// implement it exactly as it is. A registered function is told the key of
// every document a write stored or a deletion removed once that write stands —
// durable for the file engine, committed for the SQL engine, and never for a
// write rolled back — on the writer's goroutine and outside every lock of the
// store.
type Announcer interface {
	// OnWrite registers fn to be told the key of every document Put, Insert,
	// Replace or Update stored, and returns the function that removes the
	// registration. A nil fn registers nothing. A panic in fn reaches the
	// writer after its write stood.
	OnWrite(fn func(key string)) (remove func())
	// OnDelete registers fn to be told the key of every document Delete
	// removed, on the same terms as OnWrite.
	OnDelete(fn func(key string)) (remove func())
}

// Collection is a typed, keyed collection of JSON documents with secondary
// indexes whose calls take no context — the port the file engine,
// internal/service/data/docstore.Store, implements. Every read returns a
// copy and every write stores one: a document is kept as its JSON encoding,
// so what a caller holds never aliases what the store holds. No refusal
// quotes a key.
//
// The versions of a document are [Versioned], its sibling; the file engine's
// Stats, Fold and Close are the engine's own, because a double of the port has
// no files to fold.
type Collection[T any] interface {
	Announcer
	// Get returns the document stored under key, or DOCUMENT_NOT_FOUND.
	Get(key string) (T, error)
	// List returns every document, in key order.
	List() ([]T, error)
	// Filter returns the documents keep accepts, in key order. It decodes
	// every document; an index is the way not to.
	Filter(keep func(T) bool) ([]T, error)
	// Entries returns up to limit stored documents as JSON, in key order —
	// every one when limit is not positive — each a copy the caller owns.
	Entries(limit int) ([]EntryValue, error)
	// Lookup returns the document holding key in the unique index named
	// index: DOCUMENT_NOT_FOUND when none does, INDEX_UNKNOWN for an index the
	// store never declared, INDEX_NOT_UNIQUE for one that may file several.
	Lookup(index, key string) (T, error)
	// Find returns the documents filed under key in the index named index, in
	// store-key order. It reads any index, unique or not, and no document is
	// an empty slice, not an error.
	Find(index, key string) ([]T, error)
	// Put stores v under its key, creating it or replacing whatever was there.
	Put(v T) error
	// Insert stores v, which must be new: DOCUMENT_EXISTS when its key is
	// taken.
	Insert(v T) error
	// Replace stores v over the document already under its key:
	// DOCUMENT_NOT_FOUND when there is none, so a document deleted meanwhile
	// is never brought back.
	Replace(v T) error
	// Update applies fn to a copy of the document stored under key and stores
	// the result, atomically with respect to every other write, and returns
	// it. An error from fn is returned as it is and changes nothing, and so
	// does a result whose key is not key (DOCUMENT_KEY_CHANGED). fn may read
	// the store and must not write to it.
	Update(key string, fn func(*T) error) (T, error)
	// Delete removes the document stored under key, or returns
	// DOCUMENT_NOT_FOUND. Its versions go with it.
	Delete(key string) error
}
