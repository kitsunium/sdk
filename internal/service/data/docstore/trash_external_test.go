// Package docstore_test — a document's trash, one contract run on every
// engine: the memory store, the file store, and the SQL store on each
// dialect's statements over the fake engine (ADR 0143, amended for ADR 0178).
package docstore_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/service/data/docstore"
)

// trashEngine is one engine's accounts store seen through the calls the trash
// contract reads, with the SQL store's context supplied, so one scenario runs
// on every engine alike.
type trashEngine struct {
	get        func(key string) (account, error)
	list       func() ([]account, error)
	lookup     func(index, key string) (account, error)
	find       func(index, key string) ([]account, error)
	insert     func(v account) error
	put        func(v account) error
	replace    func(v account) error
	update     func(key string, fn func(*account) error) error
	del        func(key string) error
	versions   func(key string) ([]coredocstore.VersionValue, error)
	rewrite    func(key string, fn func([]coredocstore.VersionValue) ([]coredocstore.VersionValue, error)) error
	trash      func(key string) error
	restore    func(key string) error
	purge      func(key string) error
	tombstones func() ([]coredocstore.TombstoneValue, error)
	onWrite    func(fn func(string)) func()
	onDelete   func(fn func(string)) func()
}

// fileTrashEngine is the file engine's accounts store over fsys — in memory
// when fsys is nil — keeping versions former versions on clk.
func fileTrashEngine(t *testing.T, store *docstore.Store[account]) trashEngine {
	t.Helper()
	return trashEngine{
		get: store.Get, list: store.List, lookup: store.Lookup, find: store.Find,
		insert: store.Insert, put: store.Put, replace: store.Replace,
		update: func(key string, fn func(*account) error) error { _, err := store.Update(key, fn); return err },
		del:    store.Delete, versions: store.Versions, rewrite: store.RewriteVersions,
		trash: store.Trash, restore: store.Restore, purge: store.Purge, tombstones: store.Tombstones,
		onWrite: store.OnWrite, onDelete: store.OnDelete,
	}
}

// sqlTrashEngine is the SQL engine's accounts store, every call under ctx.
func sqlTrashEngine(ctx context.Context, store *docstore.SQLStore[account]) trashEngine {
	return trashEngine{
		get:    func(key string) (account, error) { return store.Get(ctx, key) },
		list:   func() ([]account, error) { return store.List(ctx) },
		lookup: func(index, key string) (account, error) { return store.Lookup(ctx, index, key) },
		find:   func(index, key string) ([]account, error) { return store.Find(ctx, index, key) },
		insert: func(v account) error { return store.Insert(ctx, v) },
		put:    func(v account) error { return store.Put(ctx, v) },
		replace: func(v account) error {
			return store.Replace(ctx, v)
		},
		update: func(key string, fn func(*account) error) error { _, err := store.Update(ctx, key, fn); return err },
		del:    func(key string) error { return store.Delete(ctx, key) },
		versions: func(key string) ([]coredocstore.VersionValue, error) {
			return store.Versions(ctx, key)
		},
		rewrite: func(key string, fn func([]coredocstore.VersionValue) ([]coredocstore.VersionValue, error)) error {
			return store.RewriteVersions(ctx, key, fn)
		},
		trash:      func(key string) error { return store.Trash(ctx, key) },
		restore:    func(key string) error { return store.Restore(ctx, key) },
		purge:      func(key string) error { return store.Purge(ctx, key) },
		tombstones: func() ([]coredocstore.TombstoneValue, error) { return store.Tombstones(ctx) },
		onWrite:    store.OnWrite, onDelete: store.OnDelete,
	}
}

// eachTrashEngine runs fn over every engine — memory, file, and the SQL store
// on each dialect — each keeping versions former versions on a clock of its
// own starting at epoch, in a subtest of its own.
func eachTrashEngine(t *testing.T, versions int, fn func(t *testing.T, e trashEngine, clk *clock.ManualClock)) {
	t.Helper()
	eachBackend(t, func(t *testing.T, fsys corevfs.FullFS) {
		clk := clock.NewManualClock(epoch)
		fn(t, fileTrashEngine(t, openVersioned(t, versionedConfig(fsys, versions, clk))), clk)
	})
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		clk := clock.NewManualClock(epoch)
		fn(t, sqlTrashEngine(t.Context(), openSQLVersioned(t, dialect, keeping(versions, clk)).store), clk)
	})
}

// tombstonesOf lists the trash, failing the test on an error.
func tombstonesOf(t *testing.T, e trashEngine) []coredocstore.TombstoneValue {
	t.Helper()
	all, err := e.tombstones()
	if err != nil {
		t.Fatalf("Tombstones() = %v", err)
	}
	return all
}

// trashedKeys returns the tombstones' keys, in order.
func trashedKeys(all []coredocstore.TombstoneValue) []string {
	out := make([]string, len(all))
	for i, tomb := range all {
		out[i] = tomb.Key
	}
	return out
}

// engineVersionsOf reads key's versions, failing the test on an error.
func engineVersionsOf(t *testing.T, e trashEngine, key string) []coredocstore.VersionValue {
	t.Helper()
	all, err := e.versions(key)
	if err != nil {
		t.Fatalf("Versions(%s) = %v", key, err)
	}
	return all
}

// TestATrashedDocumentLeavesEveryReadAndKeepsItsVersions pins Trash on every
// engine: the document is gone from Get, List, Lookup and Find, its versions
// stay readable — the trashed document first, with its number, instant and
// metadata — the trash lists it with the instant it was trashed, and OnDelete
// is told.
func TestATrashedDocumentLeavesEveryReadAndKeepsItsVersions(t *testing.T) {
	t.Parallel()
	eachTrashEngine(t, 3, func(t *testing.T, e trashEngine, clk *clock.ManualClock) {
		must(t, e.insert(account{ID: "acc_1", Name: "v1", Email: "ada@x.dev", Teams: []string{"red"}}))
		clk.Advance(time.Minute)
		must(t, e.put(account{ID: "acc_1", Name: "v2", Email: "ada@x.dev", Teams: []string{"red"}}))
		must(t, e.insert(account{ID: "acc_2", Name: "kept"}))
		before := engineVersionsOf(t, e, "acc_1")
		var mu sync.Mutex
		var deleted []string
		e.onDelete(func(key string) { mu.Lock(); deleted = append(deleted, key); mu.Unlock() })
		clk.Advance(time.Hour)
		must(t, e.trash("acc_1"))

		_, err := e.get("acc_1")
		requireCode(t, err, coredocstore.CodeDocumentNotFound, "Get of a trashed document")
		if all, listErr := e.list(); listErr != nil || !slices.Equal(ids(all), []string{"acc_2"}) {
			t.Fatalf("List() = %v, %v, want the document that was not trashed", ids(all), listErr)
		}
		_, err = e.lookup("email", "ada@x.dev")
		requireCode(t, err, coredocstore.CodeDocumentNotFound, "Lookup of a trashed document's e-mail")
		if red, findErr := e.find("team", "red"); findErr != nil || len(red) != 0 {
			t.Fatalf("Find of a trashed document's team = %v, %v", ids(red), findErr)
		}
		after := engineVersionsOf(t, e, "acc_1")
		if !slices.EqualFunc(after, before, sameVersion) {
			t.Fatalf("the trash changed the versions: %+v, want %+v", after, before)
		}
		if got := namesIn(t, after); !slices.Equal(got, []string{"v2", "v1"}) {
			t.Fatalf("a trashed document's versions hold %v", got)
		}
		tombs := tombstonesOf(t, e)
		if len(tombs) != 1 || tombs[0].Key != "acc_1" || !tombs[0].At.Equal(epoch.Add(time.Minute+time.Hour)) ||
			tombs[0].At.Location() != time.UTC {
			t.Fatalf("Tombstones() = %+v, want acc_1 trashed at %v in UTC", tombs, epoch.Add(time.Minute+time.Hour))
		}
		mu.Lock()
		defer mu.Unlock()
		if !slices.Equal(deleted, []string{"acc_1"}) {
			t.Fatalf("OnDelete was told %v", deleted)
		}
	})
}

// TestRestoreBringsATrashedDocumentBackAsItWas pins Restore on every engine:
// the document is back — the same JSON, the same versions, filed again — the
// trash no longer lists it, and OnWrite is told; a second Restore, or a
// Purge, finds nothing in the trash.
func TestRestoreBringsATrashedDocumentBackAsItWas(t *testing.T) {
	t.Parallel()
	eachTrashEngine(t, 3, func(t *testing.T, e trashEngine, clk *clock.ManualClock) {
		must(t, e.insert(account{ID: "acc_1", Name: "v1", Email: "ada@x.dev", Teams: []string{"red"}}))
		clk.Advance(time.Minute)
		must(t, e.put(account{ID: "acc_1", Name: "v2", Email: "ada@x.dev", Teams: []string{"red"}}))
		before := engineVersionsOf(t, e, "acc_1")
		must(t, e.trash("acc_1"))
		var mu sync.Mutex
		var written []string
		e.onWrite(func(key string) { mu.Lock(); written = append(written, key); mu.Unlock() })
		clk.Advance(time.Hour)
		must(t, e.restore("acc_1"))

		if a, err := e.get("acc_1"); err != nil || a.Name != "v2" {
			t.Fatalf("Get() after Restore = %+v, %v", a, err)
		}
		if a, err := e.lookup("email", "ada@x.dev"); err != nil || a.ID != "acc_1" {
			t.Fatalf("Lookup() after Restore = %+v, %v", a, err)
		}
		if red, err := e.find("team", "red"); err != nil || !slices.Equal(ids(red), []string{"acc_1"}) {
			t.Fatalf("Find() after Restore = %v, %v", ids(red), err)
		}
		if after := engineVersionsOf(t, e, "acc_1"); !slices.EqualFunc(after, before, sameVersion) {
			t.Fatalf("Restore changed the versions: %+v, want %+v", after, before)
		}
		if tombs := tombstonesOf(t, e); len(tombs) != 0 {
			t.Fatalf("Tombstones() after Restore = %+v", tombs)
		}
		mu.Lock()
		if !slices.Equal(written, []string{"acc_1"}) {
			t.Errorf("OnWrite was told %v", written)
		}
		mu.Unlock()
		err := e.restore("acc_1")
		requireCode(t, err, coredocstore.CodeDocumentNotTrashed, "a second Restore")
		quotesNothing(t, err, "acc_1")
		requireCode(t, e.purge("acc_1"), coredocstore.CodeDocumentNotTrashed, "Purge of a restored document")
		requireCode(t, e.restore("acc_404"), coredocstore.CodeDocumentNotTrashed, "Restore of nothing")
		//: a restored document is an ordinary one again.
		clk.Advance(time.Minute)
		must(t, e.put(account{ID: "acc_1", Name: "v3", Email: "ada@x.dev"}))
		if got := numbers(engineVersionsOf(t, e, "acc_1")); !slices.Equal(got, []uint64{3, 2, 1}) {
			t.Fatalf("a write after Restore left versions %v", got)
		}
	})
}

// TestPurgeEmptiesTheTrash pins Purge on every engine: the trashed document
// and its versions are gone, the trash no longer lists it, nobody is told, and
// a document written again under its key starts at version 1.
func TestPurgeEmptiesTheTrash(t *testing.T) {
	t.Parallel()
	eachTrashEngine(t, 3, func(t *testing.T, e trashEngine, _ *clock.ManualClock) {
		must(t, e.insert(account{ID: "acc_1", Name: "v1"}))
		must(t, e.put(account{ID: "acc_1", Name: "v2"}))
		must(t, e.insert(account{ID: "acc_2", Name: "other"}))
		must(t, e.trash("acc_1"))
		must(t, e.trash("acc_2"))
		var mu sync.Mutex
		var told []string
		e.onWrite(func(key string) { mu.Lock(); told = append(told, "write "+key); mu.Unlock() })
		e.onDelete(func(key string) { mu.Lock(); told = append(told, "delete "+key); mu.Unlock() })
		must(t, e.purge("acc_1"))

		_, err := e.versions("acc_1")
		requireCode(t, err, coredocstore.CodeDocumentNotFound, "Versions of a purged document")
		if got := trashedKeys(tombstonesOf(t, e)); !slices.Equal(got, []string{"acc_2"}) {
			t.Fatalf("Tombstones() after Purge = %v, want the other trashed document", got)
		}
		requireCode(t, e.purge("acc_1"), coredocstore.CodeDocumentNotTrashed, "a second Purge")
		requireCode(t, e.restore("acc_1"), coredocstore.CodeDocumentNotTrashed, "Restore of a purged document")
		mu.Lock()
		if len(told) != 0 {
			t.Errorf("Purge told the hooks %v", told)
		}
		mu.Unlock()
		must(t, e.insert(account{ID: "acc_1", Name: "again"}))
		if got := numbers(engineVersionsOf(t, e, "acc_1")); !slices.Equal(got, []uint64{1}) {
			t.Fatalf("a document written after Purge has versions %v, want 1", got)
		}
	})
}

// TestATrashedDocumentFreesItsUniqueKeys pins the decision on the unique
// indexes, on every engine: a trashed document holds none of its keys — it is
// a former version, which never does — so another document may take one, and
// Restore then refuses with UniqueKeyTaken and leaves the trash as it was.
func TestATrashedDocumentFreesItsUniqueKeys(t *testing.T) {
	t.Parallel()
	eachTrashEngine(t, 3, func(t *testing.T, e trashEngine, _ *clock.ManualClock) {
		must(t, e.insert(account{ID: "acc_1", Name: "ada", Email: "ada@x.dev"}))
		must(t, e.trash("acc_1"))
		must(t, e.insert(account{ID: "acc_2", Name: "impostor", Email: "ada@x.dev"}))

		err := e.restore("acc_1")
		requireCode(t, err, coredocstore.CodeUniqueKeyTaken, "Restore over a unique key taken meanwhile")
		quotesNothing(t, err, "acc_1", "ada@x.dev")
		if got := trashedKeys(tombstonesOf(t, e)); !slices.Equal(got, []string{"acc_1"}) {
			t.Fatalf("a refused Restore left the trash %v", got)
		}
		_, err = e.get("acc_1")
		requireCode(t, err, coredocstore.CodeDocumentNotFound, "Get after a refused Restore")
		if a, lookupErr := e.lookup("email", "ada@x.dev"); lookupErr != nil || a.ID != "acc_2" {
			t.Fatalf("a refused Restore moved the unique key: %+v, %v", a, lookupErr)
		}
		must(t, e.del("acc_2"))
		must(t, e.restore("acc_1"))
		if a, lookupErr := e.lookup("email", "ada@x.dev"); lookupErr != nil || a.ID != "acc_1" {
			t.Fatalf("Lookup() after Restore = %+v, %v", a, lookupErr)
		}
	})
}

// TestAWriteUnderATrashedKeyContinuesItsVersions pins the number kept across
// the trash, on every engine: a creation under a trashed key is a write over
// the trashed document — the next version, the trashed one becoming a former
// one — and takes it out of the trash. Replace and Update find no document,
// and so does Delete: a trashed document is not Delete's.
func TestAWriteUnderATrashedKeyContinuesItsVersions(t *testing.T) {
	t.Parallel()
	eachTrashEngine(t, 3, func(t *testing.T, e trashEngine, _ *clock.ManualClock) {
		must(t, e.insert(account{ID: "acc_1", Name: "v1"}))
		must(t, e.put(account{ID: "acc_1", Name: "v2"}))
		must(t, e.trash("acc_1"))
		requireCode(t, e.replace(account{ID: "acc_1", Name: "nope"}), coredocstore.CodeDocumentNotFound, "Replace of a trashed document")
		requireCode(t, e.update("acc_1", func(*account) error { return nil }), coredocstore.CodeDocumentNotFound, "Update of a trashed document")
		requireCode(t, e.del("acc_1"), coredocstore.CodeDocumentNotFound, "Delete of a trashed document")
		requireCode(t, e.rewrite("acc_1", func(v []coredocstore.VersionValue) ([]coredocstore.VersionValue, error) { return v, nil }),
			coredocstore.CodeDocumentNotFound, "RewriteVersions of a trashed document")
		requireCode(t, e.trash("acc_1"), coredocstore.CodeDocumentNotFound, "Trash of a trashed document")
		if got := trashedKeys(tombstonesOf(t, e)); !slices.Equal(got, []string{"acc_1"}) {
			t.Fatalf("the refused calls left the trash %v", got)
		}

		must(t, e.insert(account{ID: "acc_1", Name: "v3"}))
		all := engineVersionsOf(t, e, "acc_1")
		if got := numbers(all); !slices.Equal(got, []uint64{3, 2, 1}) {
			t.Fatalf("a creation under a trashed key left versions %v, want 3, 2, 1", got)
		}
		if got := namesIn(t, all); !slices.Equal(got, []string{"v3", "v2", "v1"}) {
			t.Fatalf("the versions hold %v", got)
		}
		if tombs := tombstonesOf(t, e); len(tombs) != 0 {
			t.Fatalf("a creation left the key in the trash: %+v", tombs)
		}
		requireCode(t, e.restore("acc_1"), coredocstore.CodeDocumentNotTrashed, "Restore after a creation")
		//: a Put, too.
		must(t, e.trash("acc_1"))
		must(t, e.put(account{ID: "acc_1", Name: "v4"}))
		if got := numbers(engineVersionsOf(t, e, "acc_1")); !slices.Equal(got, []uint64{4, 3, 2, 1}) {
			t.Fatalf("a Put under a trashed key left versions %v", got)
		}
	})
}

// TestDeleteNeverReachesTheTrash pins the bypass an erasure takes, on every
// engine: Delete removes a document and its versions at once, and the trash
// never lists it; Trash of a key that holds nothing is a miss that names no
// key.
func TestDeleteNeverReachesTheTrash(t *testing.T) {
	t.Parallel()
	eachTrashEngine(t, 3, func(t *testing.T, e trashEngine, _ *clock.ManualClock) {
		must(t, e.insert(account{ID: "acc_1", Name: "v1"}))
		must(t, e.put(account{ID: "acc_1", Name: "v2"}))
		must(t, e.del("acc_1"))
		if tombs := tombstonesOf(t, e); len(tombs) != 0 {
			t.Fatalf("Delete reached the trash: %+v", tombs)
		}
		_, err := e.versions("acc_1")
		requireCode(t, err, coredocstore.CodeDocumentNotFound, "Versions of a deleted document")
		requireCode(t, e.restore("acc_1"), coredocstore.CodeDocumentNotTrashed, "Restore of a deleted document")
		err = e.trash("acc_1")
		requireCode(t, err, coredocstore.CodeDocumentNotFound, "Trash of nothing")
		quotesNothing(t, err, "acc_1")
	})
}

// TestAStoreWithoutVersionsHasNoTrash pins that only a store keeping versions
// has a trash, on every engine: every trash call is VersionsNotKept, and the
// document is left as it was.
func TestAStoreWithoutVersionsHasNoTrash(t *testing.T) {
	t.Parallel()
	eachTrashEngine(t, 0, func(t *testing.T, e trashEngine, _ *clock.ManualClock) {
		must(t, e.insert(account{ID: "acc_1", Name: "v1"}))
		requireCode(t, e.trash("acc_1"), coredocstore.CodeVersionsNotKept, "Trash")
		requireCode(t, e.restore("acc_1"), coredocstore.CodeVersionsNotKept, "Restore")
		requireCode(t, e.purge("acc_1"), coredocstore.CodeVersionsNotKept, "Purge")
		_, err := e.tombstones()
		requireCode(t, err, coredocstore.CodeVersionsNotKept, "Tombstones")
		if a, getErr := e.get("acc_1"); getErr != nil || a.Name != "v1" {
			t.Fatalf("Get() = %+v, %v", a, getErr)
		}
	})
}

// TestTombstonesAreInKeyOrder pins the trash's order on every engine: by key,
// whatever order the documents went in.
func TestTombstonesAreInKeyOrder(t *testing.T) {
	t.Parallel()
	eachTrashEngine(t, 1, func(t *testing.T, e trashEngine, clk *clock.ManualClock) {
		for _, key := range []string{"acc_3", "acc_1", "acc_2"} {
			must(t, e.insert(account{ID: key}))
			clk.Advance(time.Second)
			must(t, e.trash(key))
		}
		tombs := tombstonesOf(t, e)
		if got := trashedKeys(tombs); !slices.Equal(got, []string{"acc_1", "acc_2", "acc_3"}) {
			t.Fatalf("Tombstones() = %v, want key order", got)
		}
		if !tombs[2].At.Equal(epoch.Add(time.Second)) || !tombs[0].At.Equal(epoch.Add(2*time.Second)) {
			t.Fatalf("the instants are %v, %v", tombs[0].At, tombs[2].At)
		}
	})
}

// sameVersion reports whether two versions are the same: number, instant,
// metadata and JSON.
func sameVersion(a, b coredocstore.VersionValue) bool {
	return a.Number == b.Number && a.At.Equal(b.At) && string(a.JSON) == string(b.JSON) && len(a.Meta) == len(b.Meta)
}
