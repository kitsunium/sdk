//go:build integration

// Package sql_test — what a store's versions weigh over SQL on each real
// engine (ADR 0143, the amendment for kit's Studio): the one aggregate the
// engine parses and answers, counting bytes and never characters, the same
// weight a reader of the versions counts, and a caller's uncommitted version
// counted inside its own transaction only.
package sql_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
)

// weighedByReading is what the store's versions weigh as a reader of them
// counts it: every former version of keys, the trashed document of trashed,
// and the bytes of their JSON.
func weighedByReading(t *testing.T, store *docstore.SQLStore[member], keys, trashed []string) docstore.VersionWeight {
	t.Helper()
	var w docstore.VersionWeight
	for _, key := range append(append([]string{}, keys...), trashed...) {
		all, err := store.Versions(t.Context(), key)
		must(t, err)
		w.Former += len(all) - 1
		for _, v := range all[1:] {
			w.Bytes += int64(len(v.JSON))
		}
	}
	for _, key := range trashed {
		all, err := store.Versions(t.Context(), key)
		must(t, err)
		w.Trashed++
		w.Bytes += int64(len(all[0].JSON))
	}
	return w
}

// TestVersionWeightOnEveryEngine pins VersionWeight on each engine: two
// documents with former versions — one of them named with characters UTF-8
// spells in two bytes, which LENGTH must count as two —, pruned to the number
// kept, one trashed; the weight equals what a reader of the versions counts;
// a version a caller's transaction made is counted inside it and gone after
// its rollback.
func TestVersionWeightOnEveryEngine(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		clk := clock.NewManualClock(versionsEpoch)
		fx := openVersioned(t, e, 2, func(c *docstore.SQLConfig[member]) { c.Clock = clk })
		ctx, store := t.Context(), fx.store
		empty, err := store.VersionWeight(ctx)
		must(t, err)
		if empty != (docstore.VersionWeight{}) {
			t.Fatalf("VersionWeight() of an empty store = %+v", empty)
		}
		for _, name := range []string{"Élodie", "Éloïse", "Émile", "Énora"} {
			clk.Advance(time.Minute)
			must(t, store.Put(ctx, member{ID: "m1", Name: name}))
		}
		must(t, store.Put(ctx, member{ID: "m2", Name: "v1"}))
		must(t, store.Put(ctx, member{ID: "m2", Name: "v2"}))
		must(t, store.Put(ctx, member{ID: "m3", Name: "trashed"}))
		must(t, store.Trash(ctx, "m3"))
		w, err := store.VersionWeight(ctx)
		must(t, err)
		want := weighedByReading(t, store, []string{"m1", "m2"}, []string{"m3"})
		if w != want || w.Former != 3 || w.Trashed != 1 {
			t.Fatalf("VersionWeight() = %+v, a reader of the versions counts %+v; want three former versions and one trashed document", w, want)
		}
		errRollback := errors.New("the caller changed its mind")
		err = sql.Transact(ctx, fx.tm, func(ctx context.Context, _ sql.Executor) error {
			must(t, store.Put(ctx, member{ID: "m2", Name: "v3"}))
			inside, insideErr := store.VersionWeight(ctx)
			must(t, insideErr)
			if inside.Former != 4 {
				t.Fatalf("VersionWeight() inside the transaction = %+v, want its own version counted", inside)
			}
			return errRollback
		})
		if !errors.Is(err, errRollback) {
			t.Fatalf("Transact() = %v", err)
		}
		if after, afterErr := store.VersionWeight(ctx); afterErr != nil || after != w {
			t.Fatalf("VersionWeight() after the rollback = %+v, %v, want %+v", after, afterErr, w)
		}
	})
}
