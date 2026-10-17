package plug

import (
	"database/sql"
	"encoding/json"
	"io/fs"
	"sync/atomic"
	"time"
)

// The store subsystem's contract (ADR 0179 §3): what kit's core asks of the
// engine its stores run on and of the databases that keep them, as
// database/sql/driver is to database/sql. framework/kit/store sets the hook
// and framework/internal/kit/storekit implements it over the SDK's docstore
// and sql; the core reads it and imports neither. Nothing here names an
// entity's type: the engine works on documents, and Store[T] stays the typed
// facade above it, in the core. The values are here, the engine and its
// siblings in store_interface.go.

// The write modes.
const (
	// WriteUpsert writes whatever the key holds: a creation or a
	// replacement.
	WriteUpsert WriteMode = iota
	// WriteInsert expects nothing under the key, and is refused otherwise.
	WriteInsert
	// WriteReplace expects a document under the key, and is refused
	// otherwise: never a resurrection.
	WriteReplace
)

// Stores is the store subsystem: the engines kit's stores run on and kit's
// scope on a database; nil until framework/kit/store is imported. Until
// that package exists (ADR 0179, step 3) the core runs the stores itself
// and nothing sets it; once it does, a product that declares a store or a
// database without it is refused at the start (ADR 0179 §6).
var Stores atomic.Pointer[StoreDriverConfig]

// StoreDriverConfig is what framework/kit/store plugs into kit: how a
// store's engine opens, and how kit opens its scope on a database.
type StoreDriverConfig struct {
	// Open opens the engine of one store, as cfg places it: loaded from the
	// data directory, empty in memory, or on a database.
	Open func(cfg StoreConfig) (StoreEngine, error)
	// Database opens kit's scope on one database's pool.
	Database func(cfg DatabaseConfig) (Database, error)
}

// DocumentValue is a record as a store's engine holds it (ADR 0179 §2):
// its JSON as it rests, byte for byte what kit encodes, and the entity
// once decoded, kept beside it so that a read, an index or a privacy
// walk does not decode it twice.
type DocumentValue struct {
	// JSON is the record as it rests.
	JSON json.RawMessage
	// Value is the entity decoded from JSON, nil until something decoded it;
	// the store's typed facade asserts it to its type.
	Value any
}

// WriteMode says what a write expects to find under the document's key.
type WriteMode int

// IndexSpec declares one secondary index of a store, over documents: the
// product's index function on its type, wrapped once at the declaration.
type IndexSpec struct {
	// Name reads the index, and names it in a refusal.
	Name string
	// Unique refuses a write that would file two documents under one key.
	Unique bool
	// Keys are a document's keys in the index; an empty key is not indexed.
	Keys func(d DocumentValue) []string
}

// StoreConfig is what kit asks of one store's engine: the store, its key and
// its indexes over documents, where it rests and what it keeps.
type StoreConfig struct {
	// Name is the store's, as its files and its table name it.
	Name string
	// Key is a document's key: the product's key function on its type,
	// wrapped once at the declaration.
	Key func(d DocumentValue) string
	// Decode is the entity a document's JSON holds, which the engine keeps
	// as the document's Value.
	Decode func(raw json.RawMessage) (any, error)
	// Indexes are the store's secondary indexes.
	Indexes []IndexSpec
	// Data is the data directory the store rests in — the SDK's writable
	// file system, which the engine asserts — and Path its snapshot there;
	// a nil Data and a nil Database keep the store in memory.
	Data fs.FS
	// Path is the store's snapshot in Data.
	Path string
	// Database keeps the store, nil when the data directory or memory does.
	Database Database
	// Versions is how many former versions of each record the engine keeps,
	// none when zero; Clock stamps them.
	Versions int
	// Clock is the app's clock, which stamps a version.
	Clock func() time.Time
	// Held reports whether a record's versions are held, and no write may
	// prune them.
	Held func(key string) bool
}

// DatabaseConfig is one database a product declares, as kit opened it: its
// pool, its dialect and the timeout of each call.
type DatabaseConfig struct {
	// Name is the database's, as the product declared it.
	Name string
	// DB is the pool kit.Engine opened.
	DB *sql.DB
	// Dialect is the engine's SQL dialect: "postgres", "mysql" or
	// "sqlite".
	Dialect string
	// Timeout bounds each call kit makes on the database.
	Timeout time.Duration
}

// VersionValue is one version of a record: its number, when the write
// that made it ran, what that write said about it, and the record as it
// held it.
type VersionValue struct {
	// Number counts a record's versions from 1.
	Number uint64
	// At is when the write that made the version ran, in UTC.
	At time.Time
	// Meta is what the write said about it: who made it, which command.
	Meta map[string]string
	// JSON is the record as the version holds it.
	JSON json.RawMessage
}

// VersionWeightValue is what a store keeps beyond the records it holds:
// its former versions and its trash, counted and weighed.
type VersionWeightValue struct {
	// Former is how many former versions the store keeps.
	Former int
	// Trashed is how many records are in the trash.
	Trashed int
	// Bytes is what the former versions and the trashed records weigh.
	Bytes int64
}

// TombstoneValue is one record in a store's trash: its key, and when it was
// trashed.
type TombstoneValue struct {
	// Key is the trashed record's key.
	Key string
	// At is when the record was trashed, in UTC.
	At time.Time
}
