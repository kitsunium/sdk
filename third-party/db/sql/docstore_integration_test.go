//go:build integration

// Package sql_test — the document store over SQL on each real engine, against
// the requirements kit's ADR 0004 lists for it: the write modes, the
// documents as written, keys as bytes, the indexes, an atomic Update, the
// caller's transaction joined, hooks after the commit, and no key quoted.
package sql_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/docstore"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/sql"
)

// member is the document most cases store.
type member struct {
	ID    string   `json:"id"`
	Email string   `json:"email,omitempty"`
	Name  string   `json:"name,omitempty"`
	Teams []string `json:"teams,omitempty"`
}

// memberIndexes are a unique "email" and a multi-valued "team".
func memberIndexes() []docstore.IndexSpec[member] {
	return []docstore.IndexSpec[member]{
		docstore.Unique("email", func(m member) string { return m.Email }),
		docstore.Index("team", func(m member) []string { return m.Teams }),
	}
}

// storeFixture is one case's store, its transactor, and its table.
type storeFixture struct {
	tm    sql.Transactor
	store *docstore.SQLStore[member]
	table string
}

// migrateStore creates the tables of a store named table on e, through the
// SDK's own Migrator under a version table of the case's own.
func migrateStore(tb testing.TB, e *engine, table string) {
	tb.Helper()
	migration, err := docstore.SQLMigration(e.dialect, table, 1)
	must(tb, err)
	runner, err := sql.NewMigrator(sql.Config{DB: e.db, Dialect: e.dialect, Pool: sql.PoolConfig{MaxOpen: 16}},
		sql.MigrateConfig{Migrations: []sql.Migration{migration}, VersionTable: uniqueName("versions_")})
	must(tb, err)
	must(tb, runner.Up(tb.Context()))
}

// openMembers migrates and opens a members store on e.
func openMembers(tb testing.TB, e *engine, configure ...func(*docstore.SQLConfig[member])) *storeFixture {
	tb.Helper()
	table := uniqueName("members__")
	migrateStore(tb, e, table)
	tm := transactor(tb, e)
	cfg := docstore.SQLConfig[member]{Key: func(m member) string { return m.ID }, Transactor: tm, Dialect: e.dialect, Table: table}
	for _, c := range configure {
		c(&cfg)
	}
	store, err := docstore.OpenSQL(cfg, memberIndexes()...)
	must(tb, err)
	return &storeFixture{tm: tm, store: store, table: table}
}

// requireCode fails the test unless err carries code.
func requireCode(t *testing.T, err error, code errs.Code, what string) {
	t.Helper()
	if !errs.HasCode(err, code) {
		t.Fatalf("%s = %v, want %v", what, err, code)
	}
}

// quotesNothing fails the test when err's text or fields carry a secret.
func quotesNothing(t *testing.T, err error, secrets ...string) {
	t.Helper()
	text := err.Error()
	for _, field := range errs.FieldsOf(err) {
		text += " " + field.StringValue()
	}
	for _, secret := range secrets {
		if strings.Contains(text, secret) {
			t.Fatalf("the error quotes %q: %s", secret, text)
		}
	}
}

// TestTheWriteModesOnEveryEngine pins Put, Insert, Replace and Delete, with
// the refusals kit's said maps, on each engine.
func TestTheWriteModesOnEveryEngine(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		ctx, store := t.Context(), openMembers(t, e).store
		must(t, store.Insert(ctx, member{ID: "m1", Name: "Ada"}))
		err := store.Insert(ctx, member{ID: "m1"})
		requireCode(t, err, docstore.CodeDocumentExists, "a second Insert")
		quotesNothing(t, err, "m1")
		requireCode(t, store.Replace(ctx, member{ID: "m2"}), docstore.CodeDocumentNotFound, "Replace of a missing key")
		must(t, store.Put(ctx, member{ID: "m2", Name: "Grace"}))
		must(t, store.Put(ctx, member{ID: "m2", Name: "Grace Hopper"}))
		if got, getErr := store.Get(ctx, "m2"); getErr != nil || got.Name != "Grace Hopper" {
			t.Fatalf("Get() = %+v, %v", got, getErr)
		}
		must(t, store.Delete(ctx, "m2"))
		requireCode(t, store.Replace(ctx, member{ID: "m2"}), docstore.CodeDocumentNotFound, "Replace after a Delete")
		if n, countErr := store.Count(ctx); countErr != nil || n != 1 {
			t.Fatalf("Count() = %d, %v", n, countErr)
		}
	})
}

// verbatim is a document whose JSON is written by hand, so a test can hold
// bytes encoding/json would never produce by itself — member order, a
// number's spelling, a duplicate member — and ask the database to keep them.
type verbatim struct {
	id  string
	raw json.RawMessage
}

// MarshalJSON writes the document as given.
func (v verbatim) MarshalJSON() ([]byte, error) { return v.raw, nil }

// UnmarshalJSON keeps the bytes as read.
func (v *verbatim) UnmarshalJSON(b []byte) error {
	v.raw = slices.Clone(b)
	return nil
}

// TestADocumentReadsBackByteForByte pins the document as written: the bytes
// the store encoded, not the engine's JSON type — which would sort the
// members, keep one of two duplicates and respell the number.
func TestADocumentReadsBackByteForByte(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		ctx, table := t.Context(), uniqueName("docs__")
		migrateStore(t, e, table)
		store, err := docstore.OpenSQL(docstore.SQLConfig[verbatim]{
			Key: func(v verbatim) string { return v.id }, Transactor: transactor(t, e), Dialect: e.dialect, Table: table,
		})
		must(t, err)
		written := `{"z":1,"a":1.230e-5,"a":"duplicate","m":[3,2,1]}`
		must(t, store.Put(ctx, verbatim{id: "d1", raw: json.RawMessage(written)}))
		got, err := store.Get(ctx, "d1")
		must(t, err)
		if string(got.raw) != written {
			t.Fatalf("Get() = %s, want the bytes written: %s", got.raw, written)
		}
		entries, err := store.Entries(ctx, 0)
		must(t, err)
		if len(entries) != 1 || string(entries[0].JSON) != written {
			t.Fatalf("Entries() = %+v", entries)
		}
	})
}

// TestKeysAreComparedAsBytes pins three pairs kit's conformance names — a/A,
// a/a␠ and é/e — as six keys, in Go's order: a case-insensitive or
// pad-space collation would fold them.
func TestKeysAreComparedAsBytes(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		ctx, store := t.Context(), openMembers(t, e).store
		keys := []string{"a", "A", "a ", "é", "e", "b"}
		for _, key := range keys {
			must(t, store.Insert(ctx, member{ID: key, Name: "of " + key}))
		}
		for _, key := range keys {
			if m, err := store.Get(ctx, key); err != nil || m.Name != "of "+key {
				t.Fatalf("Get(%q) = %+v, %v — the key was folded into another", key, m, err)
			}
		}
		all, err := store.List(ctx)
		must(t, err)
		got := make([]string, len(all))
		for i, m := range all {
			got[i] = m.ID
		}
		if want := slices.Sorted(slices.Values(keys)); !slices.Equal(got, want) {
			t.Fatalf("List() order = %q, want Go's %q", got, want)
		}
	})
}

// TestIndexesOnEveryEngine pins the unique index's refusal naming the index
// and never the key, Find in store-key order, a key freed by an update, and
// index keys kept hashed.
func TestIndexesOnEveryEngine(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		ctx := t.Context()
		fx := openMembers(t, e, func(c *docstore.SQLConfig[member]) {
			c.IndexKey = func(index, key string) []byte {
				sum := sha256.Sum256([]byte(index + "\x00" + key))
				return sum[:]
			}
		})
		must(t, fx.store.Insert(ctx, member{ID: "m2", Email: "ada@x.dev", Teams: []string{"red"}}))
		must(t, fx.store.Insert(ctx, member{ID: "m1", Email: "grace@x.dev", Teams: []string{"red", "blue"}}))
		err := fx.store.Insert(ctx, member{ID: "m3", Email: "ada@x.dev"})
		requireCode(t, err, docstore.CodeUniqueKeyTaken, "a second holder of a unique key")
		quotesNothing(t, err, "ada@x.dev", "m3")
		red, err := fx.store.Find(ctx, "team", "red")
		must(t, err)
		if len(red) != 2 || red[0].ID != "m1" || red[1].ID != "m2" {
			t.Fatalf("Find(red) = %+v, want m1 then m2", red)
		}
		if m, lookErr := fx.store.Lookup(ctx, "email", "ada@x.dev"); lookErr != nil || m.ID != "m2" {
			t.Fatalf("Lookup() = %+v, %v", m, lookErr)
		}
		_, err = fx.store.Update(ctx, "m2", func(m *member) error { m.Email = "lovelace@x.dev"; return nil })
		must(t, err)
		must(t, fx.store.Insert(ctx, member{ID: "m3", Email: "ada@x.dev"}))
		var inClear int
		ex, _ := fx.tm.(sql.Joiner).Join(ctx)
		query := "SELECT COUNT(*) FROM " + fx.table + "___ix WHERE index_key = ?"
		if e.dialect == sql.DialectPostgres {
			query = strings.Replace(query, "?", "$1", 1)
		}
		must(t, ex.QueryRowContext(ctx, query, []byte("ada@x.dev")).Scan(&inClear))
		if inClear != 0 {
			t.Fatal("an index key reached the table in clear")
		}
	})
}

// TestUpdateIsAtomicUnderContention pins the atomic Update: thirty-two writers
// of one document, none of whose changes is lost — the row locked on
// PostgreSQL and MySQL, the database's write lock on SQLite — and thirty-two
// inserters of one unique key, exactly one winning.
func TestUpdateIsAtomicUnderContention(t *testing.T) {
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
			ctx, store := t.Context(), openMembers(t, open(t)).store
			must(t, store.Put(ctx, member{ID: "counter"}))
			var wg sync.WaitGroup
			for range 32 {
				wg.Go(func() {
					_, err := store.Update(ctx, "counter", func(m *member) error {
						m.Teams = append(m.Teams, "x")
						return nil
					})
					if err != nil {
						t.Errorf("Update() = %v", err)
					}
				})
			}
			wg.Wait()
			if m, err := store.Get(ctx, "counter"); err != nil || len(m.Teams) != 32 {
				t.Fatalf("after 32 Updates the document holds %d, want 32 (%v)", len(m.Teams), err)
			}
			var mu sync.Mutex
			wins, conflicts := 0, 0
			for i := range 32 {
				wg.Go(func() {
					err := store.Insert(ctx, member{ID: fmt.Sprintf("m%02d", i), Email: "same@x.dev"})
					mu.Lock()
					defer mu.Unlock()
					switch {
					case err == nil:
						wins++
					case errs.HasCode(err, docstore.CodeUniqueKeyTaken):
						conflicts++
					default:
						t.Errorf("Insert() = %v", err)
					}
				})
			}
			wg.Wait()
			if wins != 1 || conflicts != 31 {
				t.Fatalf("%d won and %d conflicted, want 1 and 31", wins, conflicts)
			}
		})
	}
}

// TestTheCallersTransactionIsJoined pins the transaction in the context: a
// store call runs in its caller's transaction — its write undone by the
// caller's rollback, a refused write undone alone — and its hooks run after
// the commit, never for a write rolled back.
func TestTheCallersTransactionIsJoined(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		ctx, fx := t.Context(), openMembers(t, e)
		var written []string
		fx.store.OnWrite(func(key string) { written = append(written, key) })
		errWork := errors.New("the unit of work failed")
		err := sql.Transact(ctx, fx.tm, func(ctx context.Context, _ sql.Executor) error {
			must(t, fx.store.Insert(ctx, member{ID: "rolled-back"}))
			return errWork
		})
		if !errors.Is(err, errWork) {
			t.Fatalf("Transact() = %v", err)
		}
		requireCode(t, errOf(fx.store.Get(ctx, "rolled-back")), docstore.CodeDocumentNotFound, "a rolled-back write")
		err = sql.Transact(ctx, fx.tm, func(ctx context.Context, _ sql.Executor) error {
			must(t, fx.store.Insert(ctx, member{ID: "m1", Email: "ada@x.dev"}))
			requireCode(t, fx.store.Insert(ctx, member{ID: "m1"}), docstore.CodeDocumentExists, "a taken key, caught")
			requireCode(t, fx.store.Insert(ctx, member{ID: "m2", Email: "ada@x.dev"}), docstore.CodeUniqueKeyTaken, "a taken unique key, caught")
			if m, getErr := fx.store.Get(ctx, "m1"); getErr != nil || m.ID != "m1" {
				t.Errorf("the transaction does not read its own write: %+v, %v", m, getErr)
			}
			if len(written) != 0 {
				t.Errorf("a hook ran before the commit: %v", written)
			}
			nested := sql.Transact(ctx, fx.tm, func(ctx context.Context, _ sql.Executor) error {
				must(t, fx.store.Insert(ctx, member{ID: "undone-alone"}))
				return errWork
			})
			if !errors.Is(nested, errWork) {
				t.Errorf("the nested Transact = %v", nested)
			}
			return fx.store.Put(ctx, member{ID: "m3"})
		})
		must(t, err)
		if !slices.Equal(written, []string{"m1", "m3"}) {
			t.Fatalf("OnWrite saw %v, want m1 then m3, after the commit", written)
		}
		for key, present := range map[string]bool{"m1": true, "m2": false, "m3": true, "undone-alone": false} {
			if _, getErr := fx.store.Get(ctx, key); (getErr == nil) != present {
				t.Fatalf("%s present = %v, want %v", key, getErr == nil, present)
			}
		}
	})
}

// errOf drops a value and keeps the error.
func errOf[T any](_ T, err error) error { return err }

// TestADatabaseFailureWithholdsTheDriversText pins STATEMENT_FAILED on a real
// driver: a store over a table nobody created fails every call as the one
// verdict a caller maps to 503, with the driver's own error one errors.As
// away and none of its words in the text.
func TestADatabaseFailureWithholdsTheDriversText(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		store, err := docstore.OpenSQL(docstore.SQLConfig[member]{
			Key: func(m member) string { return m.ID }, Transactor: transactor(t, e), Dialect: e.dialect,
			Table: uniqueName("never_created_"),
		}, memberIndexes()...)
		must(t, err)
		_, err = store.Get(t.Context(), "secret-key")
		requireCode(t, err, docstore.CodeStatementFailed, "a read of a missing table")
		quotesNothing(t, err, "secret-key")
		if errs.HTTPStatusOf(err) != 503 {
			t.Fatalf("HTTP status %d, want 503", errs.HTTPStatusOf(err))
		}
		err = store.Put(t.Context(), member{ID: "secret-key", Email: "ada@x.dev"})
		requireCode(t, err, docstore.CodeStatementFailed, "a write to a missing table")
		quotesNothing(t, err, "secret-key", "ada@x.dev")
		t.Logf("%s: %v", e.name, err)
	})
}

// TestReindexOnEveryEngine pins the rebuild: an index declared over stored
// documents finds them only after Reindex, and documents sharing a key made
// unique refuse it with INDEX_BROKEN, changing nothing.
func TestReindexOnEveryEngine(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		ctx, table := t.Context(), uniqueName("reindexed__")
		migrateStore(t, e, table)
		tm := transactor(t, e)
		open := func(indexes ...docstore.IndexSpec[member]) *docstore.SQLStore[member] {
			store, err := docstore.OpenSQL(docstore.SQLConfig[member]{
				Key: func(m member) string { return m.ID }, Transactor: tm, Dialect: e.dialect, Table: table,
			}, indexes...)
			must(t, err)
			return store
		}
		before := open()
		for i := range 300 {
			must(t, before.Put(ctx, member{ID: fmt.Sprintf("m%03d", i), Email: "shared@x.dev", Teams: []string{"all"}}))
		}
		teams := open(docstore.Index("team", func(m member) []string { return m.Teams }))
		if found, err := teams.Find(ctx, "team", "all"); err != nil || len(found) != 0 {
			t.Fatalf("Find() before the rebuild = %d, %v", len(found), err)
		}
		must(t, teams.Reindex(ctx))
		if found, err := teams.Find(ctx, "team", "all"); err != nil || len(found) != 300 {
			t.Fatalf("Find() after the rebuild = %d, %v, want 300", len(found), err)
		}
		unique := open(docstore.Unique("email", func(m member) string { return m.Email }),
			docstore.Index("team", func(m member) []string { return m.Teams }))
		err := unique.Reindex(ctx)
		requireCode(t, err, docstore.CodeIndexBroken, "documents sharing a key made unique")
		quotesNothing(t, err, "shared@x.dev")
		if found, findErr := unique.Find(ctx, "team", "all"); findErr != nil || len(found) != 300 {
			t.Fatalf("a refused rebuild changed the rows: %d, %v", len(found), findErr)
		}
	})
}
