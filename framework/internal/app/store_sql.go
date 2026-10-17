package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// Table name suffixes of what kit keeps beside a store: its records'
// former values (ADR 0007), a workflow's journal; and the SDK's own for a
// store's versions, <table>___vs (kitsunium/sdk ADR 0143), which three
// underscores keep apart from every table kit names.
const (
	historySuffix  = "__history"
	workflowSuffix = "__workflow"
	versionsSuffix = "___vs"
)

// Stores on SQL (ADR 0004, step 2). A store a database keeps is still a
// document store: the SDK's over SQL, a table per store — a row per entity,
// the entity as the JSON kit encoded, its index rows beside it in
// <table>___ix, kept in the same write —, on the database's pool, each call
// on the transaction its context carries for that database and within
// <name>-timeout. Nothing is held in memory: the database is the source of
// truth. Index keys reach the table as keyed hashes, HMAC-SHA256 under a
// subkey of kit's index key: an e-mail, a token's hash, a subject never does.

// databaseOf is the open database that keeps s, nil when s keeps its data
// in the data directory or in memory: placed nowhere else, kept in memory,
// or its database without a URL in dev.
func (a *App) databaseOf(s placementSource) *databaseRun {
	p := a.placementOf(s)
	if p.db == nil || p.memory {
		return nil
	}
	if r := a.runOf(p.db); r != nil && r.open() {
		return r
	}
	return nil
}

// storesOnDatabase reports whether the stores d keeps live on it: while the
// app runs, d is open; otherwise, it has a URL — which outside dev it must
// have.
func (a *App) storesOnDatabase(d *database) bool {
	if a.opts.memory || d.engine == nil {
		return false
	}
	if a.running() {
		r := a.runOf(d)
		return r != nil && r.open()
	}
	if a.cfg.env != EnvDev {
		return true
	}
	_, _, err := a.databaseURL(context.Background(), d)
	return err == nil
}

// Tables ------------------------------------------------------------------

// tableName is a kit table's name on a database: the model's rule, which
// the analyzer names tables with too.
func tableName(service, node, suffix string) string { return model.TableName(service, node, suffix) }

// table is the store's table on the database that keeps it.
func (s *storeCore) table() string { return tableName(s.svc.name, s.name, "") }

// historyTable is where a store on a database keeps its records' former
// values.
func (s *storeCore) historyTable() string { return tableName(s.svc.name, s.name, historySuffix) }

// tabled is a store, whatever its entity type, as a database's tables see
// it.
type tabled interface {
	placementSource
	table() string
	historyTable() string
	remembers() bool
	keepsRevisions() bool
}

// remembers reports whether the store keeps former values: a history of
// its own beside its table.
func (s *storeCore) remembers() bool { return len(s.keeps()) > 0 }

// keepsRevisions reports whether the store keeps its records' versions: a
// table of them beside its own.
func (s *storeCore) keepsRevisions() bool { return s.revisions > 0 }

// kitTable is one table kit keeps on a database, and what it is for.
type kitTable struct {
	name string
	// node is the node that has it: a store, a workflow.
	node string
	// versionsOf is, for a store's versions, the store's table: the SDK
	// derives the versions' table from it (docstore.SQLVersionsMigration).
	versionsOf string
	// queue marks a consumer's queue: the SDK's SQL broker's one table, which
	// the queue subsystem migrates, not a document store's.
	queue bool
}

// kitTables are the tables kit keeps on d: each store it keeps, the former
// values of those that remember, the journals of the workflows over them,
// the queues of the consumers it keeps (ADR 0178 C1); in the order the app
// mounts them.
func (a *App) kitTables(d *database) []kitTable {
	var out []kitTable
	for _, st := range a.placedStores() {
		if t, ok := st.(tabled); ok && a.keptOn(st, d) {
			out = append(out, storeTables(t, st.base().id)...)
		}
	}
	for _, n := range a.mountedNodes() {
		out = append(out, a.nodeTables(n, d)...)
	}
	return out
}

// nodeTables are the tables kit keeps on d for a node that is no store: a
// workflow's journal, a consumer's queue.
func (a *App) nodeTables(n node, d *database) []kitTable {
	var out []kitTable
	if w, ok := n.(journaled); ok {
		if st := w.journalStore(); st != nil && a.keptOn(st, d) {
			out = append(out, kitTable{name: w.journalTable(), node: n.base().id})
		}
	}
	if q, ok := n.(queueOwner); ok {
		if under := q.queueUnder(); under != "" && a.queueKeptOn(q.base(), d) {
			out = append(out, kitTable{name: queueTable(q.base(), under), node: q.base().id, queue: true})
		}
	}
	return out
}

// keptOn reports whether st lives on d, not in memory.
func (a *App) keptOn(st placementSource, d *database) bool {
	p := a.placementOf(st)
	return p.db == d && !p.memory
}

// storeTables are the tables of the store t, node id: its own, its former
// values' when it remembers, its versions' when it keeps revisions.
func storeTables(t tabled, id string) []kitTable {
	out := []kitTable{{name: t.table(), node: id}}
	if t.remembers() {
		out = append(out, kitTable{name: t.historyTable(), node: id})
	}
	if t.keepsRevisions() {
		out = append(out, kitTable{name: t.table() + versionsSuffix, node: id, versionsOf: t.table()})
	}
	return out
}

// reservedTables are the tables kit's own set keeps on every database.
var reservedTables = map[string]string{kitTablesTable: "kit", "kit_migrations": "kit", tableProduct: "kit"}

// tableProblems are the tables two of kit's names would give one name on a
// database: two nodes whose names differ only by what the rule writes '_'
// or lowers, a digest meeting another. Each is said at the app.Database.
func (a *App) tableProblems(d *database) []string {
	seen := maps.Clone(reservedTables)
	var out []string
	for _, t := range a.kitTables(d) {
		if owner, taken := seen[t.name]; taken && owner != t.node {
			out = append(out, fmt.Sprintf("%s (%s, %s)", t.name, owner, t.node))
			continue
		}
		seen[t.name] = t.node
	}
	return out
}

// Opening a store on its database -----------------------------------------

// startSQL opens the store on the database that keeps it: the SDK's document
// store over its table — which kit's migrations made —, behind kit's sealing
// when the store seals, its index keys hashed, its index rows filed again
// when their declarations or kit's index key changed since, its history
// beside it.
func (s *storeCore) startSQL(ctx context.Context, a *App, r *databaseRun) error {
	own, err := s.kitIndexes(ctx, a)
	if err != nil {
		return err
	}
	specs := append(s.specs(), own...)
	at := sqlTable{run: r, table: s.table(), store: s.id, own: kitOwn(s.svc), versions: s.versionsOn(a)}
	if len(specs) > 0 {
		keys, err := a.referenceKeys(ctx)
		if err != nil {
			return err
		}
		at.indexKey, at.fingerprint = keys.indexKey, indexFingerprint(specsOf(specs), keys)
	}
	opened, err := s.openSQL(ctx, a, at, specs)
	if err != nil {
		return err
	}
	eng, err := s.withHistory(ctx, opened, historyOn{sql: r})
	if err != nil {
		return err
	}
	s.running(ctx, eng, r)
	return nil
}

// openSQL opens the store's engine on its table: the SDK's document store
// over SQL, or — for a store that seals (seal_store.go) — that store of
// records as they rest, a sealed member a string the table keeps as it is,
// behind kit's sealing. The index rows are filed again when their
// fingerprint changed; a record the refiling could not open refuses the
// start and leaves the fingerprint as it was, for the next start to file
// them all.
//
// IFACE-OPAQUE: the engine is kit's own port — the SQL engine, or the sealing
// one around it — and only the store holds it.
func (s *storeCore) openSQL(ctx context.Context, a *App, at sqlTable, specs []plug.IndexSpec) (plug.StoreEngine, error) {
	if !s.opensSealed(a) {
		eng, err := openSQLEngine(a, at, s.engineConfig(specs))
		if err != nil {
			return nil, s.sqlOpenRefusal(at.run, "the store cannot be opened on its database", err)
		}
		if err := at.reindex(ctx, reindexOf(eng)); err != nil {
			return nil, s.sqlOpenRefusal(at.run, "the store's index rows could not be filed again", err)
		}
		return eng, nil
	}
	z, err := a.sealing(ctx)
	if err != nil {
		return nil, err
	}
	var inner plug.StoreEngine
	se, err := openSealedEngine(s, a, z, specs, func(cfg *engineConfig) (plug.StoreEngine, error) {
		var err error
		inner, err = openSQLEngine(a, at, cfg)
		return inner, err
	})
	if err != nil {
		return nil, s.sqlOpenRefusal(at.run, "the store cannot be opened on its database", err)
	}
	if err := at.reindex(ctx, reindexOf(inner)); err != nil {
		return nil, s.sqlOpenRefusal(at.run, "the store's index rows could not be filed again", err)
	}
	return se, nil
}

// reindexOf is what files eng's index rows again — nothing for an engine
// that keeps none.
func reindexOf(eng plug.StoreEngine) func(ctx context.Context) error {
	if r, ok := eng.(plug.Reindexer); ok {
		return r.Reindex
	}
	return func(context.Context) error { return nil }
}

// sqlOpenRefusal is why the store did not open on its database, in kit's
// words: a record sealing could not open, or decode, says so; anything else
// the SDK or the database refused is kit's CodeStoreLoad, why its message.
func (s *storeCore) sqlOpenRefusal(r *databaseRun, why string, err error) error {
	switch {
	case errs.HasCode(err, CodeSealOpen), errs.HasCode(err, CodeSealKey):
		return err
	case plug.Refused(err, plug.RefusalIndexBroken):
		return failure(CodeStoreIndex, "STORE_INDEX_BROKEN", "the stored entities break a unique index", err,
			errs.String("store", s.id), errs.String("index", fieldOf(err, "index")))
	case plug.Refused(err, plug.RefusalUndecodable):
		return s.decodeFailure("", err)
	}
	return failure(CodeStoreLoad, "STORE_LOAD", why, err, errs.String("store", s.id), errs.String("database", r.d.name))
}

// sqlTable is where a store's engine opens on a database: its table, the
// key its index keys are hashed under, and what its index rows are filed
// with.
type sqlTable struct {
	run   *databaseRun
	table string
	// store is the store's node ID; own whether it is kit's: a read kit
	// makes of kit's own stores (app.Privacy's holds and journal) on the way
	// — a hold looked up before a deletion — runs on the pool, and never
	// makes a product's transaction theirs.
	store       string
	own         bool
	indexKey    func(index, key string) []byte
	fingerprint string
	// versions is how the table keeps its records' versions (ADR 0007 §3).
	versions versionsOn
}

// openSQLEngine opens the store subsystem's document store over at's
// table, of records as cfg keys and indexes them: a store's entities, the
// records a sealing store keeps of them, a store's history, a workflow's
// journal. Each call runs on the transaction its context carries for the
// database, else on the pool, within <name>-timeout (sqlCall); a hold is
// asked inside the write's transaction, with its context: a hold kit keeps
// on the same database is read in it.
func openSQLEngine(a *App, at sqlTable, cfg *engineConfig) (plug.StoreEngine, error) {
	db := at.run.database()
	if db == nil {
		return nil, Unavailable(fmt.Sprintf("database %q is not open", at.run.d.name))
	}
	sc := cfg.storeConfig(at.store, at.versions)
	sc.Database, sc.Table, sc.IndexKey = db, at.table, at.indexKey
	sc.Call = sqlCall(a, at.run, at.store, at.own)
	return openEngine(sc)
}

// sqlCall is the context of a call of the engine of store on r: on the
// transaction ctx carries for r, else on the pool — a read of kit's own
// store always there —, within the database's timeout; done ends it.
func sqlCall(a *App, r *databaseRun, store string, own bool) func(ctx context.Context, write bool) (context.Context, func(), error) {
	return func(ctx context.Context, write bool) (context.Context, func(), error) {
		if own && !write {
			ctx = withoutUnit(ctx)
		}
		return callContext(ctx, a, r, store, write)
	}
}

// reindex files the table's index rows again with fn when their
// fingerprint changed.
func (at sqlTable) reindex(ctx context.Context, fn func(context.Context) error) error {
	return at.run.reindex(ctx, at.table, at.fingerprint, fn)
}

// indexSpec is an index as its fingerprint reads it.
type indexSpec struct {
	name   string
	unique bool
}

// specsOf reads the name and uniqueness of each spec.
func specsOf(specs []plug.IndexSpec) []indexSpec {
	out := make([]indexSpec, len(specs))
	for i, sp := range specs {
		out[i] = indexSpec{name: sp.Name, unique: sp.Unique}
	}
	return out
}

// indexFingerprint says what a store's index rows were filed with: its
// indexes, their uniqueness, and kit's index key — by a tag of its own,
// never the key. The rows are filed again when it changes. A key function
// changed under the same name is not seen: rename the index.
func indexFingerprint(specs []indexSpec, keys *referenceKeys) string {
	parts := make([]string, len(specs))
	for i, sp := range specs {
		parts[i] = sp.name + uniqueMark(sp.unique)
	}
	slices.Sort(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00") + "\x00" + keys.indexKeyID()))
	return hex.EncodeToString(sum[:16])
}

// uniqueMark is what a unique index adds to its name in a fingerprint.
func uniqueMark(unique bool) string {
	if unique {
		return "!"
	}
	return ""
}

// sqlStoreRefusal is a refusal of the SDK's SQL store, or of its
// transactor, in kit's words — nil when err is none of them: a database
// that failed is Unavailable, one past <name>-timeout deadline_exceeded, a
// key longer than a column Invalid. Never a driver's text.
func sqlStoreRefusal(name string, err error) error {
	failed := plug.Refused(err, plug.RefusalStatement) || plug.Refused(err, plug.RefusalTxFailed)
	switch {
	case plug.Refused(err, plug.RefusalKeyTooLong):
		return Invalid(fmt.Sprintf("%s: a key is longer than the database's key columns hold (%s bytes)", name, fieldOf(err, "max")))
	case errors.Is(err, context.DeadlineExceeded) && failed:
		return (&Error{Status: http.StatusGatewayTimeout, Code: WireTimeout, Message: name + ": the database did not answer in time"}).Wrap(err)
	case failed:
		return Unavailable(name + ": the database did not complete the call").Wrap(err)
	case plug.Refused(err, plug.RefusalTxClosed):
		return failure(CodeDatabaseUnavailable, "TRANSACTION_CLOSED", "a store was called with the context of a transaction that ended", err)
	}
	return nil
}
