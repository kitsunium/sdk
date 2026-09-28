// Package docstore_test — Reindex: the SQL store's index rows rebuilt from its
// documents once the declarations that filed them changed.
package docstore_test

import (
	"fmt"
	"testing"

	coresql "github.com/kitsunium/sdk/internal/core/sql"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/docstore"
)

// reopen opens a second store over fx's engine and transactor, with other
// index declarations — what a new release of a program does to its tables.
func reopen(t *testing.T, fx *sqlFixture, dialect coresql.Dialect, indexes ...docstore.IndexSpec[account]) *docstore.SQLStore[account] {
	t.Helper()
	store, err := docstore.OpenSQL(docstore.SQLConfig[account]{
		Key: accountKey, Transactor: fx.tm, Dialect: dialect, Table: accountsTable,
	}, indexes...)
	must(t, err)
	return store
}

// TestSQLReindexFilesWhatANewDeclarationMissed pins the case Reindex exists
// for: an index declared over documents already stored finds none of them
// until the rebuild, and every one after it — across several pages — while a
// removed index's rows go.
func TestSQLReindexFilesWhatANewDeclarationMissed(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		ctx := t.Context()
		fx := openSQL(t, dialect, docstore.Index("old", func(a account) []string { return []string{"x"} }))
		const stored int = 600
		for i := range stored {
			must(t, fx.store.Put(ctx, account{ID: fmt.Sprintf("acc_%04d", i), Teams: []string{"all"}}))
		}
		store := reopen(t, fx, dialect, accountIndexes()...)
		if before, err := store.Find(ctx, "team", "all"); err != nil || len(before) != 0 {
			t.Fatalf("Find() before the rebuild = %d documents, %v — the rows did not exist yet", len(before), err)
		}
		must(t, store.Reindex(ctx))
		after, err := store.Find(ctx, "team", "all")
		if err != nil || len(after) != stored {
			t.Fatalf("Find() after the rebuild = %d documents, %v, want %d", len(after), err, stored)
		}
		for k := range fx.engine.snapshot().ix {
			if k.name == "old" {
				t.Fatal("a removed index kept its rows through the rebuild")
			}
		}
		must(t, store.Insert(ctx, account{ID: "new", Email: "new@x.dev"}))
		requireCode(t, store.Insert(ctx, account{ID: "newer", Email: "new@x.dev"}),
			docstore.CodeUniqueKeyTaken, "the rebuilt unique index constraining a later write")
	})
}

// TestSQLReindexRefusesDocumentsThatBreakAUniqueIndex pins the refusal: an
// index made unique over documents that share a key is INDEX_BROKEN, naming
// the index and never the key, and nothing changes — the rows the old
// declarations filed are still there.
func TestSQLReindexRefusesDocumentsThatBreakAUniqueIndex(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		ctx := t.Context()
		fx := openSQL(t, dialect, docstore.Index("email", func(a account) []string { return []string{a.Email} }))
		must(t, fx.store.Put(ctx, account{ID: "acc_1", Email: "same@x.dev"}))
		must(t, fx.store.Put(ctx, account{ID: "acc_2", Email: "same@x.dev"}))
		before := fx.engine.snapshot().ix
		store := reopen(t, fx, dialect, docstore.Unique("email", func(a account) string { return a.Email }))
		err := store.Reindex(ctx)
		requireCode(t, err, docstore.CodeIndexBroken, "two documents sharing a key made unique")
		quotesNothing(t, err, "same@x.dev")
		if index := fieldValue(errs.FieldsOf(err), "index"); index != "email" {
			t.Fatalf("the refusal names index %q, want email", index)
		}
		after := fx.engine.snapshot().ix
		if len(after) != len(before) {
			t.Fatalf("a refused rebuild changed the rows: %d before, %d after", len(before), len(after))
		}
	})
}

// TestSQLReindexRefusesAKeyFunctionThatPanics pins the rebuild's second
// refusal, the file store's own: stored data an index cannot compute keys for
// is data the index cannot hold.
func TestSQLReindexRefusesAKeyFunctionThatPanics(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		ctx := t.Context()
		fx := openSQL(t, dialect)
		must(t, fx.store.Put(ctx, account{ID: "acc_1"}))
		store := reopen(t, fx, dialect, docstore.Index("broken", func(account) []string { panic("no keys for this one") }))
		err := store.Reindex(ctx)
		requireCode(t, err, docstore.CodeIndexBroken, "a key function that panics")
		if problem := fieldValue(errs.FieldsOf(err), "problem"); problem != "a key function panicked" {
			t.Fatalf("the refusal says %q", problem)
		}
	})
}
