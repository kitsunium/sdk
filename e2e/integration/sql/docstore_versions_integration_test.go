//go:build integration

// Package sql_test — a document's versions over SQL on each real engine
// (ADR 0143): the versions table the migration creates, the claim a Put makes
// before replacing a document, the pruning in the write's own transaction, a
// write that cannot keep its versions not writing its document, a hold asked
// inside the transaction, the erasure's rewrite, and writers racing on one
// document.
package sql_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/docstore"
	"github.com/kitsunium/sdk/pkg/v1/sql"
)

// versionsEpoch is where every versioned case's clock starts.
var versionsEpoch = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

// migrateVersions creates the versions table of a store named table on e,
// through the SDK's own Migrator under a version table of the case's own.
func migrateVersions(tb testing.TB, e *engine, table string) {
	tb.Helper()
	migration, err := docstore.SQLVersionsMigration(e.dialect, table, 2)
	must(tb, err)
	runner, err := sql.NewMigrator(sql.Config{DB: e.db, Dialect: e.dialect, Pool: sql.PoolConfig{MaxOpen: 16}},
		sql.MigrateConfig{Migrations: []sql.Migration{migration}, VersionTable: uniqueName("versions_")})
	must(tb, err)
	must(tb, runner.Up(tb.Context()))
}

// openVersioned migrates the two tables and the versions table of a members
// store on e, and opens it keeping versions former versions.
func openVersioned(tb testing.TB, e *engine, versions int, configure ...func(*docstore.SQLConfig[member])) *storeFixture {
	tb.Helper()
	table := uniqueName("versioned__")
	migrateStore(tb, e, table)
	migrateVersions(tb, e, table)
	tm := transactor(tb, e)
	cfg := docstore.SQLConfig[member]{
		Key: func(m member) string { return m.ID }, Transactor: tm, Dialect: e.dialect, Table: table, Versions: versions,
	}
	for _, c := range configure {
		c(&cfg)
	}
	store, err := docstore.OpenSQL(cfg, memberIndexes()...)
	must(tb, err)
	return &storeFixture{tm: tm, store: store, table: table}
}

// versionNumbers reads key's versions and returns their numbers, newest first.
func versionNumbers(t *testing.T, store *docstore.SQLStore[member], key string) []uint64 {
	t.Helper()
	all, err := store.Versions(t.Context(), key)
	must(t, err)
	out := make([]uint64, len(all))
	for i, v := range all {
		out[i] = v.Number
	}
	return out
}

// versionRows counts the rows of the versions table of fx under key, as the
// table holds them.
func versionRows(t *testing.T, e *engine, fx *storeFixture, key string) int {
	t.Helper()
	var n int
	ex, _ := fx.tm.(sql.Joiner).Join(t.Context())
	query := "SELECT COUNT(*) FROM " + fx.table + "___vs WHERE doc_key = ?"
	if e.dialect == sql.DialectPostgres {
		query = strings.Replace(query, "?", "$1", 1)
	}
	must(t, ex.QueryRowContext(t.Context(), query, []byte(key)).Scan(&n))
	return n
}

// TestVersionsOnEveryEngine pins the versions on each engine: numbered from 1,
// stamped with the store's clock and the caller's metadata, read back newest
// first with the document itself, pruned by the write that makes a newer one
// — the table never holding more rows than the store keeps — and taken by a
// deletion.
func TestVersionsOnEveryEngine(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		clk := clock.NewManualClock(versionsEpoch)
		fx := openVersioned(t, e, 2, func(c *docstore.SQLConfig[member]) { c.Clock = clk })
		ctx, store := t.Context(), fx.store
		must(t, store.InsertStamped(ctx, member{ID: "m1", Name: "v1"}, docstore.Stamp{Meta: map[string]string{"by": "ada"}}))
		for i, name := range []string{"v2", "v3", "v4", "v5"} {
			clk.Advance(time.Minute)
			must(t, store.PutStamped(ctx, member{ID: "m1", Name: name}, docstore.Stamp{Meta: map[string]string{"by": name}}))
			if rows, want := versionRows(t, e, fx, "m1"), min(i+2, 3); rows != want {
				t.Fatalf("after write %d the versions table holds %d rows, want %d: pruned in the write", i+2, rows, want)
			}
		}
		all, err := store.Versions(ctx, "m1")
		must(t, err)
		if len(all) != 3 || all[0].Number != 5 || all[1].Number != 4 || all[2].Number != 3 {
			t.Fatalf("Versions() = %+v, want 5, 4 and 3", all)
		}
		if !all[0].At.Equal(versionsEpoch.Add(4*time.Minute)) || all[1].Meta["by"] != "v4" || !strings.Contains(string(all[2].JSON), `"v3"`) {
			t.Fatalf("Versions() = %+v", all)
		}
		var current member
		must(t, json.Unmarshal(all[0].JSON, &current))
		if current.Name != "v5" {
			t.Fatalf("the current version holds %+v", current)
		}
		_, err = store.Version(ctx, "m1", 2)
		requireCode(t, err, docstore.CodeVersionNotFound, "a pruned version")
		must(t, store.Delete(ctx, "m1"))
		if rows := versionRows(t, e, fx, "m1"); rows != 0 {
			t.Fatalf("a deletion left %d version rows", rows)
		}
		must(t, store.Insert(ctx, member{ID: "m1", Name: "again"}))
		if got := versionNumbers(t, store, "m1"); !slices.Equal(got, []uint64{1}) {
			t.Fatalf("a document inserted again has versions %v", got)
		}
	})
}

// TestAWriteThatCannotKeepItsVersionsWritesNothing pins the one transaction
// on each engine: over a versions table nobody created, a write fails at its
// versions — after its document and index rows were written — and leaves
// neither, alone or inside the caller's transaction, which goes on.
func TestAWriteThatCannotKeepItsVersionsWritesNothing(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		ctx, table := t.Context(), uniqueName("unversioned__")
		migrateStore(t, e, table)
		tm := transactor(t, e)
		plain, err := docstore.OpenSQL(docstore.SQLConfig[member]{
			Key: func(m member) string { return m.ID }, Transactor: tm, Dialect: e.dialect, Table: table,
		}, memberIndexes()...)
		must(t, err)
		versioned, err := docstore.OpenSQL(docstore.SQLConfig[member]{
			Key: func(m member) string { return m.ID }, Transactor: tm, Dialect: e.dialect, Table: table, Versions: 3,
		}, memberIndexes()...)
		must(t, err)
		err = versioned.Put(ctx, member{ID: "m1", Email: "ada@x.dev"})
		requireCode(t, err, docstore.CodeStatementFailed, "a write whose versions table is missing")
		requireCode(t, errOf(plain.Get(ctx, "m1")), docstore.CodeDocumentNotFound, "the document of a failed write")
		requireCode(t, errOf(plain.Lookup(ctx, "email", "ada@x.dev")), docstore.CodeDocumentNotFound, "the index rows of a failed write")
		err = sql.Transact(ctx, tm, func(ctx context.Context, _ sql.Executor) error {
			requireCode(t, versioned.Put(ctx, member{ID: "m2"}), docstore.CodeStatementFailed, "a failed write, caught")
			return plain.Put(ctx, member{ID: "m3"})
		})
		must(t, err)
		requireCode(t, errOf(plain.Get(ctx, "m2")), docstore.CodeDocumentNotFound, "the caught write's document")
		must(t, errOf(plain.Get(ctx, "m3")))
	})
}

// TestConcurrentVersionedWritersOnEveryEngine pins the claim and the locks on
// each engine: sixteen writers creating and updating one document at once
// leave consecutive numbers from 1, none repeated, none lost, and every
// write's change in the document.
func TestConcurrentVersionedWritersOnEveryEngine(t *testing.T) {
	t.Parallel()
	engines := map[string]func(testing.TB) *engine{
		"sqlite":           func(tb testing.TB) *engine { return sqliteEngine(tb, "") },
		"sqlite-immediate": func(tb testing.TB) *engine { return sqliteEngine(tb, "immediate") },
		"postgres":         postgresEngine,
		"mysql":            mysqlEngine,
	}
	for name, open := range engines {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, store := t.Context(), openVersioned(t, open(t), 100).store
			var wg sync.WaitGroup
			for worker := range 16 {
				wg.Go(func() {
					if err := store.Put(ctx, member{ID: "shared", Name: "put"}); err != nil {
						t.Errorf("Put() = %v", err)
					}
					_, err := store.Update(ctx, "shared", func(m *member) error {
						m.Teams = append(m.Teams, string(rune('a'+worker)))
						return nil
					})
					if err != nil {
						t.Errorf("Update() = %v", err)
					}
				})
			}
			wg.Wait()
			got := versionNumbers(t, store, "shared")
			for i, number := range got {
				if number != uint64(len(got)-i) {
					t.Fatalf("the versions are numbered %v, want consecutive numbers down to 1", got)
				}
			}
			if len(got) < 17 {
				t.Fatalf("%d versions for 32 writes that each changed the document at least half the time", len(got))
			}
		})
	}
}

// TestAHoldOnEveryEngine pins the legal hold on each engine: Held reads the
// holds a framework keeps in the same database, inside the write's own
// transaction, and while it finds one nothing is pruned; the first write after
// the release prunes.
func TestAHoldOnEveryEngine(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		ctx, holdsTable := t.Context(), uniqueName("holds__")
		migrateStore(t, e, holdsTable)
		var holds *docstore.SQLStore[member]
		fx := openVersioned(t, e, 1, func(c *docstore.SQLConfig[member]) {
			holdsStore, err := docstore.OpenSQL(docstore.SQLConfig[member]{
				Key: func(m member) string { return m.ID }, Transactor: c.Transactor, Dialect: e.dialect, Table: holdsTable,
			})
			must(t, err)
			holds = holdsStore
			c.Held = func(ctx context.Context, key string) bool {
				_, err := holds.Get(ctx, key)
				return !errors.Is(err, docstore.DocumentNotFound)
			}
		})
		must(t, holds.Insert(ctx, member{ID: "m1"}))
		for _, name := range []string{"v1", "v2", "v3", "v4"} {
			must(t, fx.store.Put(ctx, member{ID: "m1", Name: name}))
		}
		if got := versionNumbers(t, fx.store, "m1"); !slices.Equal(got, []uint64{4, 3, 2, 1}) {
			t.Fatalf("a held document keeps %v, want every version", got)
		}
		must(t, holds.Delete(ctx, "m1"))
		must(t, fx.store.Put(ctx, member{ID: "m1", Name: "v5"}))
		if got := versionNumbers(t, fx.store, "m1"); !slices.Equal(got, []uint64{5, 4}) {
			t.Fatalf("after the release the document keeps %v, want 5 and 4", got)
		}
	})
}

// TestRewriteVersionsOnEveryEngine pins the erasure's rewrite on each engine:
// the former versions cleared or dropped in one transaction, the document
// untouched.
func TestRewriteVersionsOnEveryEngine(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		ctx, fx := t.Context(), openVersioned(t, e, 5)
		for _, email := range []string{"a@x.dev", "b@x.dev", "c@x.dev"} {
			must(t, fx.store.Put(ctx, member{ID: "m1", Email: email}))
		}
		must(t, fx.store.RewriteVersions(ctx, "m1", func(former []docstore.Version) ([]docstore.Version, error) {
			former[0].JSON = json.RawMessage(`{"id":"m1","email":"[erased]"}`)
			return former[:1], nil
		}))
		all, err := fx.store.Versions(ctx, "m1")
		must(t, err)
		if len(all) != 2 || all[0].Number != 3 || all[1].Number != 2 || string(all[1].JSON) != `{"id":"m1","email":"[erased]"}` {
			t.Fatalf("after the rewrite Versions() = %+v", all)
		}
		if m, err := fx.store.Lookup(ctx, "email", "c@x.dev"); err != nil || m.ID != "m1" {
			t.Fatalf("the rewrite touched the document: %+v, %v", m, err)
		}
	})
}

// TestADocumentStoredBeforeVersionsOnEveryEngine pins, on each engine, the
// document a store kept before it kept versions: read through the LEFT JOIN
// as version 1 at an unknown instant, then version 1 of the history its first
// write starts.
func TestADocumentStoredBeforeVersionsOnEveryEngine(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		ctx, table := t.Context(), uniqueName("upgraded__")
		migrateStore(t, e, table)
		tm := transactor(t, e)
		cfg := docstore.SQLConfig[member]{Key: func(m member) string { return m.ID }, Transactor: tm, Dialect: e.dialect, Table: table}
		plain, err := docstore.OpenSQL(cfg, memberIndexes()...)
		must(t, err)
		must(t, plain.Put(ctx, member{ID: "m1", Name: "before"}))
		migrateVersions(t, e, table)
		cfg.Versions = 3
		versioned, err := docstore.OpenSQL(cfg, memberIndexes()...)
		must(t, err)
		all, err := versioned.Versions(ctx, "m1")
		must(t, err)
		if len(all) != 1 || all[0].Number != 1 || !all[0].At.IsZero() || !strings.Contains(string(all[0].JSON), "before") {
			t.Fatalf("a document stored before versions = %+v", all)
		}
		must(t, versioned.Put(ctx, member{ID: "m1", Name: "after"}))
		if got := versionNumbers(t, versioned, "m1"); !slices.Equal(got, []uint64{2, 1}) {
			t.Fatalf("after its first versioned write the versions are %v", got)
		}
	})
}

// TestAnInstantFarFrom1970OnEveryEngine pins the instant to the nanosecond on
// each engine, in years one count of nanoseconds since 1970 cannot hold: the
// table keeps seconds and nanoseconds apart.
func TestAnInstantFarFrom1970OnEveryEngine(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		ancient := time.Date(1066, 10, 14, 9, 0, 0, 123456789, time.UTC)
		far := time.Date(2600, 1, 1, 0, 0, 0, 987654321, time.UTC)
		clk := clock.NewManualClock(ancient)
		ctx, store := t.Context(), openVersioned(t, e, 3, func(c *docstore.SQLConfig[member]) { c.Clock = clk }).store
		must(t, store.Put(ctx, member{ID: "m1", Name: "then"}))
		clk.Set(far)
		must(t, store.Put(ctx, member{ID: "m1", Name: "later"}))
		all, err := store.Versions(ctx, "m1")
		must(t, err)
		if len(all) != 2 || !all[0].At.Equal(far) || !all[1].At.Equal(ancient) {
			t.Fatalf("the versions were made at %+v, want %v and %v", all, far, ancient)
		}
	})
}
