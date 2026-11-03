// Package docstore_test — what is the SQL engine's own in a document's
// versions, on each dialect's statements over the fake engine: the rows a
// deletion takes and a creation clears, a hold asked inside the write's
// transaction, the statements a store without versions never sends, the
// instant over every year, a failed statement undoing its write, and
// writers racing the claim. The contract every engine keeps is
// conformance_external_test.go's.
package docstore_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/service/data/docstore"
)

// keeping configures a SQL store to keep versions former versions, stamped
// on clk — the system clock when nil.
func keeping(versions int, clk clock.Clock) func(*docstore.SQLConfig[account]) {
	return func(cfg *docstore.SQLConfig[account]) { cfg.Versions, cfg.Clock = versions, clk }
}

// openSQLVersioned opens an accounts store on dialect over a fresh engine,
// with the accounts indexes, configured by each of configure in turn.
func openSQLVersioned(t *testing.T, dialect coresql.Dialect, configure ...func(*docstore.SQLConfig[account])) *sqlFixture {
	t.Helper()
	engine := newSQLEngine(dialect, accountsTable)
	tm := newSQLTransactor(t, engine, dialect)
	return &sqlFixture{engine: engine, tm: tm, store: reopenSQL(t, tm, dialect, configure...)}
}

// reopenSQL opens another accounts store over the transactor tm, on the same
// engine, configured by each of configure in turn.
func reopenSQL(t *testing.T, tm coresql.Transactor, dialect coresql.Dialect,
	configure ...func(*docstore.SQLConfig[account]),
) *docstore.SQLStore[account] {
	t.Helper()
	cfg := docstore.SQLConfig[account]{Key: accountKey, Transactor: tm, Dialect: dialect, Table: accountsTable}
	for _, c := range configure {
		c(&cfg)
	}
	store, err := docstore.OpenSQL(cfg, accountIndexes()...)
	if err != nil {
		t.Fatalf("OpenSQL() = %v", err)
	}
	return store
}

// sqlVersionsOf reads key's versions, failing the test on an error.
func sqlVersionsOf(t *testing.T, store *docstore.SQLStore[account], key string) []coredocstore.VersionValue {
	t.Helper()
	all, err := store.Versions(t.Context(), key)
	if err != nil {
		t.Fatalf("Versions(%s) = %v", key, err)
	}
	return all
}

// TestSQLADeletionLeavesNoVersionRow pins the SQL engine's own half of the
// conformance row ADeletionTakesTheVersions: the deletion removes every
// version row in its own transaction, on every dialect.
func TestSQLADeletionLeavesNoVersionRow(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		ctx, fx := t.Context(), openSQLVersioned(t, dialect, keeping(3, nil))
		must(t, fx.store.Put(ctx, account{ID: "acc_1", Name: "a"}))
		must(t, fx.store.Put(ctx, account{ID: "acc_1", Name: "b"}))
		must(t, fx.store.Delete(ctx, "acc_1"))
		if left := fx.engine.snapshot().versionsOf("acc_1", false); len(left) != 0 {
			t.Fatalf("the deletion left the version rows %v", left)
		}
	})
}

// TestSQLHeldIsAskedInsideTheWrite pins the SQL engine's own half of the
// conformance row AHeldDocumentKeepsItsVersions, on every dialect: Held is
// asked inside the write's transaction, with its context — a read it makes
// through the store joins it, and sees the document the write is storing.
func TestSQLHeldIsAskedInsideTheWrite(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		var mu sync.Mutex
		asked := 0
		var store *docstore.SQLStore[account]
		fx := openSQLVersioned(t, dialect, keeping(1, nil), func(cfg *docstore.SQLConfig[account]) {
			cfg.Held = func(ctx context.Context, key string) bool {
				//: inside the write's transaction: this read joins it, and
				//: sees the document the write is storing.
				if doc, err := store.Get(ctx, key); err != nil || doc.Name != "v3" {
					t.Errorf("a read inside Held = %+v, %v, want the document being written", doc, err)
				}
				mu.Lock()
				defer mu.Unlock()
				asked++
				return true
			}
		})
		ctx, store := t.Context(), fx.store
		for _, name := range []string{"v1", "v2", "v3"} {
			must(t, store.Put(ctx, account{ID: "acc_1", Name: name}))
		}
		mu.Lock()
		defer mu.Unlock()
		if asked != 1 {
			t.Fatalf("Held was asked %d times, want once: by the one write that would prune", asked)
		}
	})
}

// TestSQLAStoreWithoutVersionsTouchesNoVersionsTable pins the zero value on
// every dialect: every read and rewrite of versions refused, stamped writes
// taken as plain ones, and not one statement sent to the versions table.
func TestSQLAStoreWithoutVersionsTouchesNoVersionsTable(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		ctx, fx := t.Context(), openSQLVersioned(t, dialect)
		must(t, fx.store.PutStamped(ctx, account{ID: "acc_1"}, coredocstore.StampValue{Meta: map[string]string{"by": "ada"}}))
		must(t, fx.store.Delete(ctx, "acc_1"))
		_, err := fx.store.Versions(ctx, "acc_1")
		requireCode(t, err, coredocstore.CodeVersionsNotKept, "Versions")
		_, err = fx.store.Version(ctx, "acc_1", 1)
		requireCode(t, err, coredocstore.CodeVersionsNotKept, "Version")
		err = fx.store.RewriteVersions(ctx, "acc_1", func(f []coredocstore.VersionValue) ([]coredocstore.VersionValue, error) { return f, nil })
		requireCode(t, err, coredocstore.CodeVersionsNotKept, "RewriteVersions")
		for _, role := range fx.engine.roles() {
			if strings.Contains(role, "ersion") || role == "claim" || role == "pruneCut" || role == "retireHead" {
				t.Fatalf("a store without versions sent %s", role)
			}
		}
	})
}

// TestSQLADocumentStoredBeforeVersionsIsVersionOne pins a store that begins
// to keep versions over a table it wrote without: each document is version 1,
// made at an unknown instant, until its first write makes version 2.
func TestSQLADocumentStoredBeforeVersionsIsVersionOne(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		ctx, fx := t.Context(), openSQLVersioned(t, dialect)
		must(t, fx.store.Put(ctx, account{ID: "acc_1", Name: "before"}))
		store := reopenSQL(t, fx.tm, dialect, keeping(3, clock.NewManualClock(epoch)))
		all := sqlVersionsOf(t, store, "acc_1")
		if len(all) != 1 || all[0].Number != 1 || !all[0].At.IsZero() || all[0].Meta != nil {
			t.Fatalf("a document stored before versions = %+v, want version 1 at an unknown instant", all)
		}
		must(t, store.Put(ctx, account{ID: "acc_1", Name: "after"}))
		all = sqlVersionsOf(t, store, "acc_1")
		if got := numbers(all); !slices.Equal(got, []uint64{2, 1}) || !all[0].At.Equal(epoch) || !all[1].At.IsZero() {
			t.Fatalf("after its first versioned write = %+v", all)
		}
		if got := namesIn(t, all); !slices.Equal(got, []string{"after", "before"}) {
			t.Fatalf("after its first versioned write the versions hold %v", got)
		}
	})
}

// TestSQLAFailedVersionStatementUndoesTheWrite pins the same durable write on
// every dialect: a statement of the versions that fails — the new version's
// row, the retired one, the pruning — rolls the whole write back, document
// and index rows included, and inside the caller's transaction undoes the
// write alone.
func TestSQLAFailedVersionStatementUndoesTheWrite(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		ctx, fx := t.Context(), openSQLVersioned(t, dialect, keeping(1, nil))
		must(t, fx.store.Put(ctx, account{ID: "acc_1", Name: "v1", Email: "v1@x.dev"}))
		must(t, fx.store.Put(ctx, account{ID: "acc_1", Name: "v2", Email: "v2@x.dev"}))
		for _, role := range []string{"retireHead", "insertVersions", "pruneCut", "pruneVersions", "versionHead"} {
			fx.engine.failNext(role, errors.New("the statement was cancelled"))
			err := fx.store.Put(ctx, account{ID: "acc_1", Name: "refused", Email: "refused@x.dev"})
			requireCode(t, err, coredocstore.CodeStatementFailed, role)
			if got := namesIn(t, sqlVersionsOf(t, fx.store, "acc_1")); !slices.Equal(got, []string{"v2", "v1"}) {
				t.Fatalf("%s failed and the versions hold %v", role, got)
			}
			if a, lookupErr := fx.store.Lookup(ctx, "email", "v2@x.dev"); lookupErr != nil || a.Name != "v2" {
				t.Fatalf("%s failed and the index holds %+v, %v", role, a, lookupErr)
			}
		}
		fx.engine.failNext("insertVersions", errors.New("the statement was cancelled"))
		err := fx.tm.Transact(ctx, coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
			requireCode(t, fx.store.Put(ctx, account{ID: "acc_1", Name: "refused"}), coredocstore.CodeStatementFailed, "a failed write, caught")
			return fx.store.Put(ctx, account{ID: "acc_2", Name: "kept"})
		})
		must(t, err)
		if got := namesIn(t, sqlVersionsOf(t, fx.store, "acc_1")); !slices.Equal(got, []string{"v2", "v1"}) {
			t.Fatalf("the caught failure left the versions %v", got)
		}
		if got := numbers(sqlVersionsOf(t, fx.store, "acc_2")); !slices.Equal(got, []uint64{1}) {
			t.Fatalf("the write after the caught failure left %v", got)
		}
	})
}

// TestSQLVersionsRoundTrips pins what a write sends on a store that keeps
// versions, which is what it costs: the claim that creates or locks the
// document before a Put replaces it, the version rows, and the pruning, all
// between the same BEGIN and COMMIT — or inside one savepoint of the caller's.
func TestSQLVersionsRoundTrips(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		fx := openSQLVersioned(t, dialect, keeping(1, nil))
		runCase := func(t *testing.T, c roundTripCase) {
			t.Helper()
			fx.engine.resetLog()
			must(t, c.call(t.Context()))
			if got := fx.engine.roles(); !slices.Equal(got, c.want) {
				t.Fatalf("%s sent\n%v\nwant\n%v", c.name, got, c.want)
			}
		}
		//: in order: each case writes over what the one before left.
		for _, c := range versionsRoundTripCases(fx, dialect) {
			runCase(t, c)
		}
	})
}

// roundTripCase is one call and the statements it must send.
type roundTripCase struct {
	call func(ctx context.Context) error
	name string
	want []string
}

// versionsRoundTripCases are TestSQLVersionsRoundTrips' calls on dialect, in
// the order they run over fx's store keeping one former version.
func versionsRoundTripCases(fx *sqlFixture, dialect coresql.Dialect) []roundTripCase {
	begin, claimed := "BEGIN", []string{"claim"}
	//: MySQL opens its own transactions READ COMMITTED, and reads what its
	//: claim locked in a statement of its own.
	if dialect == coresql.DialectMySQL {
		begin, claimed = "BEGIN READ COMMITTED", []string{"claim", "lockDoc"}
	}
	replaced := func(tail ...string) []string {
		return slices.Concat([]string{begin}, claimed, []string{"writeLocked", "uniqueTaken", "deleteIndex", "insertIndexRows"}, tail)
	}
	store := fx.store
	return []roundTripCase{
		{
			name: "Put of a new document", call: func(ctx context.Context) error { return store.Put(ctx, account{ID: "acc_1", Email: "a@x.dev"}) },
			want: []string{begin, "claim", "uniqueTaken", "insertIndexRows", "firstVersion", "insertVersions", "COMMIT"},
		},
		{
			name: "Put over it", call: func(ctx context.Context) error { return store.Put(ctx, account{ID: "acc_1", Email: "b@x.dev"}) },
			want: replaced("versionHead", "retireHead", "insertVersions", "pruneCut", "COMMIT"),
		},
		{
			name: "Put over it again, pruning", call: func(ctx context.Context) error { return store.Put(ctx, account{ID: "acc_1", Email: "c@x.dev"}) },
			want: replaced("versionHead", "retireHead", "insertVersions", "pruneCut", "pruneVersions", "COMMIT"),
		},
		{
			name: "Put of the same document", call: func(ctx context.Context) error { return store.Put(ctx, account{ID: "acc_1", Email: "c@x.dev"}) },
			want: replaced("pruneCut", "COMMIT"),
		},
		{
			name: "Insert", call: func(ctx context.Context) error { return store.Insert(ctx, account{ID: "acc_2"}) },
			want: []string{begin, "insert", "firstVersion", "insertVersions", "COMMIT"},
		},
		{
			name: "Replace", call: func(ctx context.Context) error { return store.Replace(ctx, account{ID: "acc_2", Name: "b"}) },
			want: []string{begin, "lockDoc", "writeLocked", "deleteIndex", "versionHead", "retireHead", "insertVersions", "pruneCut", "COMMIT"},
		},
		{
			name: "Update", call: func(ctx context.Context) error {
				_, err := store.Update(ctx, "acc_2", func(a *account) error { a.Name = "c"; return nil })
				return err
			},
			want: []string{
				begin, "lockDoc", "writeLocked", "deleteIndex", "versionHead", "retireHead", "insertVersions", "pruneCut",
				"pruneVersions", "COMMIT",
			},
		},
		{
			name: "Delete", call: func(ctx context.Context) error { return store.Delete(ctx, "acc_2") },
			want: []string{begin, "deleteDoc", "deleteIndex", "dropVersions", "COMMIT"},
		},
		{
			name: "Versions", call: func(ctx context.Context) error { _, err := store.Versions(ctx, "acc_1"); return err },
			want: []string{"readVersions"},
		},
		{
			name: "a Put inside the caller's transaction", call: func(ctx context.Context) error {
				return fx.tm.Transact(ctx, coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
					return store.Put(ctx, account{ID: "acc_1", Email: "d@x.dev"})
				})
			},
			want: slices.Concat([]string{"BEGIN", "SAVEPOINT ktn_sp_1"}, claimed, []string{
				"writeLocked", "uniqueTaken", "deleteIndex", "insertIndexRows",
				"versionHead", "retireHead", "insertVersions", "pruneCut", "pruneVersions",
				"RELEASE SAVEPOINT ktn_sp_1", "COMMIT",
			}),
		},
		{
			name: "Trash", call: func(ctx context.Context) error { return store.Trash(ctx, "acc_1") },
			want: []string{begin, "lockDoc", "deleteDoc", "deleteIndex", "insertVersions", "COMMIT"},
		},
		{
			name: "Versions of a trashed document", call: func(ctx context.Context) error { _, err := store.Versions(ctx, "acc_1"); return err },
			want: []string{"readVersions", "readTrashed"},
		},
		{
			name: "Tombstones", call: func(ctx context.Context) error { _, err := store.Tombstones(ctx); return err },
			want: []string{"tombstones"},
		},
		{
			name: "Restore", call: func(ctx context.Context) error { return store.Restore(ctx, "acc_1") },
			want: []string{begin, "firstVersion", "insert", "uniqueTaken", "insertIndexRows", "dropTombstone", "COMMIT"},
		},
		{
			name: "Trash again", call: func(ctx context.Context) error { return store.Trash(ctx, "acc_1") },
			want: []string{begin, "lockDoc", "deleteDoc", "deleteIndex", "insertVersions", "COMMIT"},
		},
		{
			name: "Insert under the trashed key", call: func(ctx context.Context) error { return store.Insert(ctx, account{ID: "acc_1", Email: "e@x.dev"}) },
			want: []string{
				begin, "insert", "uniqueTaken", "insertIndexRows", "firstVersion", "dropTombstone",
				"versionHead", "retireHead", "insertVersions", "pruneCut", "pruneVersions", "COMMIT",
			},
		},
		{
			name: "Trash, to purge", call: func(ctx context.Context) error { return store.Trash(ctx, "acc_1") },
			want: []string{begin, "lockDoc", "deleteDoc", "deleteIndex", "insertVersions", "COMMIT"},
		},
		{
			name: "Purge", call: func(ctx context.Context) error { return store.Purge(ctx, "acc_1") },
			want: []string{begin, "firstVersion", "dropVersions", "COMMIT"},
		},
		{
			name: "Put after the purge", call: func(ctx context.Context) error { return store.Put(ctx, account{ID: "acc_1", Email: "c@x.dev"}) },
			want: []string{begin, "claim", "uniqueTaken", "insertIndexRows", "firstVersion", "insertVersions", "COMMIT"},
		},
	}
}

// TestSQLConcurrentVersionedWriters pins the claim and the locks on every
// dialect: writers of one document, creating it and updating it at once,
// leave consecutive numbers with no gap, no repeat and nothing lost — the
// newest versions kept.
func TestSQLConcurrentVersionedWriters(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		ctx, store := t.Context(), openSQLVersioned(t, dialect, keeping(40, nil)).store
		var wg sync.WaitGroup
		for worker := range 16 {
			wg.Go(func() {
				tolerate(t, store.Put(ctx, account{ID: "shared", Name: "put"}))
				_, err := store.Update(ctx, "shared", func(a *account) error { a.Teams = append(a.Teams, string(rune('a'+worker))); return nil })
				tolerate(t, err)
			})
		}
		wg.Wait()
		all := sqlVersionsOf(t, store, "shared")
		for i, v := range all {
			if want := all[0].Number - uint64(i); v.Number != want {
				t.Fatalf("the versions are numbered %v, want consecutive numbers", numbers(all))
			}
		}
		if all[len(all)-1].Number != 1 {
			t.Fatalf("the oldest version is %d, want 1 — nothing pruned below 41 versions", all[len(all)-1].Number)
		}
	})
}

// TestSQLARefusedConfigurationSendsNothing pins the SQL engine's own half of
// the conformance row VersionsConfigurationRefusals: a configuration refused
// before any statement — a negative Versions, a Held without Versions —
// sends none.
func TestSQLARefusedConfigurationSendsNothing(t *testing.T) {
	t.Parallel()
	engine := newSQLEngine(coresql.DialectSQLite, accountsTable)
	tm := newSQLTransactor(t, engine, coresql.DialectSQLite)
	for name, cfg := range map[string]docstore.SQLConfig[account]{
		"a negative Versions": {Key: accountKey, Transactor: tm, Dialect: coresql.DialectSQLite, Table: accountsTable, Versions: -1},
		"Held without Versions": {
			Key: accountKey, Transactor: tm, Dialect: coresql.DialectSQLite, Table: accountsTable,
			Held: func(context.Context, string) bool { return true },
		},
	} {
		_, err := docstore.OpenSQL(cfg)
		requireCode(t, err, coredocstore.CodeStoreMisconfigured, name)
	}
	if roles := engine.roles(); len(roles) != 0 {
		t.Fatalf("a refused configuration sent %v", roles)
	}
}

// TestSQLACreationStartsANewHistory pins the rows a creation clears: a
// document deleted while its store kept no versions leaves its rows in the
// versions table, and one created under its key once versions are kept again
// starts at version 1 without them — in both write modes that create.
func TestSQLACreationStartsANewHistory(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		ctx, fx := t.Context(), openSQLVersioned(t, dialect, keeping(5, nil))
		for _, name := range []string{"a", "b", "c"} {
			must(t, fx.store.Put(ctx, account{ID: "acc_1", Name: name}))
			must(t, fx.store.Put(ctx, account{ID: "acc_2", Name: name}))
		}
		plain := reopenSQL(t, fx.tm, dialect)
		must(t, plain.Delete(ctx, "acc_1"))
		must(t, plain.Delete(ctx, "acc_2"))
		if left := fx.engine.snapshot().versionsOf("acc_1", false); len(left) != 3 {
			t.Fatalf("a store without versions touched the versions table: %v left", left)
		}
		must(t, fx.store.Insert(ctx, account{ID: "acc_1", Name: "new"}))
		must(t, fx.store.Put(ctx, account{ID: "acc_2", Name: "new"}))
		for _, key := range []string{"acc_1", "acc_2"} {
			if got := numbers(sqlVersionsOf(t, fx.store, key)); !slices.Equal(got, []uint64{1}) {
				t.Fatalf("%s created again has versions %v, want 1 alone", key, got)
			}
		}
	})
}

// TestSQLAnInstantOutsideTheNanosecondRangeIsKept pins the instant to the
// nanosecond over every instant a time.Time holds, on every dialect: the
// seconds and the nanoseconds travel in two columns, where one count of
// nanoseconds since 1970 would wrap before 1678 and after 2262.
func TestSQLAnInstantOutsideTheNanosecondRangeIsKept(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		ancient := time.Date(1066, 10, 14, 9, 0, 0, 123456789, time.UTC)
		far := time.Date(2600, 1, 1, 0, 0, 0, 987654321, time.UTC)
		clk := clock.NewManualClock(ancient)
		ctx, store := t.Context(), openSQLVersioned(t, dialect, keeping(3, clk)).store
		must(t, store.Put(ctx, account{ID: "acc_1", Name: "then"}))
		clk.Set(far)
		must(t, store.Put(ctx, account{ID: "acc_1", Name: "later"}))
		all := sqlVersionsOf(t, store, "acc_1")
		if len(all) != 2 || !all[0].At.Equal(far) || !all[1].At.Equal(ancient) {
			t.Fatalf("the versions were made at %v and %v, want %v and %v", all[0].At, all[1].At, far, ancient)
		}
	})
}
