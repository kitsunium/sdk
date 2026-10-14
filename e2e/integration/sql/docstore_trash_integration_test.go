//go:build integration

// Package sql_test — a document's trash over SQL on each real engine (ADR 0143,
// amended for ADR 0178): the version row numbered 0 the engine parses, writes
// and reads back, its unique keys freed, the numbering continued by a write
// under its key, a restoration and a purge, and a trash undone by the caller's
// rollback.
package sql_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
)

// TestTheTrashOnEveryEngine pins the trash on each engine: a trashed document
// leaves every read and keeps its versions, read back with the instant it was
// trashed; another document may take its unique key, which Restore then
// refuses; a creation under its key continues its numbering; Purge takes the
// versions; and a Trash inside the caller's rolled-back transaction leaves
// nothing.
func TestTheTrashOnEveryEngine(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		clk := clock.NewManualClock(versionsEpoch)
		fx := openVersioned(t, e, 3, func(c *docstore.SQLConfig[member]) { c.Clock = clk })
		ctx, store := t.Context(), fx.store
		must(t, store.Insert(ctx, member{ID: "m1", Name: "v1", Email: "ada@x.dev"}))
		clk.Advance(time.Minute)
		must(t, store.Put(ctx, member{ID: "m1", Name: "v2", Email: "ada@x.dev"}))
		clk.Advance(time.Hour)
		must(t, store.Trash(ctx, "m1"))

		_, err := store.Lookup(ctx, "email", "ada@x.dev")
		requireCode(t, err, docstore.CodeDocumentNotFound, "Lookup of a trashed document")
		if got := versionNumbers(t, store, "m1"); !slices.Equal(got, []uint64{2, 1}) {
			t.Fatalf("a trashed document's versions are %v", got)
		}
		tombs, err := store.Tombstones(ctx)
		must(t, err)
		if len(tombs) != 1 || tombs[0].Key != "m1" || !tombs[0].At.Equal(versionsEpoch.Add(time.Minute+time.Hour)) {
			t.Fatalf("Tombstones() = %+v", tombs)
		}
		if rows := versionRows(t, e, fx, "m1"); rows != 3 {
			t.Fatalf("the versions table holds %d rows of the trashed document, want its two versions and the trash's", rows)
		}

		must(t, store.Insert(ctx, member{ID: "m2", Email: "ada@x.dev"}))
		requireCode(t, store.Restore(ctx, "m1"), docstore.CodeUniqueKeyTaken, "Restore over a unique key taken meanwhile")
		must(t, store.Delete(ctx, "m2"))
		must(t, store.Restore(ctx, "m1"))
		if got, getErr := store.Lookup(ctx, "email", "ada@x.dev"); getErr != nil || got.Name != "v2" {
			t.Fatalf("Lookup() after Restore = %+v, %v", got, getErr)
		}

		must(t, store.Trash(ctx, "m1"))
		must(t, store.Put(ctx, member{ID: "m1", Name: "v3"}))
		if got := versionNumbers(t, store, "m1"); !slices.Equal(got, []uint64{3, 2, 1}) {
			t.Fatalf("a write under a trashed key left versions %v", got)
		}

		must(t, store.Trash(ctx, "m1"))
		must(t, store.Purge(ctx, "m1"))
		if rows := versionRows(t, e, fx, "m1"); rows != 0 {
			t.Fatalf("a purge left %d version rows", rows)
		}
		requireCode(t, store.Restore(ctx, "m1"), docstore.CodeDocumentNotTrashed, "Restore of a purged document")

		must(t, store.Insert(ctx, member{ID: "m3", Name: "kept"}))
		errRollback := errors.New("the caller changed its mind")
		err = sql.Transact(ctx, fx.tm, func(ctx context.Context, _ sql.Executor) error {
			must(t, store.Trash(ctx, "m3"))
			return errRollback
		})
		if !errors.Is(err, errRollback) {
			t.Fatalf("Transact() = %v", err)
		}
		if got, getErr := store.Get(ctx, "m3"); getErr != nil || got.Name != "kept" {
			t.Fatalf("a rolled-back Trash left Get() = %+v, %v", got, getErr)
		}
		if tombs, err = store.Tombstones(ctx); err != nil || len(tombs) != 0 {
			t.Fatalf("a rolled-back Trash left Tombstones() = %+v, %v", tombs, err)
		}
	})
}
