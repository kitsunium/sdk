//go:build integration

// Package sql_test — a document store's change feed on each real engine
// (kitsunium/sdk#831): the stream's SQL engine on the store's own transactor,
// every write's change record appended inside the write's transaction, so a
// write in a transaction that rolls back leaves no record, and a committed one
// exactly one, with the version it left, which a consumer group reads.
package sql_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
	svcstream "github.com/kitsunium/sdk/internal/service/data/stream"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
)

// errRolledBack is the failure a case's unit of work returns to roll it back.
var errRolledBack = errors.New("the unit of work failed")

// TestTheChangeFeedIsTheWritesTransactionOnEveryEngine runs the feed on each
// engine: nothing for a rolled-back write, one record per committed write,
// numbered by the version it left, read by a consumer group.
func TestTheChangeFeedIsTheWritesTransactionOnEveryEngine(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		changesTable := uniqueName("changes_")
		migration, err := svcstream.SQLMigration(e.dialect, changesTable, 1)
		must(t, err)
		runner, err := sql.NewMigrator(sql.Config{DB: e.db, Dialect: e.dialect, Pool: sql.PoolConfig{MaxOpen: 16}},
			sql.MigrateConfig{Migrations: []sql.Migration{migration}, VersionTable: uniqueName("versions_")})
		must(t, err)
		must(t, runner.Up(t.Context()))
		var feed corestream.Stream
		fx := openVersioned(t, e, 2, func(cfg *docstore.SQLConfig[member]) {
			feed, err = svcstream.NewSQL(svcstream.SQLConfig{
				Transactor: cfg.Transactor, Dialect: e.dialect, Table: changesTable,
				Policy: corestream.PolicyValue{Partitions: 2}, Clock: clock.NewManualClock(streamEpoch),
			})
			must(t, err)
			cfg.Changes, cfg.ChangesWithDocument = feed, true
		})
		ctx := t.Context()

		err = fx.tm.Transact(ctx, sql.TxOptions{}, func(txCtx context.Context, _ sql.Executor) error {
			if putErr := fx.store.Put(txCtx, member{ID: "rolled", Email: "r@x"}); putErr != nil {
				return putErr
			}
			return errRolledBack
		})
		if !errors.Is(err, errRolledBack) {
			t.Fatalf("Transact() = %v, want the unit of work's failure", err)
		}
		if n := recordsIn(t, feed); n != 0 {
			t.Fatalf("a rolled-back write left %d change records, want none", n)
		}

		must(t, fx.tm.Transact(ctx, sql.TxOptions{}, func(txCtx context.Context, _ sql.Executor) error {
			return fx.store.Put(txCtx, member{ID: "m1", Email: "a@x"})
		}))
		must(t, fx.store.Put(ctx, member{ID: "m1", Email: "a@x", Name: "Ada"}))
		if n := recordsIn(t, feed); n != 2 {
			t.Fatalf("two committed writes left %d change records, want 2", n)
		}

		got := readGroup(t, feed, 2)
		if got[0].Key != "m1" || got[0].Op != docstore.ChangePut || got[0].Version != 1 || got[1].Version != 2 {
			t.Fatalf("the group read %+v, want m1's put at versions 1 then 2", got)
		}
		var doc member
		if err := json.Unmarshal(got[1].Document, &doc); err != nil || doc.Name != "Ada" {
			t.Fatalf("the second record's document = %s (%v), want Ada's", got[1].Document, err)
		}
	})
}

// recordsIn counts the records every partition of s holds.
func recordsIn(t *testing.T, s corestream.Stream) int {
	t.Helper()
	n := 0
	for p := range s.Partitions() {
		bounds, err := s.Bounds(t.Context(), p)
		must(t, err)
		n += int(bounds.Next - bounds.First)
	}
	return n
}

// readGroup subscribes a consumer group to s until it read want changes.
func readGroup(t *testing.T, s corestream.Stream, want int) []docstore.Change {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var read []docstore.Change
	err := svcstream.Subscribe(ctx, s, svcstream.SubscriberConfig{
		Group: "projection", HandlerIsIdempotent: true, PollInterval: 5 * time.Millisecond,
		Handler: func(_ context.Context, entries []corestream.EntryValue) error {
			for _, entry := range entries {
				var change docstore.Change
				if err := json.Unmarshal(entry.Record.Payload, &change); err != nil {
					return err
				}
				read = append(read, change)
			}
			if len(read) >= want {
				cancel()
			}
			return nil
		},
	})
	must(t, err)
	if len(read) != want {
		t.Fatalf("the group read %d changes, want %d", len(read), want)
	}
	return read
}
