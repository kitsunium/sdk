// Package docstore_test — what a store's versions weigh (ADR 0143, the
// amendment for kit's Studio): one contract on the memory, file and SQL
// engines — every count recomputed from Versions and Tombstones after each
// step, so the counter of the file engine and the aggregate of the SQL engine
// are both held to what a reader of the versions sees — and, per engine, where
// the weight comes from and how it fails.
package docstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// weightEngine is one engine's accounts store seen through what the weight
// contract needs: the trash contract's calls, a stamped Put, PutDocument and
// VersionWeight, with the SQL store's context supplied.
type weightEngine struct {
	trashEngine
	putStamped  func(v account, stamp coredocstore.StampValue) error
	putDocument func(key string, versions []coredocstore.VersionValue, trashedAt time.Time) error
	weight      func() (coredocstore.VersionWeightValue, error)
}

// eachWeightEngine runs fn over every engine — memory, file, and the SQL store
// on each dialect — each keeping versions former versions, in a subtest of its
// own.
func eachWeightEngine(t *testing.T, versions int, fn func(t *testing.T, e weightEngine, clk *clock.ManualClock)) {
	t.Helper()
	eachBackend(t, func(t *testing.T, fsys corevfs.FullFS) {
		clk := clock.NewManualClock(epoch)
		store := openVersioned(t, versionedConfig(fsys, versions, clk))
		fn(t, weightEngine{
			trashEngine: fileTrashEngine(t, store), putStamped: store.PutStamped,
			putDocument: store.PutDocument, weight: store.VersionWeight,
		}, clk)
	})
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		clk := clock.NewManualClock(epoch)
		ctx, store := t.Context(), openSQLVersioned(t, dialect, keeping(versions, clk)).store
		fn(t, weightEngine{
			trashEngine: sqlTrashEngine(ctx, store),
			putStamped:  func(v account, stamp coredocstore.StampValue) error { return store.PutStamped(ctx, v, stamp) },
			putDocument: func(key string, versions []coredocstore.VersionValue, trashedAt time.Time) error {
				return store.PutDocument(ctx, key, versions, trashedAt)
			},
			weight: func() (coredocstore.VersionWeightValue, error) { return store.VersionWeight(ctx) },
		}, clk)
	})
}

// recount is what the store's versions weigh as a reader of them counts it:
// every former version of every stored and trashed document, every trashed
// document, and the bytes of their JSON.
func recount(t *testing.T, e weightEngine) coredocstore.VersionWeightValue {
	t.Helper()
	var w coredocstore.VersionWeightValue
	stored, err := e.list()
	must(t, err)
	for _, a := range stored {
		all := engineVersionsOf(t, e.trashEngine, a.ID)
		w.Former += len(all) - 1
		for _, v := range all[1:] {
			w.Bytes += int64(len(v.JSON))
		}
	}
	for _, tomb := range tombstonesOf(t, e.trashEngine) {
		all := engineVersionsOf(t, e.trashEngine, tomb.Key)
		w.Former += len(all) - 1
		w.Trashed++
		for _, v := range all {
			w.Bytes += int64(len(v.JSON))
		}
	}
	return w
}

// requireWeight fails the test unless the engine's weight is want, and is
// what a reader of its versions counts.
func requireWeight(t *testing.T, e weightEngine, want coredocstore.VersionWeightValue, step string) {
	t.Helper()
	got, err := e.weight()
	if err != nil {
		t.Fatalf("%s: VersionWeight() = %v", step, err)
	}
	if got != want {
		t.Fatalf("%s: VersionWeight() = %+v, want %+v", step, got, want)
	}
	if counted := recount(t, e); got != counted {
		t.Fatalf("%s: VersionWeight() = %+v, a reader of the versions counts %+v", step, got, counted)
	}
}

// sizeOf is the length of the JSON both engines keep of a.
func sizeOf(t *testing.T, a account) int64 {
	t.Helper()
	raw, err := json.Marshal(a)
	must(t, err)
	return int64(len(raw))
}

// TestVersionWeightCountsWhatTheStoreKeepsBeyondItsDocuments pins the
// contract on every engine, step by step: a creation weighs nothing, a write
// that changes the document adds its former version, the same JSON and a write
// in place add none, pruning takes what it prunes, the trash weighs its
// document, a restoration gives it back, a purge and a deletion take every
// version, a rewrite weighs what it leaves, and PutDocument what it was given.
func TestVersionWeightCountsWhatTheStoreKeepsBeyondItsDocuments(t *testing.T) {
	t.Parallel()
	eachWeightEngine(t, 2, func(t *testing.T, e weightEngine, clk *clock.ManualClock) {
		v := func(id, name string) account { return account{ID: id, Name: name, Email: id + "@x.dev"} }
		requireWeight(t, e, coredocstore.VersionWeightValue{}, "an empty store")
		must(t, e.insert(v("acc_1", "v1")))
		requireWeight(t, e, coredocstore.VersionWeightValue{}, "a creation")
		clk.Advance(time.Second)
		must(t, e.put(v("acc_1", "v2")))
		requireWeight(t, e, coredocstore.VersionWeightValue{Former: 1, Bytes: sizeOf(t, v("acc_1", "v1"))}, "a change")
		must(t, e.put(v("acc_1", "v2")))
		must(t, e.putStamped(v("acc_1", "v2-in-place"), coredocstore.StampValue{InPlace: true}))
		requireWeight(t, e, coredocstore.VersionWeightValue{Former: 1, Bytes: sizeOf(t, v("acc_1", "v1"))}, "the same JSON, then in place")
		must(t, e.put(v("acc_1", "v3")))
		must(t, e.put(v("acc_1", "v4")))
		two := sizeOf(t, v("acc_1", "v2-in-place")) + sizeOf(t, v("acc_1", "v3"))
		requireWeight(t, e, coredocstore.VersionWeightValue{Former: 2, Bytes: two}, "pruned to two")
		must(t, e.insert(v("acc_2", "w1")))
		must(t, e.put(v("acc_2", "w2")))
		both := two + sizeOf(t, v("acc_2", "w1"))
		requireWeight(t, e, coredocstore.VersionWeightValue{Former: 3, Bytes: both}, "a second document")
		must(t, e.trash("acc_1"))
		requireWeight(t, e, coredocstore.VersionWeightValue{Former: 3, Trashed: 1, Bytes: both + sizeOf(t, v("acc_1", "v4"))}, "a trashing")
		must(t, e.restore("acc_1"))
		requireWeight(t, e, coredocstore.VersionWeightValue{Former: 3, Bytes: both}, "a restoration")
		must(t, e.rewrite("acc_1", func(former []coredocstore.VersionValue) ([]coredocstore.VersionValue, error) {
			former[0].JSON = json.RawMessage(`{}`)
			return former[:1], nil
		}))
		requireWeight(t, e, coredocstore.VersionWeightValue{Former: 2, Bytes: 2 + sizeOf(t, v("acc_2", "w1"))}, "a rewrite")
		must(t, e.trash("acc_1"))
		must(t, e.purge("acc_1"))
		must(t, e.del("acc_2"))
		requireWeight(t, e, coredocstore.VersionWeightValue{}, "a purge and a deletion")
		at := epoch.Add(time.Hour)
		given := []coredocstore.VersionValue{
			{Number: 9, At: at, JSON: json.RawMessage(`{"id":"acc_3","name":"c9"}`)},
			{Number: 4, At: at, Meta: map[string]string{"by": "ada"}, JSON: json.RawMessage(`{"id":"acc_3","name":"c4"}`)},
			{Number: 1, JSON: json.RawMessage(`{"id":"acc_3"}`)},
		}
		must(t, e.putDocument("acc_3", given, at))
		var weighs int64
		for _, g := range given {
			weighs += int64(len(g.JSON))
		}
		requireWeight(t, e, coredocstore.VersionWeightValue{Former: 2, Trashed: 1, Bytes: weighs}, "a trashed document as given")
	})
}

// TestVersionWeightOnAStoreWithoutVersions pins the refusal on both engines:
// a store that keeps no versions answers VersionsNotKept, rather than a
// weight of nothing that would say a history is kept and empty; the file
// engine's Stats says nothing either.
func TestVersionWeightOnAStoreWithoutVersions(t *testing.T) {
	t.Parallel()
	eachBackend(t, func(t *testing.T, fsys corevfs.FullFS) {
		store := openAccounts(t, fsys)
		must(t, store.Put(account{ID: "acc_1", Name: "v1"}))
		must(t, store.Put(account{ID: "acc_1", Name: "v2"}))
		_, err := store.VersionWeight()
		requireCode(t, err, coredocstore.CodeVersionsNotKept, "VersionWeight() without versions")
		if got := store.Stats().Versions; got != (coredocstore.VersionWeightValue{}) {
			t.Fatalf("Stats().Versions without versions = %+v, want zero", got)
		}
	})
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		fx := openSQL(t, dialect)
		fx.engine.resetLog()
		_, err := fx.store.VersionWeight(t.Context())
		requireCode(t, err, coredocstore.CodeVersionsNotKept, "VersionWeight() without versions")
		if got := fx.engine.roles(); len(got) != 0 {
			t.Fatalf("VersionWeight() without versions sent %v, want nothing", got)
		}
	})
}

// TestVersionWeightIsACounterInStats pins the file engine's side: Stats
// reports the same weight as VersionWeight; after Close, Stats still answers
// with the state the store closed in and VersionWeight refuses StoreClosed.
func TestVersionWeightIsACounterInStats(t *testing.T) {
	t.Parallel()
	eachBackend(t, func(t *testing.T, fsys corevfs.FullFS) {
		store := openVersioned(t, versionedConfig(fsys, 3, clock.NewManualClock(epoch)))
		for i := range 5 {
			must(t, store.Put(account{ID: "acc_1", Name: fmt.Sprint("v", i)}))
		}
		must(t, store.Put(account{ID: "acc_2"}))
		must(t, store.Trash("acc_2"))
		w, err := store.VersionWeight()
		must(t, err)
		want := coredocstore.VersionWeightValue{Former: 3, Trashed: 1, Bytes: 3*sizeOf(t, account{ID: "acc_1", Name: "v0"}) + sizeOf(t, account{ID: "acc_2"})}
		if w != want || store.Stats().Versions != want {
			t.Fatalf("VersionWeight() = %+v, Stats().Versions = %+v, want %+v twice", w, store.Stats().Versions, want)
		}
		must(t, store.Close())
		if got := store.Stats().Versions; got != want {
			t.Fatalf("Stats().Versions after Close = %+v, want %+v", got, want)
		}
		_, err = store.VersionWeight()
		requireCode(t, err, coredocstore.CodeStoreClosed, "VersionWeight() after Close")
	})
}

// TestVersionWeightIsRebuiltWhenTheStoreOpens pins the counter of the file
// engine across a reopen: from the versions file after a fold, and from the
// overlay entries a crash left — the same weight either way.
func TestVersionWeightIsRebuiltWhenTheStoreOpens(t *testing.T) {
	t.Parallel()
	fsys := memFS()
	cfg := versionedConfig(fsys, 2, clock.NewManualClock(epoch))
	cfg.FoldAt = -1
	crashed, err := openWith(cfg)
	must(t, err)
	for i := range 4 {
		must(t, crashed.Put(account{ID: "acc_1", Name: fmt.Sprint("v", i)}))
	}
	must(t, crashed.Put(account{ID: "acc_2", Name: "w"}))
	must(t, crashed.Trash("acc_2"))
	want, err := crashed.VersionWeight()
	must(t, err)
	if want.Former != 2 || want.Trashed != 1 {
		t.Fatalf("VersionWeight() = %+v, want two former versions and one trashed document", want)
	}
	// The process dies: the next open replays the entries.
	replayed := openVersioned(t, versionedConfig(fsys, 2, clock.NewManualClock(epoch)))
	if got, err := replayed.VersionWeight(); err != nil || got != want {
		t.Fatalf("VersionWeight() after a crash = %+v, %v, want %+v", got, err, want)
	}
	must(t, replayed.Close())
	folded := openVersioned(t, versionedConfig(fsys, 2, clock.NewManualClock(epoch)))
	if got, err := folded.VersionWeight(); err != nil || got != want {
		t.Fatalf("VersionWeight() after a fold = %+v, %v, want %+v", got, err, want)
	}
}

// TestVersionWeightFollowsConcurrentWriters runs writers, trashers and readers
// of the weight at once on the file engine, under the race detector, and
// requires the counter to match a recount once they are done.
func TestVersionWeightFollowsConcurrentWriters(t *testing.T) {
	t.Parallel()
	eachBackend(t, func(t *testing.T, fsys corevfs.FullFS) {
		store := openVersioned(t, versionedConfig(fsys, 3, clock.NewManualClock(epoch)))
		var wg sync.WaitGroup
		for w := range 8 {
			wg.Go(func() {
				key := fmt.Sprint("acc_", w%4)
				for i := range 40 {
					switch err := store.Put(account{ID: key, Name: fmt.Sprint(w, "-", i)}); {
					case err != nil:
						t.Errorf("Put() = %v", err)
					case i%10 == 9:
						if err := store.Trash(key); err != nil && !errors.Is(err, coredocstore.DocumentNotFound) {
							t.Errorf("Trash() = %v", err)
						}
					}
					if _, err := store.VersionWeight(); err != nil {
						t.Errorf("VersionWeight() = %v", err)
					}
				}
			})
		}
		wg.Wait()
		e := weightEngine{trashEngine: fileTrashEngine(t, store), weight: store.VersionWeight}
		got, err := store.VersionWeight()
		must(t, err)
		requireWeight(t, e, got, "after the writers")
	})
}

// TestSQLVersionWeightIsOneStatement pins the SQL engine's side: one
// aggregate statement and nothing else, run on the transaction the context
// carries — a version the caller made and has not committed is counted —, and
// a failure answered StatementFailed, quoting nothing the driver said.
func TestSQLVersionWeightIsOneStatement(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		fx := openSQLVersioned(t, dialect, keeping(2, clock.NewManualClock(epoch)))
		ctx := t.Context()
		must(t, fx.store.Put(ctx, account{ID: "ada@example.org", Name: "v1"}))
		must(t, fx.store.Put(ctx, account{ID: "ada@example.org", Name: "v2"}))
		fx.engine.resetLog()
		w, err := fx.store.VersionWeight(ctx)
		must(t, err)
		if w.Former != 1 || !slices.Equal(fx.engine.roles(), []string{"versionWeight"}) {
			t.Fatalf("VersionWeight() = %+v sending %v, want one former version in one statement", w, fx.engine.roles())
		}
		err = fx.tm.Transact(ctx, coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
			must(t, fx.store.Put(ctx, account{ID: "ada@example.org", Name: "v3"}))
			if inside, err := fx.store.VersionWeight(ctx); err != nil || inside.Former != 2 {
				t.Fatalf("VersionWeight() in the transaction = %+v, %v, want its own version counted", inside, err)
			}
			return errors.New("roll back")
		})
		if err == nil {
			t.Fatal("the transaction committed")
		}
		if after, err := fx.store.VersionWeight(ctx); err != nil || after != w {
			t.Fatalf("VersionWeight() after the rollback = %+v, %v, want %+v", after, err, w)
		}
		fx.engine.failNext("versionWeight", errors.New("connection reset reading ada@example.org"))
		_, err = fx.store.VersionWeight(ctx)
		requireCode(t, err, coredocstore.CodeStatementFailed, "a failed VersionWeight()")
		quotesNothing(t, err, "ada@example.org")
	})
}

// The ports, as a caller standing a double in holds them.
var (
	_ coredocstore.VersionWeigher        = (*docstore.Store[account])(nil)
	_ coredocstore.VersionWeigherContext = (*docstore.SQLStore[account])(nil)
)
