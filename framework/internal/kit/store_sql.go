// Package kit — stores on SQL: the SDK's document store over a database.
package kit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
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

// sqlEngine is a store on a database: the SDK's document store over SQL.
type sqlEngine[T any] struct {
	ds  *docstore.SQLStore[T]
	key func(T) string
	run *databaseRun
	a   *App
	// store is the store's node ID, which a refusal names.
	store string
	// own marks kit's own stores (kit.Privacy's holds and journal): a read
	// kit makes of them on the way — a hold looked up before a deletion —
	// runs on the pool, and never makes a product's transaction theirs.
	own bool
	// keep is how many former versions the table keeps of each record (ADR
	// 0007 §3), in <table>___vs: none when zero.
	keep int
	// onWrite is what the store's Watch registered: the SDK tells it each
	// write once its transaction commits, and a rollback of kit's
	// transaction tells it again, so that what reads the store reads it
	// anew.
	onWrite func(key string)
}

// call is ctx for a call of the engine: on the transaction it carries for
// the store's database, else on the pool — a read of kit's own store always
// there —, within the database's timeout; done ends it.
func (e *sqlEngine[T]) call(ctx context.Context, write bool) (context.Context, func(), error) {
	if e.own && !write {
		ctx = withoutUnit(ctx)
	}
	return callContext(ctx, e.a, e.run, e.store, write)
}

// Get reads the entity key.
func (e *sqlEngine[T]) Get(ctx context.Context, key string) (T, error) {
	c, done, err := e.call(ctx, false)
	if err != nil {
		var zero T
		return zero, err
	}
	defer done()
	return e.ds.Get(c, key)
}

// List reads every entity.
func (e *sqlEngine[T]) List(ctx context.Context) ([]T, error) {
	c, done, err := e.call(ctx, false)
	if err != nil {
		return nil, err
	}
	defer done()
	return e.ds.List(c)
}

// Filter reads the entities keep keeps.
func (e *sqlEngine[T]) Filter(ctx context.Context, keep func(T) bool) ([]T, error) {
	c, done, err := e.call(ctx, false)
	if err != nil {
		return nil, err
	}
	defer done()
	return e.ds.Filter(c, keep)
}

// Lookup reads the entity whose unique index holds key.
func (e *sqlEngine[T]) Lookup(ctx context.Context, index, key string) (T, error) {
	c, done, err := e.call(ctx, false)
	if err != nil {
		var zero T
		return zero, err
	}
	defer done()
	return e.ds.Lookup(c, index, key)
}

// Find reads the entities an index files under key.
func (e *sqlEngine[T]) Find(ctx context.Context, index, key string) ([]T, error) {
	c, done, err := e.call(ctx, false)
	if err != nil {
		return nil, err
	}
	defer done()
	return e.ds.Find(c, index, key)
}

// Count is how many entities the table holds.
func (e *sqlEngine[T]) Count(ctx context.Context) (int, error) {
	c, done, err := e.call(ctx, false)
	if err != nil {
		return 0, err
	}
	defer done()
	return e.ds.Count(c)
}

// Entries are up to limit entities as the table keeps them.
func (e *sqlEngine[T]) Entries(ctx context.Context, limit int) ([]json.RawMessage, error) {
	entries, err := e.KeyedEntries(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]json.RawMessage, len(entries))
	for i, en := range entries {
		out[i] = en.JSON
	}
	return out, nil
}

// KeyedEntries are up to limit entities as the table keeps them, with their
// keys: what a sealing store reads its records by (seal_store_engine.go).
func (e *sqlEngine[T]) KeyedEntries(ctx context.Context, limit int) ([]docstore.Entry, error) {
	c, done, err := e.call(ctx, false)
	if err != nil {
		return nil, err
	}
	defer done()
	return e.ds.Entries(c, limit)
}

// Write writes v in mode, stamped as the caller's context says.
func (e *sqlEngine[T]) Write(ctx context.Context, v T, mode writeMode) error {
	stamp := e.stamp(ctx)
	c, done, err := e.call(ctx, true)
	if err != nil {
		return err
	}
	defer done()
	switch mode {
	case insertOnly:
		err = e.ds.InsertStamped(c, v, stamp)
	case replaceOnly:
		err = e.ds.ReplaceStamped(c, v, stamp)
	default:
		err = e.ds.PutStamped(c, v, stamp)
	}
	if err == nil {
		e.retold(ctx, e.keyOf(v))
	}
	return err
}

// keyOf is v's key, "" when the key function panics: the SDK said why the
// write failed.
func (e *sqlEngine[T]) keyOf(v T) (key string) {
	defer func() {
		if recover() != nil {
			key = ""
		}
	}()
	return e.key(v)
}

// Update changes the entity key with fn, in one write.
func (e *sqlEngine[T]) Update(ctx context.Context, key string, fn func(*T) error) (T, error) {
	stamp := e.stamp(ctx)
	c, done, err := e.call(ctx, true)
	if err != nil {
		var zero T
		return zero, err
	}
	defer done()
	v, err := e.ds.UpdateStamped(c, key, stamp, fn)
	if err == nil {
		e.retold(ctx, key)
	}
	return v, err
}

// Delete removes the entity key.
func (e *sqlEngine[T]) Delete(ctx context.Context, key string) error {
	c, done, err := e.call(ctx, true)
	if err != nil {
		return err
	}
	defer done()
	err = e.ds.Delete(c, key)
	if err == nil {
		e.retold(ctx, key)
	}
	return err
}

// retold keeps key for the store's hooks to hear again if kit's transaction
// rolls its write back: the SDK drops a rolled back write's hooks, and what
// the write woke — a workflow, a retention — must read the store anew.
func (e *sqlEngine[T]) retold(ctx context.Context, key string) {
	if fn := e.onWrite; fn != nil && key != "" {
		retell(ctx, func() { fn(key) })
	}
}

// Watch registers the store's hooks with the SDK, which calls them once a
// write's transaction commits.
func (e *sqlEngine[T]) Watch(onWrite, onDelete func(key string)) {
	e.onWrite = onWrite
	e.ds.OnWrite(onWrite)
	e.ds.OnDelete(onDelete)
}

// Close closes nothing: the pool is the database's.
func (e *sqlEngine[T]) Close() error { return nil }

// databaseSource is implemented by an engine that may run on a database: the one
// it runs on, nil for none. A store's writes there take no writer turn, and
// one that remembers runs in a transaction.
type databaseSource interface{ onDatabase() *databaseRun }

// onDatabase is the database the engine runs on.
func (e *sqlEngine[T]) onDatabase() *databaseRun { return e.run }

// onDatabase is the database the engine inside runs on: a store that seals
// on a database is on it all the same.
func (e *sealedEngine[T]) onDatabase() *databaseRun {
	if b, ok := e.inner.(databaseSource); ok {
		return b.onDatabase()
	}
	return nil
}

// onDatabase is the database the engine a store's history wraps runs on.
func (h *historied[T]) onDatabase() *databaseRun {
	if b, ok := h.storeEngine.(databaseSource); ok {
		return b.onDatabase()
	}
	return nil
}

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
func (s *StoreService[T]) table() string { return tableName(s.svc.name, s.name, "") }

// historyTable is where a store on a database keeps its records' former
// values.
func (s *StoreService[T]) historyTable() string { return tableName(s.svc.name, s.name, historySuffix) }

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
func (s *StoreService[T]) remembers() bool { return len(s.keeps()) > 0 }

// keepsRevisions reports whether the store keeps its records' versions: a
// table of them beside its own.
func (s *StoreService[T]) keepsRevisions() bool { return s.revisions > 0 }

// kitTable is one table kit keeps on a database, and what it is for.
type kitTable struct {
	name string
	// node is the node that has it: a store, a workflow.
	node string
	// versionsOf is, for a store's versions, the store's table: the SDK
	// derives the versions' table from it (docstore.SQLVersionsMigration).
	versionsOf string
}

// kitTables are the tables kit keeps on d: each store it keeps, the former
// values of those that remember, the journals of the workflows over them;
// in the order the app mounts them.
func (a *App) kitTables(d *database) []kitTable {
	var out []kitTable
	for _, st := range a.placedStores() {
		if t, ok := st.(tabled); ok && a.keptOn(st, d) {
			out = append(out, storeTables(t, st.base().id)...)
		}
	}
	for _, n := range a.mountedNodes() {
		if w, ok := n.(journaled); ok {
			if st := w.journalStore(); st != nil && a.keptOn(st, d) {
				out = append(out, kitTable{name: w.journalTable(), node: n.base().id})
			}
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
// or lowers, a digest meeting another. Each is said at the kit.Database.
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
func (s *StoreService[T]) startSQL(ctx context.Context, a *App, r *databaseRun) error {
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
	eng.Watch(func(key string) { s.announce(key, false) }, func(key string) { s.announce(key, true) })
	s.mu.Lock()
	s.eng = eng
	s.mu.Unlock()
	if s.started != nil {
		s.started(ctx)
	}
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
func (s *StoreService[T]) openSQL(ctx context.Context, a *App, at sqlTable, specs []docstore.IndexSpec[T]) (storeEngine[T], error) {
	if !s.opensSealed(a) {
		eng, err := openSQLEngine(a, at, s.key, specs)
		if err != nil {
			return nil, s.sqlOpenRefusal(at.run, "the store cannot be opened on its database", err)
		}
		if err := at.reindex(ctx, eng.ds.Reindex); err != nil {
			return nil, s.sqlOpenRefusal(at.run, "the store's index rows could not be filed again", err)
		}
		return eng, nil
	}
	z, err := a.sealing(ctx)
	if err != nil {
		return nil, err
	}
	var inner *sqlEngine[sealedDoc]
	se, err := openSealedEngine(s, a, z, specs, func(key func(sealedDoc) string, specs []docstore.IndexSpec[sealedDoc]) (storeEngine[sealedDoc], error) {
		var err error
		if inner, err = openSQLEngine(a, at, key, specs); err != nil {
			return nil, err
		}
		return inner, nil
	})
	if err != nil {
		return nil, s.sqlOpenRefusal(at.run, "the store cannot be opened on its database", err)
	}
	if err := at.reindex(ctx, func(ctx context.Context) error {
		return errors.Join(inner.ds.Reindex(ctx), se.loaded())
	}); err != nil {
		return nil, s.sqlOpenRefusal(at.run, "the store's index rows could not be filed again", err)
	}
	return se, nil
}

// sqlOpenRefusal is why the store did not open on its database, in kit's
// words: a record sealing could not open, or decode, says so; anything else
// the SDK or the database refused is kit's CodeStoreLoad, why its message.
func (s *StoreService[T]) sqlOpenRefusal(r *databaseRun, why string, err error) error {
	switch {
	case errs.HasCode(err, CodeSealOpen), errs.HasCode(err, CodeSealKey):
		return err
	case errors.Is(err, docstore.IndexBroken):
		return failure(CodeStoreIndex, "STORE_INDEX_BROKEN", "the stored entities break a unique index", err,
			errs.String("store", s.id), errs.String("index", fieldOf(err, "index")))
	case errors.Is(err, docstore.DocumentUndecodable):
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
	// store is the store's node ID, own whether it is kit's (sqlEngine).
	store       string
	own         bool
	indexKey    func(index, key string) []byte
	fingerprint string
	// versions is how the table keeps its records' versions (ADR 0007 §3).
	versions versionsOn
}

// openSQLEngine opens the SDK's document store over at's table, for
// documents of type D: a store's entities, or the records a sealing store
// keeps of them.
func openSQLEngine[D any](a *App, at sqlTable, key func(D) string, specs []docstore.IndexSpec[D]) (*sqlEngine[D], error) {
	r := at.run
	cfg := docstore.SQLConfig[D]{Key: key, Transactor: r.transactor(), Dialect: r.d.dialect(), Table: at.table, IndexKey: at.indexKey}
	if vs := at.versions; vs.keep > 0 {
		// Held is asked inside the write's transaction, with its context: a
		// hold kit keeps on the same database is read in it.
		cfg.Versions, cfg.Clock, cfg.Held = vs.keep, vs.clock, vs.held
	}
	ds, err := docstore.OpenSQL(cfg, specs...)
	if err != nil {
		return nil, err
	}
	return &sqlEngine[D]{ds: ds, key: key, run: r, a: a, store: at.store, own: at.own, keep: at.versions.keep}, nil
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
func specsOf[T any](specs []docstore.IndexSpec[T]) []indexSpec {
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
	switch {
	case errors.Is(err, docstore.KeyTooLong):
		return Invalid(fmt.Sprintf("%s: a key is longer than the database's key columns hold (%d bytes)", name, docstore.MaxSQLKeyLen))
	case errors.Is(err, context.DeadlineExceeded) && (errors.Is(err, docstore.StatementFailed) || isTxFailure(err)):
		return (&Error{Status: http.StatusGatewayTimeout, Code: WireTimeout, Message: name + ": the database did not answer in time"}).Wrap(err)
	case errors.Is(err, docstore.StatementFailed), isTxFailure(err):
		return Unavailable(name + ": the database did not complete the call").Wrap(err)
	case errors.Is(err, sql.TxClosed):
		return failure(CodeDatabaseUnavailable, "TRANSACTION_CLOSED", "a store was called with the context of a transaction that ended", err)
	}
	return nil
}

// isTxFailure reports a failure of the SDK's transactor: a transaction or a
// savepoint the database would not open, commit or roll back.
func isTxFailure(err error) bool {
	for _, s := range []error{sql.BeginFailed, sql.CommitFailed, sql.RollbackFailed, sql.SavepointFailed, sql.TxPoisoned} {
		if errors.Is(err, s) {
			return true
		}
	}
	return false
}
