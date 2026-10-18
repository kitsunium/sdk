package app

import (
	"context"
	"encoding/json"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/data/vfs"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// A store is a port: Service.Store[T] is what a service declares and calls,
// its API and its refusals are kit's, and the backend it runs on is the
// app's choice (ADR 0004). plug.StoreEngine is that backend, as kit sees it:
// one engine over documents (ADR 0179 §2), compiled once whatever the
// entity type.
//
// A store holds one while its app runs, opened by the app's placement of
// the store through the store subsystem, which framework/app/store plugs in
// (plug.Stores, ADR 0179 §1): in memory or in the data directory — the
// SDK's document store —, or on a database — the SDK's document store over
// SQL (store_sql.go) —, behind kit's sealing (seal_store.go) and its history
// (history.go) when the store needs them. kit's core imports neither the
// SDK's docstore nor its sql: a program that does not import the subsystem
// links none of them, and its start refuses a store (subsystem.go).
//
// An engine's refusals are kit's kinds (plug.Refusal) — not found, exists,
// a unique key taken, an empty or changed key, an unknown or non-unique
// index, undecodable, unencodable, not persisted, closed, unconfirmed, and
// on a database a statement not completed or a key too long —, which
// Store.said words for the caller: every engine speaks them, so said speaks
// for every engine unchanged, and no engine quotes a key in an error.
//
// Every call takes the caller's context: an engine on a database runs the
// call on the transaction the context carries for it and within its
// timeout (sqlCall); the document store records, in a transaction of the
// data's, what a write replaced, and holds the write's hooks until the
// commit (kitTransaction). The port grows by sibling interfaces an engine
// may implement, never by a method (the SDK's ADR 0039).
//
// A document an engine returns holds the record as the store's caller reads
// it — opened, for a store that seals —, and its entity when the engine
// decoded it; Entries are the records as they rest.

// storeFS is where a document store keeps its files: the data directory and
// the store's snapshot in it. A nil FS keeps the store in memory.
type storeFS struct {
	fs   vfs.FullFS
	path string
}

// engineConfig is what an engine knows of its documents: their key, the
// entity their JSON holds, and the indexes over them — the product's
// functions on its type, wrapped once at the declaration.
type engineConfig struct {
	// key is a document's key: it reads the document's Value, which the
	// engine decodes first unless keyAtRest gives the key.
	key func(d plug.DocumentValue) string
	// keyAtRest, when set, gives the key of a record as it rests, without
	// decoding it; false when it cannot.
	keyAtRest func(raw json.RawMessage) (string, bool)
	// decode is the entity a document's JSON holds.
	decode func(raw json.RawMessage) (any, error)
	// indexes are the secondary indexes, over documents.
	indexes []plug.IndexSpec
}

// storeConfig is the configuration of an engine named name over c's
// documents, keeping each record's versions as vs says.
func (c *engineConfig) storeConfig(name string, vs versionsOn) plug.StoreConfig {
	cfg := plug.StoreConfig{
		Name: name, Key: c.key, KeyAtRest: c.keyAtRest, Decode: c.decode, Indexes: c.indexes,
		Transaction: kitTransaction{}, Stamp: stampOf,
	}
	if vs.keep > 0 {
		cfg.Versions, cfg.Held = vs.keep, vs.held
		if vs.clock != nil {
			cfg.Clock = vs.clock.Now
		}
	}
	return cfg
}

// openLocalEngine opens the document store of an engine named name over
// cfg's documents: loaded from where says — the snapshot, what was written
// since replayed on it, the indexes rebuilt — or empty in memory; keeping
// each record's versions as vs says.
func openLocalEngine(name string, cfg *engineConfig, where storeFS, vs versionsOn) (plug.StoreEngine, error) {
	sc := cfg.storeConfig(name, vs)
	if where.fs != nil {
		sc.Data, sc.Path = where.fs, where.path
	}
	return openEngine(sc)
}

// openEngine opens the engine sc places, through the store subsystem:
// refused when the program does not import it — which the start says first,
// for every declaration at once (subsystem.go).
func openEngine(sc plug.StoreConfig) (plug.StoreEngine, error) {
	d := plug.Stores.Load()
	if d == nil || d.Open == nil {
		return nil, storesMissing()
	}
	return d.Open(sc)
}

// storesMissing is the refusal of what needs the store subsystem in a
// program that does not import it.
func storesMissing() error {
	return errs.New(CodeSubsystemMissing, "SUBSYSTEM_NOT_IMPORTED", subsystemPublic,
		"app: a store or a database needs the store subsystem, which the program does not import: "+storePackage,
		errs.String("packages", storePackage))
}

// kitTransaction is kit's transaction as a store's engine takes part in it
// (plug.Transaction): the undo log of the data's local transactions, the
// write hooks held until the commit, the announcements a rollback on a
// database makes again.
type kitTransaction struct{}

// Local reports whether ctx runs in a transaction that holds the data's
// writer turn.
func (kitTransaction) Local(ctx context.Context) bool { return inLocal(ctx) }

// Undo keeps restore, which a rollback of the transaction ctx carries runs,
// with the entity the funnel named (undoKeyed).
func (kitTransaction) Undo(ctx context.Context, store string, restore func(ctx context.Context) error) {
	key, _ := ctx.Value(undoKeyOf{}).(string)
	recordUndo(ctx, undoStep{store: store, key: key, restore: restore})
}

// AtCommit holds fn until the transaction ctx carries commits: an effect
// that stays in the process, a store's write hook.
func (kitTransaction) AtCommit(ctx context.Context, fn func()) bool {
	return hold(ctx, heldEffect{release: func(context.Context) error { fn(); return nil }, inside: true})
}

// Retell keeps fn, which announces a write on a database again when the
// transaction ctx carries rolls it back.
func (kitTransaction) Retell(ctx context.Context, fn func()) { retell(ctx, fn) }
