package plug

import (
	"context"
	"database/sql"
	"encoding/json"
	"io/fs"
	"sync/atomic"
	"time"
)

// The store subsystem's contract (ADR 0179 §3): what kit's core asks of the
// engine its stores run on and of the databases that keep them, as
// database/sql/driver is to database/sql. framework/app/store sets the hook
// and framework/internal/app/storeengine implements it over the SDK's docstore
// and sql; the core reads it and imports neither. Nothing here names an
// entity's type: the engine works on documents, and Store[T] stays the typed
// facade above it, in the core. The values are here, the engine and its
// siblings in store_interface.go, the refusals in store_refusal.go.

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

// Stores is the store subsystem: the engines the framework's stores run on, the framework's
// scope on a database and the migrations of the framework's tables; nil until
// framework/app/store is imported. A product that declares a store or a
// database without it is refused at the start (ADR 0179 §6).
var Stores atomic.Pointer[StoreDriverConfig]

// StoreDriverConfig is what framework/app/store plugs into kit: how a
// store's engine opens, how the framework opens its scope on a database, and the
// migration that makes one of the framework's tables.
type StoreDriverConfig struct {
	// Open opens the engine of one store, as cfg places it: loaded from the
	// data directory, empty in memory, or on a database.
	Open func(cfg StoreConfig) (StoreEngine, error)
	// Database opens the framework's scope on one database's pool.
	Database func(cfg DatabaseConfig) (Database, error)
	// TableMigration is the migration that makes the table t on a database
	// of the dialect named, at version.
	TableMigration func(dialect string, t TableSpec, version uint64) (MigrationValue, error)
}

// DocumentValue is a record as a store's engine holds it (ADR 0179 §2):
// its JSON as it rests, byte for byte what the framework encodes, and the entity
// once decoded, kept beside it so that a read, an index or a privacy
// walk does not decode it twice.
type DocumentValue struct {
	// JSON is the record as it rests.
	JSON json.RawMessage
	// Value is the entity decoded from JSON, nil until something decoded it;
	// the store's typed facade asserts it to its type.
	Value any
}

// EntryValue is a record as it rests, with its key: what an engine lists
// for a reader that cannot key a record itself.
type EntryValue struct {
	// Key is the record's key.
	Key string
	// JSON is the record as it rests.
	JSON json.RawMessage
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

// StoreConfig is what the framework asks of one store's engine: the store, its key and
// its indexes over documents, where it rests and what it keeps, and how its
// writes take part in the framework's transactions.
type StoreConfig struct {
	// Name is the store's, as a log line and a rollback name it.
	Name string
	// Key is a document's key: the product's key function on its type,
	// wrapped once at the declaration. It reads the document's Value, which
	// the engine decodes first unless KeyAtRest gives the key.
	Key func(d DocumentValue) string
	// KeyAtRest, when set, gives the key of a record as it rests, without
	// decoding it; false when it cannot.
	KeyAtRest func(raw json.RawMessage) (string, bool)
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
	// Database keeps the store, nil when the data directory or memory does:
	// a scope the store subsystem's Database opened.
	Database Database
	// Table is the store's table on Database.
	Table string
	// IndexKey is the key a table's index keys are hashed under, nil for
	// none: an index key reaches the table as its keyed hash.
	IndexKey func(index, key string) []byte
	// Call is the context one call of the engine on Database runs with: on
	// the transaction ctx carries for it, else on the pool, within the
	// database's timeout; done ends it.
	Call func(ctx context.Context, write bool) (call context.Context, done func(), err error)
	// Versions is how many former versions of each record the engine keeps,
	// none when zero; Clock stamps them.
	Versions int
	// Clock is the app's clock, which stamps a version.
	Clock func() time.Time
	// Held reports whether a record's versions are held, and no write may
	// prune them: asked with the write's context, on a database inside its
	// transaction.
	Held func(ctx context.Context, key string) bool
	// Stamp is what a write under ctx says about the version it makes.
	Stamp func(ctx context.Context) StampValue
	// Transaction is the framework's transaction, which the engine's writes take part
	// in.
	Transaction Transaction
}

// StampValue is what a write says about the version it makes: who made it
// and which command, kept with the version; and whether it changes the
// document in place, without a version of its own.
type StampValue struct {
	// Meta is kept with the version the write makes.
	Meta map[string]string
	// InPlace makes the write change the document without making a version.
	InPlace bool
}

// DatabaseConfig is one database a product declares, as the framework opened it: its
// pool, its dialect, its tuning and the timeout of each call.
type DatabaseConfig struct {
	// Name is the database's, as the product declared it.
	Name string
	// DB is the pool app.Engine opened.
	DB *sql.DB
	// Dialect is the engine's SQL dialect: "postgres", "mysql" or
	// "sqlite".
	Dialect string
	// Timeout bounds each call the framework makes on the database.
	Timeout time.Duration
	// MaxOpen and MaxIdle bound the pool's connections.
	MaxOpen, MaxIdle int
	// MaxLifetime and MaxIdleTime bound a connection's life.
	MaxLifetime, MaxIdleTime time.Duration
	// CheckTimeout bounds a check of the database.
	CheckTimeout time.Duration
}

// TableSpec is one of the framework's tables on a database, as the store subsystem's
// migration makes it: a store's, or a store's versions beside the store's
// table VersionsOf. A consumer's queue's table is the queue subsystem's
// (QueueDriverConfig's TableMigration).
type TableSpec struct {
	// Name is the table's.
	Name string
	// VersionsOf is, for a store's versions, the store's table, from which
	// the versions' table is named.
	VersionsOf string
}

// MigrationValue is one versioned change of a database's schema and its
// reversal, as the core hands it to the store subsystem: a product's
// app.Migration, or one of the framework's own, its steps on the standard library's
// types.
type MigrationValue struct {
	// Version orders the migration in its set: non-zero and unique.
	Version uint64
	// Name labels it in the version table.
	Name string
	// Up applies the change.
	Up Step
	// Down reverses it.
	Down Step
}

// Step is one direction of a migration, run on ex.
type Step func(ctx context.Context, ex Executor) error

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
