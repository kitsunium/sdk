package app

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/concur/snapshot"
)

// A store's two halves (ADR 0179 §2). StoreService[T] is the typed facade a
// product declares and calls: its methods encode the entity, call the store's
// core, and decode what comes back. storeCore is everything else — the
// store's place in the graph, its engine, its history, privacy, revisions,
// trash and retention —, compiled once whatever the entity type: it never
// names T. What the product declares on its own type — the key function, the
// index functions, a retention's instants — is wrapped once, at the
// declaration, into a function of the entity as an any (entityType), which
// the core calls.

// storeCore is a store without its entity type: what every store is, and
// does, compiled once.
type storeCore struct {
	nodeBase
	// self is the store as the app places it: its typed facade, which the
	// app's placement and the graph read.
	self placementSource
	// ent is the store's entity type and the product's functions on it.
	ent entityType
	// decls are the secondary indexes the declaration asked for (index.go).
	decls []indexDecl
	// passwords are the password policies of its fields (passwords.go).
	passwords []*passwordRule
	// privacy is what the store declares about personal data, its retention
	// included (retention.go); nil when it declares nothing.
	privacy *storePrivacy
	// inMemory keeps the store in memory, whatever the app keeps (InMemory).
	inMemory bool
	// readModel marks the store as derived from others (ReadModel).
	readModel bool

	mu sync.RWMutex
	// eng is the engine the store runs on, nil while the app is not running;
	// on is the database it runs on, nil for the data directory or memory.
	eng plug.StoreEngine
	on  *databaseRun
	// onWrite is told the key of every entity written, once it is stored,
	// and onDelete the key of every entity deleted: a workflow over the store
	// hears of what it did not write itself (watch).
	onWrite, onDelete []*storeHook
	// feeds are the watches (Service.Watch, watch.go) its writes are told
	// of, with the write's context — which the engine's announcements to
	// onWrite and onDelete do not carry —; nil while none hears it: the one
	// load a write pays for them. Copy-on-write, the SDK's snapshot.Value: a
	// write reads them with no lock, and a watch that starts or stops
	// publishes a new list, serialised with every other.
	feeds snapshot.Value[[]*Watch]
	// started runs once the store is open: kit's own store of data keys
	// finishes the re-wrap a stop interrupted (seal.go). nil for a product's
	// store.
	started func(ctx context.Context)
	// role says whether the store is kit's own store of data keys (seal.go):
	// apart from every transaction, and so never on SQLite (placementOf).
	role storeRole
	// revisions is how many former versions each record keeps
	// (app.Revisions, revisions.go): none when zero.
	revisions int
	// trashFor is the store's <store>-trash, declared with its revisions:
	// how long the trash keeps what the product deletes (trash.go).
	trashFor *SettingService[time.Duration]
	// bin is the running trash's agenda and loop (trash_run.go); nil while
	// none runs.
	bin atomic.Pointer[trashRun]
	// states name the state field of each workflow over the store: a
	// restore keeps them as they are, and a transition that changes only
	// one makes no version (revisions.go).
	states []func() string
}

// entityType is a store's entity type as its core sees it: the type, and
// the product's key function wrapped to take the entity as an any — a T,
// never a *T.
type entityType struct {
	typ reflect.Type
	// key is the product's key function on the entity; it panics when the
	// product's does.
	key func(v any) string
	// decode decodes a record's JSON into a fresh entity.
	decode func(raw []byte) (any, error)
}

// entityOf is T as a store's core sees it, keyed by key.
func entityOf[T any](key func(T) string) entityType {
	return entityType{
		typ: reflect.TypeFor[T](),
		key: func(v any) string {
			t, _ := v.(T)
			return key(t)
		},
		decode: func(raw []byte) (any, error) {
			var v T
			err := json.Unmarshal(raw, &v)
			return v, err
		},
	}
}

// plan is the classification of the store's entity type.
func (s *storeCore) plan() *classPlan { return planOf(s.ent.typ) }

// keyOf is v's key, or "" when the key function panics: the document store
// says why the write failed.
func (s *storeCore) keyOf(v any) (key string) {
	defer func() {
		if recover() != nil {
			key = ""
		}
	}()
	// A store declared with no key function has none to call: the call
	// panics, and the key is empty.
	return s.ent.key(v)
}

// addressable is v, the entity as an any, as a settable value of the
// store's type: a copy, which reflection reads and changes; the zero value
// for none.
func (s *storeCore) addressable(v any) reflect.Value {
	rv := reflect.New(s.ent.typ).Elem()
	if v != nil {
		rv.Set(reflect.ValueOf(v))
	}
	return rv
}

// subjectOf is v's subject, as written; "" when it has none.
func (s *storeCore) subjectOf(v any) string {
	id := ""
	if v != nil {
		id, _ = s.plan().subjectOf(reflect.ValueOf(v))
	}
	return id
}
