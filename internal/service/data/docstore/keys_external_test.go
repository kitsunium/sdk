// Package docstore_test — Keys, the keys alone (ADR 0178): one contract on
// the memory, file and SQL engines — every key in byte order, a resume after
// a key, an early stop, a deleted or trashed document left out — and, per
// engine, what a listing reads and how it fails.
package docstore_test

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"testing"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/service/data/docstore"
)

// keysFixture is one engine's accounts store, seen through what the keys
// contract needs: a write, a deletion, and Keys.
type keysFixture struct {
	put    func(account) error
	delete func(key string) error
	keys   func(after string) iter.Seq2[string, error]
}

// keysEngines opens a fresh accounts store on every engine: memory, the file
// engine over a filesystem, and the SQL engine on each dialect.
func keysEngines(t *testing.T) map[string]keysFixture {
	t.Helper()
	ctx := t.Context()
	engines := map[string]keysFixture{}
	for name, fsys := range map[string]bool{"memory": false, "file": true} {
		cfg := accountConfig(nil)
		if fsys {
			cfg = accountConfig(memFS())
		}
		store, err := openWith(cfg)
		if err != nil {
			t.Fatalf("Open(%s) = %v", name, err)
		}
		t.Cleanup(func() { _ = store.Close() })
		engines[name] = keysFixture{put: store.Put, delete: store.Delete, keys: store.Keys}
	}
	for _, dialect := range sqlDialects {
		store := openSQL(t, dialect).store
		engines["sql/"+dialect.String()] = keysFixture{
			put:    func(a account) error { return store.Put(ctx, a) },
			delete: func(key string) error { return store.Delete(ctx, key) },
			keys:   func(after string) iter.Seq2[string, error] { return store.Keys(ctx, after) },
		}
	}
	return engines
}

// collect ranges over seq to the end, failing the test on a yielded error.
func collect(t *testing.T, seq iter.Seq2[string, error]) []string {
	t.Helper()
	var keys []string
	for key, err := range seq {
		if err != nil {
			t.Fatalf("Keys yielded %v", err)
		}
		keys = append(keys, key)
	}
	return keys
}

// TestKeysListsEveryKeyInByteOrderOnEveryEngine pins the contract on each
// engine: every key — more than one SQL page of them, and keys only byte
// order tells apart — sorted as Go compares strings; a resume after a key,
// stored or not; an early stop; a deleted document never listed; and an empty
// store listing nothing.
func TestKeysListsEveryKeyInByteOrderOnEveryEngine(t *testing.T) {
	t.Parallel()
	for name, fx := range keysEngines(t) {
		t.Run(name, func(t *testing.T) {
			if got := collect(t, fx.keys("")); len(got) != 0 {
				t.Fatalf("Keys() on an empty store = %q", got)
			}
			stored := []string{"b", "a", "A", "a ", "é", "e"}
			for i := range 600 {
				stored = append(stored, fmt.Sprintf("acc_%04d", i))
			}
			for _, key := range stored {
				must(t, fx.put(account{ID: key, Name: "a name that weighs what a record weighs"}))
			}
			must(t, fx.delete("acc_0300"))
			want := slices.Sorted(slices.Values(slices.DeleteFunc(slices.Clone(stored), func(k string) bool { return k == "acc_0300" })))
			if got := collect(t, fx.keys("")); !slices.Equal(got, want) {
				t.Fatalf("Keys(\"\") = %d keys, want %d in byte order; first %q", len(got), len(want), got[:min(len(got), 8)])
			}
			if got := collect(t, fx.keys("acc_0299")); !slices.Equal(got, want[slices.Index(want, "acc_0301"):]) {
				t.Fatalf("Keys(after a stored key) resumed at %q", got[:min(len(got), 3)])
			}
			if got := collect(t, fx.keys("acc_0300")); got[0] != "acc_0301" {
				t.Fatalf("Keys(after a deleted key) resumed at %q, want acc_0301", got[0])
			}
			if got := collect(t, fx.keys("é")); len(got) != 0 {
				t.Fatalf("Keys(after the last key) = %q", got)
			}
			var first []string
			for key, err := range fx.keys("") {
				must(t, err)
				first = append(first, key)
				if len(first) == 3 {
					break
				}
			}
			if !slices.Equal(first, want[:3]) {
				t.Fatalf("an early stop saw %q, want %q", first, want[:3])
			}
		})
	}
}

// TestKeysOnAClosedStoreYieldsStoreClosedOnce pins the file engine's one
// failure: StoreClosed, yielded once with an empty key, and nothing after it.
func TestKeysOnAClosedStoreYieldsStoreClosedOnce(t *testing.T) {
	t.Parallel()
	store := openAccounts(t, memFS())
	must(t, store.Put(account{ID: "acc_1"}))
	seq := store.Keys("")
	must(t, store.Close())
	yields := 0
	for key, err := range seq {
		yields++
		if key != "" {
			t.Fatalf("a closed store yielded the key %q", key)
		}
		requireCode(t, err, coredocstore.CodeStoreClosed, "Keys on a closed store")
	}
	if yields != 1 {
		t.Fatalf("a closed store yielded %d times, want once", yields)
	}
}

// TestKeysReadsTheKeysWhenTheRangeStarts pins the file engine's snapshot:
// nothing is read when Keys is called, every key stored when the range starts
// is listed, and a write made during the range is not.
func TestKeysReadsTheKeysWhenTheRangeStarts(t *testing.T) {
	t.Parallel()
	store := openAccounts(t, nil)
	seq := store.Keys("")
	must(t, store.Put(account{ID: "acc_1"}))
	var got []string
	for key, err := range seq {
		must(t, err)
		got = append(got, key)
		must(t, store.Put(account{ID: "acc_0"}))
	}
	if !slices.Equal(got, []string{"acc_1"}) {
		t.Fatalf("Keys() = %q, want the keys stored when the range started", got)
	}
}

// TestSQLKeysReadOnlyKeysInPages pins what the SQL engine sends: one keysAfter
// statement per page of 256, the last one short, and no statement that reads
// a document.
func TestSQLKeysReadOnlyKeysInPages(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		fx := openSQL(t, dialect)
		ctx := t.Context()
		for i := range 600 {
			must(t, fx.store.Put(ctx, account{ID: fmt.Sprintf("acc_%04d", i)}))
		}
		fx.engine.resetLog()
		if got := collect(t, fx.store.Keys(ctx, "")); len(got) != 600 {
			t.Fatalf("Keys() = %d keys, want 600", len(got))
		}
		if roles := fx.engine.roles(); !slices.Equal(roles, []string{"keysAfter", "keysAfter", "keysAfter"}) {
			t.Fatalf("Keys() sent %q, want three pages of keys and nothing that reads a document", roles)
		}
	})
}

// TestSQLKeysFailOnceAndQuoteNoKey pins a failed page: StatementFailed,
// yielded once with an empty key after the keys already read, quoting neither
// the key it resumed after nor the driver's text.
func TestSQLKeysFailOnceAndQuoteNoKey(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		fx := openSQL(t, dialect)
		ctx := t.Context()
		for i := range 300 {
			must(t, fx.store.Put(ctx, account{ID: fmt.Sprintf("ada%04d@example.org", i)}))
		}
		seen, failures := 0, 0
		for key, err := range fx.store.Keys(ctx, "") {
			if err == nil {
				seen++
				if seen == 256 {
					fx.engine.failNext("keysAfter", errors.New("connection reset after ada0255@example.org"))
				}
				continue
			}
			failures++
			if key != "" {
				t.Fatalf("a failure yielded the key %q", key)
			}
			requireCode(t, err, coredocstore.CodeStatementFailed, "a failed page")
			quotesNothing(t, err, "ada0255@example.org")
		}
		if seen != 256 || failures != 1 {
			t.Fatalf("Keys() yielded %d keys and %d failures, want 256 then one", seen, failures)
		}
	})
}

// TestSQLKeysRunOnTheContextsTransaction pins where a page runs: inside a
// caller's transaction, a key it wrote and has not committed is listed.
func TestSQLKeysRunOnTheContextsTransaction(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		fx := openSQL(t, dialect)
		must(t, fx.store.Put(t.Context(), account{ID: "acc_1"}))
		err := fx.tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
			must(t, fx.store.Put(ctx, account{ID: "acc_2"}))
			if got := collect(t, fx.store.Keys(ctx, "")); !slices.Equal(got, []string{"acc_1", "acc_2"}) {
				t.Fatalf("Keys() in the transaction = %q, want its own write too", got)
			}
			return nil
		})
		must(t, err)
	})
}

// TestKeysNeverListATrashedDocument pins Keys beside the trash on every
// engine: a trashed document is not listed — Keys lists what Get finds —, a
// restored one is again, and a purged one never comes back.
func TestKeysNeverListATrashedDocument(t *testing.T) {
	t.Parallel()
	check := func(t *testing.T, keys func() []string, trash, restore, purge func(string) error) {
		t.Helper()
		must(t, trash("acc_2"))
		must(t, trash("acc_3"))
		if got := keys(); !slices.Equal(got, []string{"acc_1"}) {
			t.Fatalf("Keys() with two documents in the trash = %q, want [acc_1]", got)
		}
		must(t, restore("acc_2"))
		must(t, purge("acc_3"))
		if got := keys(); !slices.Equal(got, []string{"acc_1", "acc_2"}) {
			t.Fatalf("Keys() after a restore and a purge = %q, want [acc_1 acc_2]", got)
		}
	}
	eachBackend(t, func(t *testing.T, fsys corevfs.FullFS) {
		store := openVersioned(t, versionedConfig(fsys, 2, clock.NewManualClock(epoch)))
		for _, id := range []string{"acc_1", "acc_2", "acc_3"} {
			must(t, store.Put(account{ID: id}))
		}
		check(t, func() []string { return collect(t, store.Keys("")) }, store.Trash, store.Restore, store.Purge)
	})
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		ctx, store := t.Context(), openSQLVersioned(t, dialect, keeping(2, clock.NewManualClock(epoch))).store
		for _, id := range []string{"acc_1", "acc_2", "acc_3"} {
			must(t, store.Put(ctx, account{ID: id}))
		}
		check(t, func() []string { return collect(t, store.Keys(ctx, "")) },
			func(k string) error { return store.Trash(ctx, k) },
			func(k string) error { return store.Restore(ctx, k) },
			func(k string) error { return store.Purge(ctx, k) })
	})
}

// The ports, as a caller standing a double in holds them.
var (
	_ coredocstore.KeyLister        = (*docstore.Store[account])(nil)
	_ coredocstore.KeyListerContext = (*docstore.SQLStore[account])(nil)
)
