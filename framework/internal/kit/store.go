// Package kit — stores: a typed, keyed collection of entities.
package kit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

const (
	upsert      writeMode = iota // anything: create or replace
	insertOnly                   // nothing: a Conflict otherwise
	replaceOnly                  // an entity: a NotFound otherwise, never a resurrection
)

// StoreService is a typed, keyed collection of entities. Every read returns a copy
// and every write stores one: an entity is kept as its JSON encoding, so what
// a handler holds can never alias what the store holds, and the memory and
// file backends behave identically.
//
// With a data directory (KIT_DATA_DIR; ".kit/data" in dev) the store persists
// every write before returning, atomically — a crash never leaves a torn
// file — and a write costs one entity, whatever the store holds. Without one,
// it lives in memory. A store a database keeps ([Database], [Keeps]) lives
// in a table of it, and every call is a round trip. A store is a port (ADR
// 0004): what it runs on is its engine (store_engine.go) — the SDK's
// document store, in memory, in files or over SQL —; kit makes it a node of
// the graph, observes every call, and speaks for its refusals. Its writes
// join the transaction their context carries ([Transact]).
type StoreService[T any] struct {
	nodeBase
	key      func(T) string
	inMemory bool
	// readModel marks the store as derived from others (ReadModel).
	readModel bool
	// decls are the secondary indexes the declaration asked for (index.go).
	decls []indexDecl[T]

	mu sync.RWMutex
	// eng is the engine the store runs on, nil while the app is not running.
	eng storeEngine[T]
	// onWrite is told the key of every entity written, once it is stored,
	// and onDelete the key of every entity deleted: a workflow over the store
	// hears of what it did not write itself (watch).
	onWrite, onDelete []*storeHook
	// privacy is what the store declares about personal data, its retention
	// included (retention.go); nil when it declares nothing.
	privacy *storePrivacy[T]
	// passwords are the password policies of its fields (passwords.go).
	passwords []*PasswordPolicyService[T]
	// feeds are the watches (Service.Watch, watch.go) its writes are told
	// of, with the write's context — which the engine's announcements to
	// onWrite and onDelete do not carry —; nil while none hears it: the one
	// load a write pays for them.
	feeds atomic.Pointer[[]*Watch]
	// started runs once the store is open: kit's own store of data keys
	// finishes the re-wrap a stop interrupted (seal.go). nil for a product's
	// store.
	started func(ctx context.Context)
	// role says whether the store is kit's own store of data keys (seal.go):
	// apart from every transaction, and so never on SQLite (placementOf).
	role storeRole
	// revisions is how many former versions each record keeps
	// (kit.Revisions, revisions.go): none when zero.
	revisions int
	// states name the state field of each workflow over the store: a
	// restore keeps them as they are, and a transition that changes only
	// one makes no version (revisions.go).
	states []func() string
}

// storeRole is what a store is kept for: a product's data, or kit's own
// data keys.
type storeRole uint8

// dataKeysRole is kit's own store of data keys; the zero role is a store
// of the product's, or of kit's own services.
const dataKeysRole storeRole = 1

// storeHook is one function a store tells a key: registered by watch, and
// removed by the function watch returns — that one, whoever else watches.
type storeHook struct{ fn func(key string) }

// StoreConfigurer configures a store.
type StoreConfigurer interface {
	storeConfigure(o *storeOptions)
}

type storeOptions struct {
	inMemory  bool
	readModel bool
	indexes   []indexOption
	// privacy are the retention, hold and purpose options (retention.go).
	privacy []*privacyOption
	// revisions is kit.Revisions' n, when revisionsSet (revisions.go).
	revisions    int
	revisionsSet bool
}

// readModelOption is ReadModel's StoreOption.
type readModelOption struct{}

// writeMode says what a write expects to find under the key.
type writeMode int

// itemsSource is implemented by stores, whatever their entity type.
type itemsSource interface {
	items(limit int) []json.RawMessage
	// redacted is an item as the Studio may show it (classify_walk.go).
	redacted(raw json.RawMessage) json.RawMessage
}

// Store declares a store of entities of type T, keyed by key.
//
//go:noinline
func (s *Service) Store[T any](name string, key func(T) string, opts ...StoreConfigurer) *StoreService[T] {
	st := NewStoreService(key)
	var o storeOptions
	for _, opt := range opts {
		opt.storeConfigure(&o)
	}
	st.inMemory, st.readModel = o.inMemory, o.readModel
	st.kind, st.name, st.decl = model.KindStore, name, callerPos()
	s.add(st, true)
	if key == nil {
		s.problem(st.decl, st.id, "store.nil-key", "name", name)
	}
	st.declareIndexes(s, o.indexes)
	st.declarePrivacy(s, o.privacy)
	st.declareRevisions(s, o)
	return st
}

// storeConfigure sets the option on what it configures.
func (readModelOption) storeConfigure(o *storeOptions) { o.readModel = true }

// ReadModel marks a store as derived from others (ADR 0005): written by
// projections — the subscriptions that keep it from the write side's
// events —, read by queries. It changes nothing of how the store runs; the
// diagram draws it on its domain's read side, and the static analysis warns
// of a read model written by anything but a subscription. kit's topics are
// queues, not logs: a read model cannot be replayed from its events, and one
// fed by a topic lags the commands that feed it — a query that must read its
// own writes reads the write side.
//
//	var Summaries = Service.Store("summaries", Summary.Key, kit.ReadModel())
func ReadModel() StoreConfigurer { return readModelOption{} }

// notRunning is the error of a building block used while its service is not
// mounted in a running app.
func notRunning(n *nodeBase) error {
	return Unavailable(fmt.Sprintf("%s %q is not running: is service %q mounted in the app, and the app started?", n.kind, n.name, n.svc.name))
}

// engine is the engine the store runs on, or nil while its app does not run.
//
// IFACE-PLUGIN: the store runs on any engine — documents, history, SQL — each
// behind this port.
func (s *StoreService[T]) engine() storeEngine[T] {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.eng
}

// Get returns the entity with the given key, or a [NotFound] error.
func (s *StoreService[T]) Get(ctx context.Context, key string) (T, error) {
	return s.get(ctx, key, model.EdgeReads)
}

// List returns every entity, ordered by key.
func (s *StoreService[T]) List(ctx context.Context) ([]T, error) {
	return s.list(ctx, model.EdgeReads, "List")
}

// Count returns how many entities the store holds.
func (s *StoreService[T]) Count(ctx context.Context) (int, error) {
	a := s.app()
	if a == nil {
		return 0, notRunning(&s.nodeBase)
	}
	ctx, sp := a.begin(ctx, &spanStart{node: s.id, from: currentNode(ctx), edge: model.EdgeReads, op: model.OpRead, name: "Count"})
	n, err := 0, error(nil)
	if eng := s.engine(); eng != nil {
		n, err = eng.Count(ctx)
		err = s.said(err, "", "")
	} else {
		err = notRunning(&s.nodeBase)
	}
	sp.end(err)
	return n, err
}

// Put stores the entity, replacing any entity with the same key.
func (s *StoreService[T]) Put(ctx context.Context, v T) error {
	return s.put(ctx, v, upsert, model.EdgeWrites, "Put")
}

// Insert stores a new entity; it is a [Conflict] error when the key exists.
func (s *StoreService[T]) Insert(ctx context.Context, v T) error {
	return s.put(ctx, v, insertOnly, model.EdgeWrites, "Insert")
}

// Update applies fn to the entity with the given key and stores the result,
// atomically with respect to every other write of this store. fn must not
// change the key. An error from fn leaves the entity untouched.
//
// fn runs while the store's writers wait — on a database, inside the
// write's transaction, the entity locked —: it may read this store, but must
// not write to it, nor fire a workflow over it — either would wait for the
// write fn is part of.
func (s *StoreService[T]) Update(ctx context.Context, key string, fn func(*T) error) (T, error) {
	return s.update(ctx, key, fn, model.EdgeWrites)
}

// Delete removes the entity with the given key, or returns a [NotFound] error.
func (s *StoreService[T]) Delete(ctx context.Context, key string) error {
	a := s.app()
	if a == nil {
		return notRunning(&s.nodeBase)
	}
	ctx, sp := a.begin(ctx, &spanStart{node: s.id, from: currentNode(ctx), edge: model.EdgeWrites, op: model.OpWrite, name: "Delete"})
	// The writer turn before the hold lock: it is a write's outermost lock
	// (transact_turn.go).
	ctx, release, err := s.turn(ctx)
	if err == nil {
		// A legal hold refuses a deletion, the product's own included (hold.go).
		err = s.removeUnlessHeld(ctx, a, key)
	}
	release()
	sp.end(err)
	return err
}

// turn is what a write of the store needs before it runs: the data's
// writer turn, for a store in the data directory or in memory
// (transact_turn.go); nothing for a store on a database, nor for a store
// apart.
func (s *StoreService[T]) turn(ctx context.Context) (context.Context, func(), error) {
	a := s.app()
	if a == nil || s.apart() {
		return ctx, func() {}, nil
	}
	return a.writeTurn(ctx, s.local(), s.id)
}

// local reports whether the store keeps its data in the data directory or
// in memory — not on a database: its writes take the writer turn.
func (s *StoreService[T]) local() bool {
	b, ok := s.engine().(databaseSource)
	return !ok || b.onDatabase() == nil
}

// apart reports whether the store is in no transaction — its writes land
// and stay, and take no writer turn —: a cache, kept in memory by its own
// InMemory, in a test's app kept in memory as in production; and kit's own
// store of data keys (seal.go), since a key a rollback took back would still
// seal from the SDK's cache what could then never be opened. A store apart
// is never on SQLite, whose one writer a transaction holds (placementOf).
func (s *StoreService[T]) apart() bool { return s.inMemory || s.role == dataKeysRole }

// outside is ctx for a call on the store's engine: outside any transaction
// for a store apart.
func (s *StoreService[T]) outside(ctx context.Context) context.Context {
	if s.apart() {
		return withoutUnit(ctx)
	}
	return ctx
}

// remove deletes the entity under key: one of the four funnels every write
// goes through, which tell the store's watches once kit confirms it
// (watch.go).
func (s *StoreService[T]) remove(ctx context.Context, key string) error {
	eng := s.engine()
	if eng == nil {
		return notRunning(&s.nodeBase)
	}
	ctx, release, err := s.turn(ctx)
	defer release()
	if err != nil {
		return err
	}
	ctx = s.outside(ctx)
	err = s.said(eng.Delete(ctx, key), key, "")
	if err == nil {
		s.notify(ctx, key, true)
	}
	return err
}

// missing is the error of a read of key the store does not hold.
func (s *StoreService[T]) missing(key string) error {
	return NotFound(fmt.Sprintf("%s: no entity with key %q", s.name, clip(key)))
}

// get reads the entity key along an edge of kind edge.
func (s *StoreService[T]) get(ctx context.Context, key string, edge model.EdgeKind) (T, error) {
	var zero T
	a := s.app()
	if a == nil {
		return zero, notRunning(&s.nodeBase)
	}
	ctx, sp := a.begin(ctx, &spanStart{node: s.id, from: currentNode(ctx), edge: edge, op: model.OpRead, name: "Get"})
	v, err := s.read(ctx, key)
	sp.end(err)
	return v, err
}

// read decodes one entity, without observing it.
func (s *StoreService[T]) read(ctx context.Context, key string) (T, error) {
	var zero T
	eng := s.engine()
	if eng == nil {
		return zero, notRunning(&s.nodeBase)
	}
	v, err := eng.Get(ctx, key)
	return v, s.said(err, key, "")
}

// decodeFailure reports a stored entity that no longer fits its type: the
// type changed under data written by an older version of the product.
func (s *StoreService[T]) decodeFailure(key string, err error) error {
	return failure(CodeStoreDecode, "STORE_DECODE", "a stored entity does not match its type", err,
		errs.String("store", s.id), errs.String("key", key))
}

// list reads every entity along an edge of kind edge, for the operation name.
func (s *StoreService[T]) list(ctx context.Context, edge model.EdgeKind, name string) ([]T, error) {
	a := s.app()
	if a == nil {
		return nil, notRunning(&s.nodeBase)
	}
	ctx, sp := a.begin(ctx, &spanStart{node: s.id, from: currentNode(ctx), edge: edge, op: model.OpRead, name: name})
	out, err := s.all(ctx)
	sp.end(err)
	return out, err
}

// all decodes every entity, ordered by key, without observing it.
func (s *StoreService[T]) all(ctx context.Context) ([]T, error) {
	eng := s.engine()
	if eng == nil {
		return nil, notRunning(&s.nodeBase)
	}
	out, err := eng.List(ctx)
	return out, s.said(err, "", "")
}

// put writes v in mode along an edge of kind edge, for the operation name.
func (s *StoreService[T]) put(ctx context.Context, v T, mode writeMode, edge model.EdgeKind, name string) error {
	a := s.app()
	if a == nil {
		return notRunning(&s.nodeBase)
	}
	ctx, sp := a.begin(ctx, &spanStart{node: s.id, from: currentNode(ctx), edge: edge, op: model.OpWrite, name: name})
	err := s.write(ctx, v, mode)
	sp.end(err)
	return err
}

// write stores one entity; the engine keeps it before returning, and a
// failed write changes nothing. A funnel: a write kit confirms is told to
// the store's watches.
func (s *StoreService[T]) write(ctx context.Context, v T, mode writeMode) error {
	eng := s.engine()
	if eng == nil {
		return notRunning(&s.nodeBase)
	}
	ctx, release, err := s.turn(ctx)
	defer release()
	if err != nil {
		return err
	}
	ctx = s.outside(ctx)
	key := s.keyOf(v)
	err = s.said(eng.Write(ctx, v, mode), key, "")
	if err == nil {
		s.notify(ctx, key, false)
	}
	return err
}

// keyOf is v's key, or "" when the key function panics: the document store
// says why the write failed.
func (s *StoreService[T]) keyOf(v T) (key string) {
	defer func() {
		if recover() != nil {
			key = ""
		}
	}()
	return s.key(v)
}

// update changes the entity key with fn, under the store's lock, along an edge
// of kind edge.
func (s *StoreService[T]) update(ctx context.Context, key string, fn func(*T) error, edge model.EdgeKind) (T, error) {
	var v T
	a := s.app()
	if a == nil {
		return v, notRunning(&s.nodeBase)
	}
	ctx, sp := a.begin(ctx, &spanStart{node: s.id, from: currentNode(ctx), edge: edge, op: model.OpWrite, name: "Update"})
	v, err := s.modify(ctx, key, fn)
	sp.end(err)
	return v, err
}

// modify runs a read-modify-write, atomic with respect to every other write.
// A funnel: a write kit confirms is told to the store's watches — not one
// fn refused.
func (s *StoreService[T]) modify(ctx context.Context, key string, fn func(*T) error) (T, error) {
	var zero T
	eng := s.engine()
	if eng == nil {
		return zero, notRunning(&s.nodeBase)
	}
	ctx, release, err := s.turn(ctx)
	defer release()
	if err != nil {
		return zero, err
	}
	ctx = s.outside(ctx)
	v, err := eng.Update(ctx, key, fn)
	if err = s.said(err, key, ""); err == nil {
		s.notify(ctx, key, false)
	}
	return v, err
}

// storeRefusal is what a store's refusal quotes: the store, and the key and
// the index the call named.
type storeRefusal struct {
	name, id, key, index string
}

// storeRefusals are kit's words for the store engine's refusals. A key an
// index names is never quoted: it is often something a caller must not
// learn back — an e-mail, the hash of a token.
var storeRefusals = []struct {
	sentinel error
	said     func(r *storeRefusal, err error) error
}{
	{docstore.DocumentNotFound, func(r *storeRefusal, _ error) error {
		return NotFound(fmt.Sprintf("%s: no entity has this key in the index %q", r.name, clip(r.index)))
	}},
	{docstore.DocumentExists, func(r *storeRefusal, _ error) error {
		return Conflict(fmt.Sprintf("%s: an entity with key %q already exists", r.name, clip(r.key)))
	}},
	{docstore.UniqueKeyTaken, func(r *storeRefusal, _ error) error {
		return Conflict(fmt.Sprintf("%s: another entity already has this key in the unique index %q", r.name, r.index))
	}},
	{docstore.DocumentKeyEmpty, func(r *storeRefusal, _ error) error {
		return Invalid(fmt.Sprintf("%s: an entity's key cannot be empty", r.name))
	}},
	{docstore.DocumentKeyChanged, func(r *storeRefusal, _ error) error {
		return Invalid(fmt.Sprintf("%s: an update cannot change the key %q", r.name, clip(r.key)))
	}},
	{docstore.IndexUnknown, func(r *storeRefusal, _ error) error {
		return Invalid(fmt.Sprintf("%s has no index %q", r.name, clip(r.index)))
	}},
	{docstore.IndexNotUnique, func(r *storeRefusal, _ error) error {
		return Invalid(fmt.Sprintf("%s: the index %q is not unique: read it with Find", r.name, clip(r.index)))
	}},
	{docstore.DocumentUnencodable, func(r *storeRefusal, err error) error {
		return failure(CodeStoreEncode, "STORE_ENCODE", "the entity cannot be stored", err, errs.String("store", r.id), errs.String("key", r.key))
	}},
	{docstore.PersistFailed, func(r *storeRefusal, err error) error {
		return failure(CodeStorePersist, "STORE_PERSIST", "the store could not be written", err, errs.String("store", r.id))
	}},
}

// said is an engine's refusal — the document store's sentinels, which every
// engine speaks (store_engine.go) — in kit's words: a [NotFound], a
// [Conflict], an [Invalid] or a failure with kit's code — never the SDK's
// error as it is, and a key quoted only where kit always quoted one. index is
// the index a Lookup or a Find read. An error of the product's own — what an
// Update's function returned — comes back as it is.
func (s *StoreService[T]) said(err error, key, index string) error {
	if err == nil {
		return nil
	}
	if index == "" {
		index = fieldOf(err, "index")
	}
	if handled, said := s.saidOwn(err, key, index); handled {
		return said
	}
	about := storeRefusal{name: s.name, id: s.id, key: key, index: index}
	for _, r := range storeRefusals {
		if errors.Is(err, r.sentinel) {
			return r.said(&about, err)
		}
	}
	if refusal := sqlStoreRefusal(s.name, err); refusal != nil {
		return refusal
	}
	return err
}

// saidOwn is kit's word for the refusals the store words itself: an
// unconfirmed write, a missing key, an undecodable entity, a closed store.
func (s *StoreService[T]) saidOwn(err error, key, index string) (handled bool, said error) {
	switch {
	case errors.Is(err, docstore.WriteUnconfirmed):
		// The write stands — the store and its file show it —, only its
		// survival across a power loss is in doubt: said, not failed.
		if a := s.app(); a != nil {
			a.problem(s.id, say("store.unconfirmed", "store", s.id))
		}
		return true, nil
	case errors.Is(err, docstore.DocumentNotFound) && index == "":
		return true, s.missing(key)
	case errors.Is(err, docstore.DocumentUndecodable):
		return true, s.decodeFailure(key, err)
	case errors.Is(err, docstore.StoreClosed):
		return true, notRunning(&s.nodeBase)
	default:
		return false, nil
	}
}

// fieldOf is the value of the field named key along err's chain, or "".
func fieldOf(err error, key string) string {
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == key {
			return f.StringValue()
		}
	}
	return ""
}

// start opens the store's engine where the app places it: on the database
// that keeps it, once open (store_sql.go); else the document store under
// the data directory, loaded — the snapshot, what was written since
// replayed on it, the indexes rebuilt — or in memory.
func (s *StoreService[T]) start(ctx context.Context, a *App) error {
	if r := a.databaseOf(s); r != nil {
		return s.startSQL(ctx, a, r)
	}
	var where storeFS
	if a.data != nil && !s.inMemory {
		where = storeFS{fs: a.data, path: s.file()}
	}
	// kit's own index — a subject's reference (privacy_keys.go) — is one
	// more spec, kept with the product's in the same write.
	own, err := s.kitIndexes(ctx, a)
	if err != nil {
		return err
	}
	opened, err := s.open(ctx, a, where, append(s.specs(), own...))
	if err != nil {
		return s.openRefusal(where, err)
	}
	// What the store remembers of its records, and its password policies,
	// wrap its engine: every write goes through them (history.go).
	eng, err := s.withHistory(ctx, opened, historyOn{files: where})
	if err != nil {
		return errors.Join(err, opened.Close())
	}
	// The hooks are kit's lists, read at each write: a workflow mounted
	// after the store starts still hears of every write.
	eng.Watch(func(key string) { s.announce(key, false) }, func(key string) { s.announce(key, true) })
	s.mu.Lock()
	s.eng = eng
	s.mu.Unlock()
	if s.started != nil {
		s.started(ctx)
	}
	return nil
}

// openRefusal is why the store did not open in the data directory or in
// memory, in kit's words: a record sealing could not open says so, a unique
// index the data breaks, a record that does not decode, files that keep
// versions the store no longer does; anything else is kit's CodeStoreLoad.
func (s *StoreService[T]) openRefusal(where storeFS, err error) error {
	switch {
	case errs.HasCode(err, CodeSealOpen), errs.HasCode(err, CodeSealKey):
		return err
	case errors.Is(err, docstore.IndexBroken):
		return failure(CodeStoreIndex, "STORE_INDEX_BROKEN", "the stored entities break a unique index", err,
			errs.String("store", s.id), errs.String("index", fieldOf(err, "index")))
	case errors.Is(err, docstore.DocumentUndecodable):
		return s.decodeFailure("", err)
	case errors.Is(err, docstore.LoadFailed) && strings.Contains(fieldOf(err, "problem"), "keeps versions"):
		// Its files keep versions the store no longer does (ADR 0007 §3):
		// the document store refuses to drop them unasked.
		return explain(CodeStoreLoad, "STORE_VERSIONS",
			"the store's files keep versions: declare kit.Revisions again, or remove its .versions file to drop them",
			err, errs.String("store", s.id), errs.String("file", fieldOf(err, "file")))
	}
	return failure(CodeStoreLoad, "STORE_LOAD", "the store cannot be opened", err, errs.String("store", s.id), errs.String("file", where.path))
}

// open opens the store's engine where the app places it: the SDK's
// document store, and — for a store on disk whose entity has a member kit
// seals, or any product store on disk of an app that seals — the document
// store behind kit's sealing (seal_store.go), which opens what an earlier
// classification sealed too. A store in memory keeps nothing at rest, and
// seals nothing.
//
// IFACE-OPAQUE: the engine is kit's own port — the document store, or the
// sealing one around it — and only the store holds it.
func (s *StoreService[T]) open(ctx context.Context, a *App, where storeFS, specs []docstore.IndexSpec[T]) (storeEngine[T], error) {
	if where.fs == nil || !s.opensSealed(a) {
		eng, err := openDocEngine(s.key, where, specs, s.versionsOn(a))
		if err != nil {
			return nil, err
		}
		eng.name = s.id
		return eng, nil
	}
	z, err := a.sealing(ctx)
	if err != nil {
		return nil, err
	}
	eng, err := openSealedEngine(s, a, z, specs, func(key func(sealedDoc) string, specs []docstore.IndexSpec[sealedDoc]) (storeEngine[sealedDoc], error) {
		inner, err := openDocEngine(key, where, specs, s.versionsOn(a))
		if err != nil {
			return nil, err
		}
		inner.name = s.id
		return inner, nil
	})
	if err != nil {
		return nil, err
	}
	return eng, nil
}

// opensSealed reports whether the store opens behind kit's sealing where it
// rests — on disk, or on a database —: its entity has a member kit seals, or
// it is a product store of an app that keeps data keys, which opens what an
// earlier classification sealed.
func (s *StoreService[T]) opensSealed(a *App) bool {
	return s.plan().sealsAny() || (a.privacyKeyStore() != nil && !kitOwn(s.svc))
}

// sealsAtRest reports whether the store seals what it keeps: its entity has
// a member kit seals, and the app keeps the store on disk — a store in
// memory keeps nothing at rest.
func (s *StoreService[T]) sealsAtRest(a *App) bool {
	return a != nil && s.plan().sealsAny() && !a.placementOf(s).memory
}

// announce tells the hooks a key was written or deleted, outside every lock.
func (s *StoreService[T]) announce(key string, deleted bool) {
	s.mu.RLock()
	hooks := s.onWrite
	if deleted {
		hooks = s.onDelete
	}
	hooks = slices.Clone(hooks)
	s.mu.RUnlock()
	for _, h := range hooks {
		h.fn(key)
	}
}

// watch registers a function told of every write and one told of every
// deletion, and returns the function that removes exactly these two: a
// workflow that fails to start, or stops, takes back its own hooks and no
// one else's.
func (s *StoreService[T]) watch(onWrite, onDelete func(key string)) (unwatch func()) {
	w, d := &storeHook{fn: onWrite}, &storeHook{fn: onDelete}
	s.mu.Lock()
	s.onWrite = append(s.onWrite, w)
	s.onDelete = append(s.onDelete, d)
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.onWrite = slices.DeleteFunc(slices.Clone(s.onWrite), func(h *storeHook) bool { return h == w })
		s.onDelete = slices.DeleteFunc(slices.Clone(s.onDelete), func(h *storeHook) bool { return h == d })
	}
}

// file is where the store lives under the data directory: its snapshot,
// beside which the document store keeps what was written since.
func (s *StoreService[T]) file() string { return s.svc.name + "/" + s.name + ".json" }

// stop closes the store's engine: the document store folds what was
// written into its snapshot first.
func (s *StoreService[T]) stop(_ context.Context, _ *App) error {
	s.mu.Lock()
	eng := s.eng
	s.eng = nil
	s.mu.Unlock()
	if eng == nil {
		return nil
	}
	if err := eng.Close(); err != nil {
		return failure(CodeStorePersist, "STORE_PERSIST", "the store could not be written", err, errs.String("store", s.id))
	}
	return nil
}

// describe fills the graph node out with what the Store declares, and returns
// its edges.
func (s *StoreService[T]) describe(a *App, out *model.Node) []model.Edge {
	backend, location, database := s.whereKept(a)
	table := ""
	if database != "" {
		table = s.table()
	}
	out.Store = &model.StoreInfo{
		Entity: keptSchemaOf(reflect.TypeFor[T](), s.sealsAtRest(a)), Backend: backend, Location: location, Database: database, Table: table,
		Indexes: s.indexInfo(), Privacy: s.privacyInfo(a), History: s.historyInfo(a), ReadModel: s.readModel,
	}
	if a != nil && a.running() {
		out.Store.Count = s.count()
	}
	return nil
}

// whereKept says where app a keeps the store's data: in memory or in a file,
// and in which database when one keeps it.
func (s *StoreService[T]) whereKept(a *App) (backend, location, database string) {
	backend = "memory"
	if a == nil {
		return backend, "", ""
	}
	if a.data != nil && !s.inMemory {
		backend, location = "file", s.file()
	}
	if p := a.placementOf(s); p.db != nil {
		database = p.db.name
		if !p.memory && a.storesOnDatabase(p.db) {
			backend, location = p.db.engineName(), ""
		}
	}
	return backend, location, database
}

// count is how many entities the running store holds; nil when it cannot
// say.
func (s *StoreService[T]) count() *int {
	eng := s.engine()
	if eng == nil {
		return nil
	}
	n, err := eng.Count(context.Background())
	if err != nil {
		return nil
	}
	return &n
}

// items returns up to limit raw entities, ordered by key, for the Studio.
func (s *StoreService[T]) items(limit int) []json.RawMessage {
	eng := s.engine()
	if eng == nil {
		return nil
	}
	out, err := eng.Entries(context.Background(), limit)
	if err != nil {
		return nil
	}
	return out
}

// NewStoreService is a store no service declares yet, keying its entities
// with key: [Service.Store] makes one and declares it, which is how a product
// gets one.
func NewStoreService[T any](key func(T) string) *StoreService[T] { return &StoreService[T]{key: key} }
