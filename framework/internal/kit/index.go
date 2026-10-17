package kit

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/framework/model"
)

// Secondary indexes of a store.
//
// The store's engine — the SDK's document store — keeps them beside the
// entities and in step with every write — Put, Insert, Update, Delete, a
// workflow's transition —: a reader never sees an entity and an index that
// disagree, and a write a unique index refuses leaves both exactly as they
// were. kit declares them, checks the declaration, observes their reads and
// speaks for their refusals.

// indexOption declares a secondary index. key is a func(T) []string for the
// store's T; the store checks the type when it is declared.
type indexOption struct {
	name   string
	unique bool
	key    any
}

// storeConfigure sets the option on what it configures.
func (o indexOption) storeConfigure(so *storeOptions) { so.indexes = append(so.indexes, o) }

// Unique declares a unique index: no two entities may share a key, and a
// write that would is refused with a [Conflict] error. An empty key is not
// indexed. [StoreService.Lookup] reads it.
//
//	var Accounts = Service.Store("accounts", Account.Key,
//		kit.Unique("email", func(a Account) string { return a.Email }))
func Unique[T any](name string, key func(T) string) StoreConfigurer {
	o := indexOption{name: name, unique: true, key: (func(T) []string)(nil)}
	if key != nil {
		o.key = func(v T) []string {
			if k := key(v); k != "" {
				return []string{k}
			}
			return nil
		}
	}
	return o
}

// Index declares an index where an entity may have several keys and a key
// several entities — the users a task is shared with. [StoreService.Find] reads it.
// Empty keys are not indexed.
func Index[T any](name string, keys func(T) []string) StoreConfigurer {
	return indexOption{name: name, key: keys}
}

// indexDecl is one index a store declared, once its declaration checked:
// its key function, wrapped to take the entity as an any.
type indexDecl struct {
	name   string
	unique bool
	keys   func(v any) []string
}

// Lookup returns the entity whose key in the unique index is key, or a
// [NotFound] error. Asking a non-unique index is an [Invalid] error: use
// [StoreService.Find].
func (s *StoreService[T]) Lookup(ctx context.Context, index, key string) (T, error) {
	v, err := s.lookupIn(ctx, index, key)
	return as[T](v), err
}

// Find returns the entities indexed under key, ordered by their store key. It
// reads any index, unique or not; no entity is an empty slice, not an error.
func (s *StoreService[T]) Find(ctx context.Context, index, key string) ([]T, error) {
	vs, err := s.findIn(ctx, index, key)
	return asAll[T](vs), err
}

// Filter returns the entities keep accepts, ordered by key. It reads every
// entity: an index is the way to avoid it.
func (s *StoreService[T]) Filter(ctx context.Context, keep func(T) bool) ([]T, error) {
	vs, err := s.filterIn(ctx, func(v any) bool { return keep(as[T](v)) })
	return asAll[T](vs), err
}

// lookupIn is Lookup, in a span of its own.
func (s *storeCore) lookupIn(ctx context.Context, index, key string) (any, error) {
	a := s.app()
	if a == nil {
		return nil, notRunning(&s.nodeBase)
	}
	ctx, sp := a.begin(ctx, &spanStart{node: s.id, from: currentNode(ctx), edge: model.EdgeReads, op: model.OpRead, name: "Lookup"})
	v, err := s.lookup(ctx, index, key)
	sp.end(err)
	return v, err
}

// lookup is the entity whose index names key, by a unique index.
func (s *storeCore) lookup(ctx context.Context, index, key string) (any, error) {
	eng := s.engine()
	if eng == nil {
		return nil, notRunning(&s.nodeBase)
	}
	d, err := eng.Lookup(ctx, index, key)
	if err != nil {
		return nil, s.said(err, "", index)
	}
	v, err := s.valueOf(d)
	return v, s.said(err, "", index)
}

// findIn is Find, in a span of its own.
func (s *storeCore) findIn(ctx context.Context, index, key string) ([]any, error) {
	a := s.app()
	if a == nil {
		return nil, notRunning(&s.nodeBase)
	}
	ctx, sp := a.begin(ctx, &spanStart{node: s.id, from: currentNode(ctx), edge: model.EdgeReads, op: model.OpRead, name: "Find"})
	out, err := s.find(ctx, index, key)
	sp.end(err)
	return out, err
}

// find is every entity whose index names key.
func (s *storeCore) find(ctx context.Context, index, key string) ([]any, error) {
	eng := s.engine()
	if eng == nil {
		return nil, notRunning(&s.nodeBase)
	}
	ds, err := eng.Find(ctx, index, key)
	if err != nil {
		return nil, s.said(err, "", index)
	}
	out, err := s.valuesOf(ds)
	if err != nil {
		return nil, s.said(err, "", index)
	}
	return out, nil
}

// filterIn is Filter, in a span of its own: every entity read, then kept or
// left.
func (s *storeCore) filterIn(ctx context.Context, keep func(v any) bool) ([]any, error) {
	a := s.app()
	if a == nil {
		return nil, notRunning(&s.nodeBase)
	}
	ctx, sp := a.begin(ctx, &spanStart{node: s.id, from: currentNode(ctx), edge: model.EdgeReads, op: model.OpRead, name: "Filter"})
	all, err := s.all(ctx)
	var out []any
	if err == nil {
		out = all[:0]
		for _, v := range all {
			if keep(v) {
				out = append(out, v)
			}
		}
	}
	sp.end(err)
	return out, err
}

// declareIndexes checks the index options of a store once it is registered:
// each key function must read the store's own entity type, and each name
// must be usable and unique within the store. A mistake is a declaration
// problem, reported with the store's position.
func (s *StoreService[T]) declareIndexes(svc *Service, opts []indexOption) {
	want := reflect.TypeFor[T]()
	for _, o := range opts {
		switch keys, ok := o.key.(func(T) []string); {
		case !model.ValidName(o.name):
			svc.problem(s.decl, s.id, "index.name", "store", s.name, "name", o.name)
		case slices.ContainsFunc(s.decls, func(d indexDecl) bool { return d.name == o.name }):
			svc.problem(s.decl, s.id, "index.twice", "store", s.name, "name", o.name)
		case !ok:
			svc.problem(s.decl, s.id, "index.type", "store", s.name, "type", want, "name", o.name, "reads", keyType(o.key))
		case keys == nil:
			svc.problem(s.decl, s.id, "index.nil", "store", s.name, "name", o.name)
		default:
			s.decls = append(s.decls, indexDecl{name: o.name, unique: o.unique, keys: func(v any) []string { return keys(as[T](v)) }})
		}
	}
}

// indexed reports whether the store files index keys: an index it
// declares, or kit's subject index (privacy_keys.go) — what a database's
// table files under kit's index key.
func (s *storeCore) indexed() bool {
	return len(s.decls) > 0 || (s.plan().subject != nil && !kitOwn(s.svc))
}

// specs are the declared indexes as the engine takes them, over documents:
// a document whose entity no longer decodes is filed under no key.
func (s *storeCore) specs() []plug.IndexSpec {
	out := make([]plug.IndexSpec, len(s.decls))
	for i, d := range s.decls {
		keys := d.keys
		out[i] = plug.IndexSpec{Name: d.name, Unique: d.unique, Keys: func(doc plug.DocumentValue) []string {
			if doc.Value == nil {
				return nil
			}
			return keys(doc.Value)
		}}
	}
	return out
}

// keyType names the entity type an index's key function reads.
func keyType(key any) string {
	if t := reflect.TypeOf(key); t != nil && t.Kind() == reflect.Func && t.NumIn() == 1 {
		return t.In(0).String()
	}
	return fmt.Sprintf("%T", key)
}

// indexInfo describes the indexes, in declaration order.
func (s *storeCore) indexInfo() []model.IndexInfo {
	if len(s.decls) == 0 {
		return nil
	}
	out := make([]model.IndexInfo, len(s.decls))
	for i, d := range s.decls {
		out[i] = model.IndexInfo{Name: d.name, Unique: d.unique}
	}
	return out
}
