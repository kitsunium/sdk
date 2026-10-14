// Package docstore_test — the SQL store's trash, on each dialect's statements
// over the fake engine: a row numbered 0 in the versions table, written and
// emptied by statements of one transaction, rolled back with it.
package docstore_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// TestSQLATrashedDocumentIsARowNumberedZero pins what the trash leaves in the
// tables, on every dialect: no document row and no index row, the version rows
// as they were, and one more row numbered 0 holding the document and the
// instant it was trashed — readable by anyone who reads the table.
func TestSQLATrashedDocumentIsARowNumberedZero(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		ctx, clk := t.Context(), clock.NewManualClock(epoch)
		fx := openSQLVersioned(t, dialect, keeping(3, clk))
		must(t, fx.store.Put(ctx, account{ID: "acc_1", Name: "v1", Email: "ada@x.dev"}))
		must(t, fx.store.Put(ctx, account{ID: "acc_1", Name: "v2", Email: "ada@x.dev"}))
		before := fx.engine.snapshot()
		clk.Advance(time.Hour)
		must(t, fx.store.Trash(ctx, "acc_1"))

		after := fx.engine.snapshot()
		if _, found := after.docs["acc_1"]; found {
			t.Fatal("the trashed document still has its row")
		}
		for k := range after.ix {
			if k.doc == "acc_1" {
				t.Fatalf("the trashed document still has the index row %+v", k)
			}
		}
		for _, num := range []int64{1, 2} {
			k := vsKey{doc: "acc_1", num: num}
			if !bytes.Equal(after.vs[k].doc, before.vs[k].doc) || after.vs[k].at != before.vs[k].at {
				t.Fatalf("the trash changed version row %d", num)
			}
		}
		tomb, found := after.vs[vsKey{doc: "acc_1", num: 0}]
		if !found || !bytes.Equal(tomb.doc, before.docs["acc_1"].doc) || tomb.at != epoch.Add(time.Hour).Unix() || tomb.meta != nil {
			t.Fatalf("the trash row is %+v, %v, want the document trashed at %v", tomb, found, epoch.Add(time.Hour))
		}
	})
}

// TestSQLTheTrashIsOneTransaction pins the trash's atomicity on every dialect:
// a statement of Trash, Restore or Purge that fails leaves the tables as they
// were, and inside the caller's transaction a rollback undoes a Trash — the
// document is back — and OnDelete is never told.
func TestSQLTheTrashIsOneTransaction(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		ctx, fx := t.Context(), openSQLVersioned(t, dialect, keeping(3, nil))
		must(t, fx.store.Put(ctx, account{ID: "acc_1", Name: "v1", Email: "ada@x.dev"}))
		for _, role := range []string{"lockDoc", "deleteDoc", "deleteIndex", "insertVersions"} {
			fx.engine.failNext(role, errors.New("the statement was cancelled"))
			requireCode(t, fx.store.Trash(ctx, "acc_1"), coredocstore.CodeStatementFailed, role)
			if a, err := fx.store.Lookup(ctx, "email", "ada@x.dev"); err != nil || a.Name != "v1" {
				t.Fatalf("%s failed and Lookup() = %+v, %v", role, a, err)
			}
		}
		var told []string
		fx.store.OnDelete(func(key string) { told = append(told, key) })
		errRollback := errors.New("the caller changed its mind")
		err := fx.tm.Transact(ctx, coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
			must(t, fx.store.Trash(ctx, "acc_1"))
			return errRollback
		})
		if !errors.Is(err, errRollback) {
			t.Fatalf("Transact() = %v", err)
		}
		if a, getErr := fx.store.Get(ctx, "acc_1"); getErr != nil || a.Name != "v1" || len(told) != 0 {
			t.Fatalf("a rolled-back Trash left Get() = %+v, %v and told %v", a, getErr, told)
		}

		must(t, fx.store.Trash(ctx, "acc_1"))
		for _, role := range []string{"firstVersion", "insert", "insertIndexRows", "dropTombstone"} {
			fx.engine.failNext(role, errors.New("the statement was cancelled"))
			requireCode(t, fx.store.Restore(ctx, "acc_1"), coredocstore.CodeStatementFailed, role)
			if tombs, tombErr := fx.store.Tombstones(ctx); tombErr != nil || !slices.Equal(trashedKeys(tombs), []string{"acc_1"}) {
				t.Fatalf("%s failed and the trash is %+v, %v", role, tombs, tombErr)
			}
		}
		fx.engine.failNext("dropVersions", errors.New("the statement was cancelled"))
		requireCode(t, fx.store.Purge(ctx, "acc_1"), coredocstore.CodeStatementFailed, "dropVersions")
		if got := numbers(sqlVersionsOf(t, fx.store, "acc_1")); !slices.Equal(got, []uint64{1}) {
			t.Fatalf("a failed Purge left versions %v", got)
		}
		must(t, fx.store.Restore(ctx, "acc_1"))
	})
}
