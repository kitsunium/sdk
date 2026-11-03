// Package docstore_test — several writes as one (#1057, ADR 0209, amended
// 2026-11-02): one contract run on every engine — memory, file, and the SQL
// store on each dialect over the fake engine — and the file engine's group
// entry through a crash.
package docstore_test

import (
	"context"
	"errors"
	"path"
	"slices"
	"sync"
	"testing"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/docstore"
)

// batchEngine is one engine's store seen through Batch and the reads the
// batch contract checks, the SQL store's context supplied.
type batchEngine struct {
	storeEngine
	batch func(fn func(w coredocstore.BatchWriter[account]) error) error
}

// eachBatchEngine runs fn over every engine, each keeping versions former
// versions, in a subtest of its own.
func eachBatchEngine(t *testing.T, versions int, fn func(t *testing.T, e batchEngine)) {
	t.Helper()
	eachBackend(t, func(t *testing.T, fsys corevfs.FullFS) {
		store := openVersioned(t, versionedConfig(fsys, versions, clock.NewManualClock(epoch)))
		fn(t, batchEngine{storeEngine: fileStoreEngine(t, store), batch: store.Batch})
	})
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		ctx, store := t.Context(), openSQLVersioned(t, dialect, keeping(versions, clock.NewManualClock(epoch))).store
		fn(t, batchEngine{
			storeEngine: sqlStoreEngine(ctx, store),
			batch: func(fn func(w coredocstore.BatchWriter[account]) error) error {
				return store.Batch(ctx, fn)
			},
		})
	})
}

// TestABatchWhoseLastWriteIsRefusedChangesNothing pins the contract: a batch
// whose last write a unique index refuses — a key one of its own earlier writes
// took — leaves no document of the batch stored or deleted, and tells no
// hook, on every engine.
func TestABatchWhoseLastWriteIsRefusedChangesNothing(t *testing.T) {
	t.Parallel()
	eachBatchEngine(t, 2, func(t *testing.T, e batchEngine) {
		must(t, e.insert(account{ID: "acc_0", Email: "zero@x.dev"}))
		var mu sync.Mutex
		var told []string
		e.onWrite(func(key string) { mu.Lock(); told = append(told, key); mu.Unlock() })
		e.onDelete(func(key string) { mu.Lock(); told = append(told, key); mu.Unlock() })
		err := e.batch(func(w coredocstore.BatchWriter[account]) error {
			must(t, w.Put(account{ID: "acc_1", Email: "ada@x.dev"}))
			must(t, w.Delete("acc_0"))
			must(t, w.Insert(account{ID: "acc_2", Email: "ada@x.dev"}))
			return nil
		})
		requireCode(t, err, coredocstore.CodeUniqueKeyTaken, "a batch whose last write takes a key its first took")
		quotesNothing(t, err, "acc_1", "acc_2", "ada@x.dev")
		if all, listErr := e.list(); listErr != nil || !slices.Equal(ids(all), []string{"acc_0"}) {
			t.Fatalf("List() after a refused batch = %v, %v, want acc_0 alone", ids(all), listErr)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(told) != 0 {
			t.Fatalf("a refused batch told the hooks %v", told)
		}
	})
}

// TestABatchSeesItsOwnWrites pins the order: each write of a batch is decided
// over the store and the batch's writes before it — an insertion then a
// replacement of the same key, a key freed by one write and taken by the next,
// a deletion then a creation — and the hooks are told every write, in order,
// once the batch took effect.
func TestABatchSeesItsOwnWrites(t *testing.T) {
	t.Parallel()
	eachBatchEngine(t, 2, func(t *testing.T, e batchEngine) {
		must(t, e.insert(account{ID: "acc_0", Email: "ada@x.dev"}))
		var mu sync.Mutex
		var told []string
		e.onWrite(func(key string) { mu.Lock(); told = append(told, "write "+key); mu.Unlock() })
		e.onDelete(func(key string) { mu.Lock(); told = append(told, "delete "+key); mu.Unlock() })
		must(t, e.batch(func(w coredocstore.BatchWriter[account]) error {
			must(t, w.Insert(account{ID: "acc_1", Name: "draft"}))
			must(t, w.Replace(account{ID: "acc_1", Name: "final"}))
			must(t, w.Put(account{ID: "acc_0", Email: "grace@x.dev"}))
			must(t, w.Insert(account{ID: "acc_2", Email: "ada@x.dev"}))
			must(t, w.Delete("acc_1"))
			must(t, w.Insert(account{ID: "acc_1", Name: "again"}))
			return nil
		}))
		if all, err := e.list(); err != nil || !slices.Equal(ids(all), []string{"acc_0", "acc_1", "acc_2"}) {
			t.Fatalf("List() = %v, %v", ids(all), err)
		}
		if owner, err := e.lookup("email", "ada@x.dev"); err != nil || owner.ID != "acc_2" {
			t.Fatalf("Lookup(ada) = %+v, %v, want acc_2, which the batch gave the key acc_0 freed", owner, err)
		}
		if got, err := e.get("acc_1"); err != nil || got.Name != "again" {
			t.Fatalf("Get(acc_1) = %+v, %v", got, err)
		}
		//: a deletion took acc_1's versions; the creation after it is version 1.
		if got := numbers(engineVersionsOf(t, e.storeEngine, "acc_1")); !slices.Equal(got, []uint64{1}) {
			t.Fatalf("acc_1's versions = %v, want 1", got)
		}
		if got := numbers(engineVersionsOf(t, e.storeEngine, "acc_0")); !slices.Equal(got, []uint64{2, 1}) {
			t.Fatalf("acc_0's versions = %v, want 2 and 1", got)
		}
		mu.Lock()
		defer mu.Unlock()
		want := []string{"write acc_1", "write acc_1", "write acc_0", "write acc_2", "delete acc_1", "write acc_1"}
		if !slices.Equal(told, want) {
			t.Fatalf("the hooks were told %v, want %v", told, want)
		}
	})
}

// TestABatchRefusesInOrder pins the refusals a batch decides when it commits,
// each its own: a replacement or a deletion of nothing, an insertion over a
// document; and those a write answers at once, before the batch commits —
// an empty key — and fn's own error, returned as it is. Nothing changes.
func TestABatchRefusesInOrder(t *testing.T) {
	t.Parallel()
	eachBatchEngine(t, 0, func(t *testing.T, e batchEngine) {
		must(t, e.insert(account{ID: "acc_0"}))
		for name, tc := range map[string]struct {
			fn   func(w coredocstore.BatchWriter[account]) error
			code errs.Code
		}{
			"a replacement of nothing": {func(w coredocstore.BatchWriter[account]) error {
				must(t, w.Put(account{ID: "acc_1"}))
				return w.Replace(account{ID: "acc_9"})
			}, coredocstore.CodeDocumentNotFound},
			"a deletion of nothing": {func(w coredocstore.BatchWriter[account]) error {
				must(t, w.Put(account{ID: "acc_1"}))
				return w.Delete("acc_9")
			}, coredocstore.CodeDocumentNotFound},
			"an insertion over a document": {func(w coredocstore.BatchWriter[account]) error {
				must(t, w.Put(account{ID: "acc_1"}))
				return w.Insert(account{ID: "acc_0"})
			}, coredocstore.CodeDocumentExists},
			"an empty key, answered at once": {func(w coredocstore.BatchWriter[account]) error {
				must(t, w.Put(account{ID: "acc_1"}))
				return w.Put(account{})
			}, coredocstore.CodeDocumentKeyEmpty},
		} {
			requireCode(t, e.batch(tc.fn), tc.code, name)
		}
		own := errors.New("the caller changed its mind")
		if err := e.batch(func(w coredocstore.BatchWriter[account]) error {
			must(t, w.Put(account{ID: "acc_1"}))
			return own
		}); !errors.Is(err, own) {
			t.Fatalf("Batch with fn failing = %v, want fn's own error", err)
		}
		if all, err := e.list(); err != nil || !slices.Equal(ids(all), []string{"acc_0"}) {
			t.Fatalf("List() after refused batches = %v, %v, want acc_0 alone", ids(all), err)
		}
		must(t, e.batch(func(coredocstore.BatchWriter[account]) error { return nil }))
	})
}

// TestASQLBatchJoinsTheCallersTransaction pins the SQL engine's batch inside
// the caller's transaction: a savepoint of it, rolled back with it.
func TestASQLBatchJoinsTheCallersTransaction(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		fx := openSQL(t, dialect)
		rollback := errors.New("the caller rolls back")
		err := fx.tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
			must(t, fx.store.Batch(ctx, func(w coredocstore.BatchWriter[account]) error {
				must(t, w.Put(account{ID: "acc_1"}))
				return w.Put(account{ID: "acc_2"})
			}))
			if all, listErr := fx.store.List(ctx); listErr != nil || len(all) != 2 {
				t.Fatalf("List() inside the transaction = %v, %v", ids(all), listErr)
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatalf("Transact() = %v", err)
		}
		if all, listErr := fx.store.List(t.Context()); listErr != nil || len(all) != 0 {
			t.Fatalf("List() after the rollback = %v, %v, want nothing", ids(all), listErr)
		}
	})
}

// TestABatchSurvivesACrashAfterItsGroupEntry pins the file engine's group
// entry: a batch whose keys' entries could not all be written took effect
// all the same — its group entry is durable — and a process that dies then
// reopens with every document of the batch; one whose group entry could not
// be published took no effect, and reopens with none.
func TestABatchSurvivesACrashAfterItsGroupEntry(t *testing.T) {
	t.Parallel()
	batch := func(w coredocstore.BatchWriter[account]) error {
		must(t, w.Put(account{ID: "acc_1", Name: "one"}))
		must(t, w.Put(account{ID: "acc_2", Name: "two"}))
		return w.Delete("acc_0")
	}
	t.Run("after the group entry", func(t *testing.T) {
		t.Parallel()
		faulty := &faultyFS{FullFS: memFS()}
		crash(t, faulty, func(store *docstore.Store[account]) {
			must(t, store.Insert(account{ID: "acc_0"}))
			faulty.set(path.Join(overlayPath, entryNameOf(t, "acc_2")), true, false)
			must(t, store.Batch(batch))
		})
		faulty.set("", false, false)
		if got := documentIDs(t, faulty); !slices.Equal(got, []string{"acc_1", "acc_2"}) {
			t.Fatalf("a reopen after the crash holds %v, want the whole batch", got)
		}
		if left := overlayEntries(t, faulty); slices.Contains(left, "batch.group.json") {
			t.Fatalf("the reopen left the group entry: %v", left)
		}
	})
	t.Run("before the group entry", func(t *testing.T) {
		t.Parallel()
		faulty := &faultyFS{FullFS: memFS()}
		crash(t, faulty, func(store *docstore.Store[account]) {
			must(t, store.Insert(account{ID: "acc_0"}))
			faulty.set(path.Join(overlayPath, "batch.group.json"), true, false)
			requireCode(t, store.Batch(batch), coredocstore.CodePersistFailed, "a batch whose group entry was refused")
			if all, err := store.List(); err != nil || !slices.Equal(ids(all), []string{"acc_0"}) {
				t.Fatalf("List() after a refused group entry = %v, %v", ids(all), err)
			}
		})
		faulty.set("", false, false)
		if got := documentIDs(t, faulty); !slices.Equal(got, []string{"acc_0"}) {
			t.Fatalf("a reopen holds %v, want none of the batch", got)
		}
	})
	t.Run("a write after an incomplete batch completes it first", func(t *testing.T) {
		t.Parallel()
		faulty := &faultyFS{FullFS: memFS()}
		crash(t, faulty, func(store *docstore.Store[account]) {
			must(t, store.Insert(account{ID: "acc_0"}))
			faulty.set(path.Join(overlayPath, entryNameOf(t, "acc_2")), true, false)
			must(t, store.Batch(batch))
			faulty.set("", false, false)
			must(t, store.Put(account{ID: "acc_2", Name: "newer"}))
		})
		store, err := openWith(accountConfig(faulty))
		must(t, err)
		defer func() { must(t, store.Close()) }()
		if got, getErr := store.Get("acc_2"); getErr != nil || got.Name != "newer" {
			t.Fatalf("Get(acc_2) after a reopen = %+v, %v, want the write made after the batch", got, getErr)
		}
	})
}
