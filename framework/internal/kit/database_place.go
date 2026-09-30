// Package kit — the placement of each store: which database keeps its data.
package kit

import (
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/sql"
)

// Where a store's data is (ADR 0004): the most precise declaration wins — a
// store kept by name, then its service, then the default database, the one
// declared without Keeps —, and InMemory, on the app or on the store, wins
// over all of them. A store apart from every transaction — kit's own store
// of data keys — is never on a SQLite file: it stays in the data directory.

// placement is where a store's data is, and which database keeps it.
type placement struct {
	// db keeps the store, as the app declares it; nil when no database
	// does.
	db *database
	// memory: the store keeps its data in memory — the app or the store
	// says so, or there is no data directory.
	memory bool
}

// placementSource is implemented by every store, whatever its entity type.
type placementSource interface {
	node
	memoryOnly() bool
	// apart reports whether the store is in no transaction (Store.apart).
	apart() bool
}

// placementOf says where a store's data is. A store InMemory keeps its data
// in memory and no database keeps it: a cache, unless a database keeps it by
// name, which the declarations refuse. A store a database keeps lives on it
// once it opens (store_sql.go), in dev without its URL in the data
// directory.
//
// A store apart from every transaction is never on SQLite: kit's own store
// of data keys, which kit.Keeps(kit.Privacy) would put there, stays in the
// data directory. A SQLite file has one writer, and a data key is written
// apart, on a connection of its own, while the write that needs it may hold
// that writer — kit.Transact's transaction on the file, or the store's own
// around an update —: the key would wait for it the file's busy timeout,
// then fail.
func (a *App) placementOf(s placementSource) placement {
	p := placement{memory: a.opts.memory || s.memoryOnly() || a.dataDir == "", db: a.keeperOf(s)}
	if p.db != nil && s.apart() && p.db.dialect() == sql.DialectSQLite {
		p.db = nil
	}
	return p
}

// keeperOf is the database that keeps s: the one that keeps it by name,
// else — a cache kept by none — its service's, its module's, or the default
// database.
func (a *App) keeperOf(s placementSource) *database {
	byName, byService, byModule, byDefault := a.keepersOf(s.base())
	switch {
	case byName != nil:
		return byName
	case s.memoryOnly():
		// A cache: no database keeps it.
		return nil
	case byService != nil:
		return byService
	case byModule != nil:
		return byModule
	case !kitOwn(s.base().svc):
		// The default database. kit's own services (ADR 0006's kit.Privacy)
		// are never taken by it: they are placed by name only.
		return byDefault
	}
	return nil
}

// keepersOf are the first database that keeps a store by name, the first
// that keeps its service, the first that keeps its module, and the default
// database.
func (a *App) keepersOf(b *nodeBase) (byName, byService, byModule, byDefault *database) {
	for _, d := range a.opts.databases {
		name, svc, module := d.keepOf(b)
		byName = firstOf(byName, d, name)
		byService = firstOf(byService, d, svc)
		byModule = firstOf(byModule, d, module)
		byDefault = firstOf(byDefault, d, !d.opts.keepsGiven)
	}
	return byName, byService, byModule, byDefault
}

// keepOf says whether d keeps the store b by name, its service, and its
// module.
func (d *database) keepOf(b *nodeBase) (byName, byService, byModule bool) {
	for _, k := range d.opts.keeps {
		k := safeKept(k)
		byName = byName || k.store == b
		byService = byService || (k.svc != nil && k.svc == asKept(b.svc))
		byModule = byModule || (k.module != nil && b.svc != nil && k.module == b.svc.module)
	}
	return byName, byService, byModule
}

// firstOf is cur, or d when cur is not set and ok holds: the first database
// that does.
func firstOf(cur, d *database, ok bool) *database {
	if cur == nil && ok {
		return d
	}
	return cur
}

// safeKept is what a Keepable names, or nothing for a nil one — of any
// type: a typed nil that panics in keep names nothing too.
func safeKept(k Keeper) (named kept) {
	switch v := k.(type) {
	case nil:
		return kept{}
	case *Service:
		if v == nil {
			return kept{}
		}
	case *Module:
		if v == nil {
			return kept{}
		}
	}
	defer func() {
		if recover() != nil {
			named = kept{}
		}
	}()
	return k.keep()
}

// storesKeptBy counts the stores d keeps whose data is not in memory: the
// ones that live on it — or, in dev without its URL, in the data
// directory.
func (a *App) storesKeptBy(d *database) int {
	n := 0
	for _, s := range a.placedStores() {
		if p := a.placementOf(s); p.db == d && !p.memory {
			n++
		}
	}
	return n
}

// placedStores are the stores of the mounted services.
func (a *App) placedStores() []placementSource {
	var out []placementSource
	for _, svc := range a.services {
		if svc == nil {
			continue
		}
		nodes, _ := svc.snapshot()
		for _, n := range nodes {
			if s, ok := n.(placementSource); ok && n.base().kind == model.KindStore {
				out = append(out, s)
			}
		}
	}
	return out
}
