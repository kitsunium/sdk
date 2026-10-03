// Package docstore_test — the SQL store's contract, on each dialect's
// statements, over the fake engine: the write modes, the reads, the indexes,
// the keys, Update, and the refusals, all naming no key.
package docstore_test

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

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/docstore"
	svcsql "github.com/kitsunium/sdk/internal/service/data/sql"
)

// accountsTable is the table every SQL case keeps its accounts in.
const accountsTable = "members__accounts"

// sqlDialects are the three engines every case runs on.
var sqlDialects = []coresql.Dialect{coresql.DialectPostgres, coresql.DialectMySQL, coresql.DialectSQLite}

// sqlFixture is one dialect's accounts store over a fresh fake engine, with
// the transactor it runs on.
type sqlFixture struct {
	engine *sqlEngine
	tm     coresql.Transactor
	store  *docstore.SQLStore[account]
}

// openSQL opens an accounts store on dialect over a fresh engine, with the
// given indexes — the accounts indexes when none are given.
func openSQL(t *testing.T, dialect coresql.Dialect, indexes ...coredocstore.IndexSpec[account]) *sqlFixture {
	t.Helper()
	engine := newSQLEngine(dialect, accountsTable)
	tm := newSQLTransactor(t, engine, dialect)
	if indexes == nil {
		indexes = accountIndexes()
	}
	store, err := docstore.OpenSQL(docstore.SQLConfig[account]{
		Key: accountKey, Transactor: tm, Dialect: dialect, Table: accountsTable,
	}, indexes...)
	if err != nil {
		t.Fatalf("OpenSQL() = %v", err)
	}
	return &sqlFixture{engine: engine, tm: tm, store: store}
}

// newSQLTransactor returns the SDK's transactor over a pool onto engine.
func newSQLTransactor(t *testing.T, engine *sqlEngine, dialect coresql.Dialect) coresql.Transactor {
	t.Helper()
	tm, err := svcsql.NewTransactor(svcsql.Config{
		DB: engine.open(t), Dialect: dialect, Pool: svcsql.PoolConfig{MaxOpen: 8},
	})
	if err != nil {
		t.Fatalf("NewTransactor() = %v", err)
	}
	return tm
}

// eachDialect runs fn once per dialect, in parallel.
func eachDialect(t *testing.T, fn func(t *testing.T, dialect coresql.Dialect)) {
	t.Helper()
	for _, dialect := range sqlDialects {
		t.Run(dialect.String(), func(t *testing.T) {
			t.Parallel()
			fn(t, dialect)
		})
	}
}

// quotesNothing fails the test when err's text or fields carry one of the
// secrets — a key, an index key — anywhere.
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

// TestSQLTheWriteModes pins the three write modes and Delete on every dialect:
// Insert refuses a taken key, Replace a missing one — never bringing a deleted
// document back — and Put does both.
func TestSQLTheWriteModes(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		ctx, store := t.Context(), openSQL(t, dialect).store
		must(t, store.Insert(ctx, account{ID: "acc_1", Name: "Ada"}))
		err := store.Insert(ctx, account{ID: "acc_1", Name: "Again"})
		requireCode(t, err, coredocstore.CodeDocumentExists, "a second Insert")
		quotesNothing(t, err, "acc_1")
		requireCode(t, store.Replace(ctx, account{ID: "acc_2"}), coredocstore.CodeDocumentNotFound, "Replace of a missing key")
		must(t, store.Put(ctx, account{ID: "acc_2", Name: "Grace"}))
		must(t, store.Put(ctx, account{ID: "acc_2", Name: "Grace Hopper"}))
		if got, getErr := store.Get(ctx, "acc_2"); getErr != nil || got.Name != "Grace Hopper" {
			t.Fatalf("Get() after two Puts = %+v, %v", got, getErr)
		}
		must(t, store.Delete(ctx, "acc_2"))
		requireCode(t, store.Replace(ctx, account{ID: "acc_2"}), coredocstore.CodeDocumentNotFound, "Replace after a Delete")
		requireCode(t, store.Delete(ctx, "acc_2"), coredocstore.CodeDocumentNotFound, "a second Delete")
		if _, getErr := store.Get(ctx, "acc_2"); !errs.HasCode(getErr, coredocstore.CodeDocumentNotFound) {
			t.Fatalf("Get() of a deleted document = %v", getErr)
		}
		if got, getErr := store.Get(ctx, "acc_1"); getErr != nil || got.Name != "Ada" {
			t.Fatalf("a refused Insert changed the document: %+v, %v", got, getErr)
		}
	})
}

// TestSQLAKeyIsNeverEmptyNorTooLong pins the two refusals a write gets before
// any statement is sent: an empty key, and a key — store or index — longer
// than the key columns hold, which MySQL outside strict mode would truncate
// into another key.
func TestSQLAKeyIsNeverEmptyNorTooLong(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		ctx, fx := t.Context(), openSQL(t, dialect)
		long := strings.Repeat("k", docstore.MaxSQLKeyLen+1)
		requireCode(t, fx.store.Put(ctx, account{ID: ""}), coredocstore.CodeDocumentKeyEmpty, "an empty key")
		err := fx.store.Put(ctx, account{ID: long})
		requireCode(t, err, coredocstore.CodeKeyTooLong, "a store key past the limit")
		quotesNothing(t, err, long)
		err = fx.store.Put(ctx, account{ID: "acc_1", Email: long})
		requireCode(t, err, coredocstore.CodeKeyTooLong, "an index key past the limit")
		if index := fieldValue(errs.FieldsOf(err), "index"); index != "email" {
			t.Fatalf("the refusal names index %q, want email", index)
		}
		if roles := fx.engine.roles(); len(roles) != 0 {
			t.Fatalf("a refused key reached the database: %v", roles)
		}
		must(t, fx.store.Put(ctx, account{ID: strings.Repeat("k", docstore.MaxSQLKeyLen)}))
	})
}

// TestSQLReadsAreInKeyOrderAndKeysAreBytes pins List, Entries, Count and
// Filter, and that keys differing by case, by a trailing space or by an
// accent are different keys, ordered as Go orders strings.
func TestSQLReadsAreInKeyOrderAndKeysAreBytes(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		ctx, store := t.Context(), openSQL(t, dialect).store
		keys := []string{"b", "a", "A", "a ", "é", "e"}
		for _, key := range keys {
			must(t, store.Insert(ctx, account{ID: key, Name: "name of " + key}))
		}
		want := slices.Sorted(slices.Values(keys))
		all, err := store.List(ctx)
		must(t, err)
		got := make([]string, len(all))
		for i, a := range all {
			got[i] = a.ID
		}
		if !slices.Equal(got, want) {
			t.Fatalf("List() order = %q, want %q", got, want)
		}
		for _, key := range keys {
			if a, getErr := store.Get(ctx, key); getErr != nil || a.Name != "name of "+key {
				t.Fatalf("Get(%q) = %+v, %v — a key was folded into another", key, a, getErr)
			}
		}
		entries, err := store.Entries(ctx, 2)
		must(t, err)
		if len(entries) != 2 || entries[0].Key != want[0] || entries[1].Key != want[1] {
			t.Fatalf("Entries(2) = %+v", entries)
		}
		written, err := json.Marshal(account{ID: want[0], Name: "name of " + want[0]})
		must(t, err)
		if string(entries[0].JSON) != string(written) {
			t.Fatalf("Entries() JSON = %s, want the bytes written: %s", entries[0].JSON, written)
		}
		if n, countErr := store.Count(ctx); countErr != nil || n != len(keys) {
			t.Fatalf("Count() = %d, %v", n, countErr)
		}
		lower, err := store.Filter(ctx, func(a account) bool { return a.ID == strings.ToLower(a.ID) })
		must(t, err)
		if len(lower) != len(keys)-1 {
			t.Fatalf("Filter() kept %d, want %d", len(lower), len(keys)-1)
		}
	})
}

// TestSQLUpdate pins the read-modify-write: the stored result is returned; an
// error from the function changes nothing and errors.Is finds it; a result
// under another key is refused; the document is read under a lock.
func TestSQLUpdate(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		ctx, fx := t.Context(), openSQL(t, dialect)
		must(t, fx.store.Put(ctx, account{ID: "acc_1", Name: "Ada", Email: "ada@x.dev"}))
		must(t, fx.store.Put(ctx, account{ID: "acc_2", Name: "Grace", Email: "grace@x.dev"}))
		fx.engine.resetLog()
		updated, err := fx.store.Update(ctx, "acc_1", func(a *account) error {
			other, readErr := fx.store.Get(ctx, "acc_2")
			if readErr != nil {
				return readErr
			}
			a.Name = "Ada, after " + other.Name
			return nil
		})
		if err != nil || updated.Name != "Ada, after Grace" {
			t.Fatalf("Update() = %+v, %v", updated, err)
		}
		roles := fx.engine.roles()
		if lock, write := slices.Index(roles, "lockDoc"), slices.Index(roles, "writeLocked"); lock < 0 || write < lock {
			t.Fatalf("statements = %v, want the locking read before the write", roles)
		}
		refusal := errors.New("the caller's own refusal")
		_, err = fx.store.Update(ctx, "acc_1", func(a *account) error { a.Name = "Lost"; return refusal })
		if !errors.Is(err, refusal) {
			t.Fatalf("Update() with a failing function = %v, want the function's own error", err)
		}
		_, err = fx.store.Update(ctx, "acc_1", func(a *account) error { a.ID = "acc_9"; return nil })
		requireCode(t, err, coredocstore.CodeDocumentKeyChanged, "an update that renames")
		_, err = fx.store.Update(ctx, "acc_404", func(*account) error { return nil })
		requireCode(t, err, coredocstore.CodeDocumentNotFound, "an update of a missing key")
		if got, getErr := fx.store.Get(ctx, "acc_1"); getErr != nil || got.Name != "Ada, after Grace" {
			t.Fatalf("a refused update changed the document: %+v, %v", got, getErr)
		}
		if _, getErr := fx.store.Get(ctx, "acc_9"); !errs.HasCode(getErr, coredocstore.CodeDocumentNotFound) {
			t.Fatalf("a refused rename stored the new key: %v", getErr)
		}
	})
}

// TestSQLUniqueIndex pins a unique index: a second holder is refused naming
// the index and never the key; a document keeps its own key through a
// rewrite; a key freed by an update is free; Lookup reads unique indexes only.
func TestSQLUniqueIndex(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		ctx, store := t.Context(), openSQL(t, dialect).store
		must(t, store.Insert(ctx, account{ID: "acc_1", Email: "ada@x.dev"}))
		err := store.Insert(ctx, account{ID: "acc_2", Email: "ada@x.dev"})
		requireCode(t, err, coredocstore.CodeUniqueKeyTaken, "a second holder of a unique key")
		quotesNothing(t, err, "ada@x.dev", "acc_2")
		if index := fieldValue(errs.FieldsOf(err), "index"); index != "email" {
			t.Fatalf("the refusal names index %q, want email", index)
		}
		if _, getErr := store.Get(ctx, "acc_2"); !errs.HasCode(getErr, coredocstore.CodeDocumentNotFound) {
			t.Fatalf("a refused write stored its document: %v", getErr)
		}
		must(t, store.Put(ctx, account{ID: "acc_1", Email: "ada@x.dev", Name: "rewritten"}))
		if _, updErr := store.Update(ctx, "acc_1", func(a *account) error { a.Email = "lovelace@x.dev"; return nil }); updErr != nil {
			t.Fatalf("Update() = %v", updErr)
		}
		must(t, store.Insert(ctx, account{ID: "acc_2", Email: "ada@x.dev"}))
		if a, lookErr := store.Lookup(ctx, "email", "lovelace@x.dev"); lookErr != nil || a.ID != "acc_1" {
			t.Fatalf("Lookup() = %+v, %v", a, lookErr)
		}
		_, err = store.Lookup(ctx, "email", "nobody@x.dev")
		requireCode(t, err, coredocstore.CodeDocumentNotFound, "a Lookup nobody answers")
		quotesNothing(t, err, "nobody@x.dev")
		_, err = store.Lookup(ctx, "email", "")
		requireCode(t, err, coredocstore.CodeDocumentNotFound, "a Lookup of the empty key")
		_, err = store.Lookup(ctx, "team", "red")
		requireCode(t, err, coredocstore.CodeIndexNotUnique, "a Lookup in a multi-valued index")
		_, err = store.Lookup(ctx, "nope", "x")
		requireCode(t, err, coredocstore.CodeIndexUnknown, "a Lookup in an undeclared index")
	})
}

// TestSQLFindAndFilter pins a multi-valued index: Find in store-key order, an
// empty slice for nobody, an empty key that is no key, a repeated key filed
// once, and the rows of what a document was taken out when it changes.
func TestSQLFindAndFilter(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		ctx, fx := t.Context(), openSQL(t, dialect)
		must(t, fx.store.Put(ctx, account{ID: "acc_2", Teams: []string{"red", "red", ""}}))
		must(t, fx.store.Put(ctx, account{ID: "acc_1", Teams: []string{"red", "blue"}}))
		red, err := fx.store.Find(ctx, "team", "red")
		must(t, err)
		if len(red) != 2 || red[0].ID != "acc_1" || red[1].ID != "acc_2" {
			t.Fatalf("Find(red) = %+v, want acc_1 then acc_2", red)
		}
		rows := 0
		for k := range fx.engine.snapshot().ix {
			if k.doc == "acc_2" {
				rows++
			}
		}
		if rows != 1 {
			t.Fatalf("acc_2 is filed under %d rows, want its one non-empty key once", rows)
		}
		must(t, fx.store.Put(ctx, account{ID: "acc_2", Teams: []string{"green"}}))
		if red, err = fx.store.Find(ctx, "team", "red"); err != nil || len(red) != 1 {
			t.Fatalf("Find(red) after a rewrite = %+v, %v", red, err)
		}
		for _, key := range []string{"nobody", ""} {
			none, findErr := fx.store.Find(ctx, "team", key)
			if findErr != nil || none == nil || len(none) != 0 {
				t.Fatalf("Find(%q) = %#v, %v, want an empty slice", key, none, findErr)
			}
		}
		_, err = fx.store.Find(ctx, "nope", "x")
		requireCode(t, err, coredocstore.CodeIndexUnknown, "a Find in an undeclared index")
	})
}

// TestSQLManyUniqueKeysAreCheckedInBatches pins a unique index that answers
// many keys for one document: its keys are checked a bounded batch at a time,
// under the engines' ceiling on bound parameters, and a key taken in a later
// batch is still refused, naming the index.
func TestSQLManyUniqueKeysAreCheckedInBatches(t *testing.T) {
	t.Parallel()
	//: 449 keys of the document's own, and a last one it may share.
	aliases := func(a account) []string {
		keys := make([]string, 0, 450)
		for i := range 449 {
			keys = append(keys, fmt.Sprintf("alias-%s-%03d", a.ID, i))
		}
		return append(keys, "shared-"+a.Name)
	}
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		ctx := t.Context()
		fx := openSQL(t, dialect, coredocstore.IndexSpec[account]{Name: "aliases", Unique: true, Keys: aliases})
		must(t, fx.store.Put(ctx, account{ID: "acc_1", Name: "ada"}))
		if checks := countOf(fx.engine.roles(), "uniqueTaken"); checks != 2 {
			t.Fatalf("450 unique keys took %d checks, want 2 bounded batches", checks)
		}
		must(t, fx.store.Put(ctx, account{ID: "acc_3", Name: "grace"}))
		err := fx.store.Put(ctx, account{ID: "acc_2", Name: "ada"})
		requireCode(t, err, coredocstore.CodeUniqueKeyTaken, "a key taken in the last batch")
		if index := fieldValue(errs.FieldsOf(err), "index"); index != "aliases" {
			t.Fatalf("the refusal names index %q, want aliases", index)
		}
	})
}

// TestSQLIndexKeysMayBeStoredHashed pins IndexKey: the table holds what it
// returns and never the key, and Lookup, Find, a rewrite and a deletion all go
// through it.
func TestSQLIndexKeysMayBeStoredHashed(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		ctx := t.Context()
		engine := newSQLEngine(dialect, accountsTable)
		store, err := docstore.OpenSQL(docstore.SQLConfig[account]{
			Key: accountKey, Transactor: newSQLTransactor(t, engine, dialect), Dialect: dialect, Table: accountsTable,
			IndexKey: func(index, key string) []byte {
				sum := sha256.Sum256([]byte(index + "\x00" + key))
				return sum[:]
			},
		}, accountIndexes()...)
		must(t, err)
		must(t, store.Put(ctx, account{ID: "acc_1", Email: "ada@x.dev", Teams: []string{"red"}}))
		for k := range engine.snapshot().ix {
			if k.key == "ada@x.dev" || k.key == "red" {
				t.Fatalf("an index key reached the table in clear: %q", k.key)
			}
		}
		if a, lookErr := store.Lookup(ctx, "email", "ada@x.dev"); lookErr != nil || a.ID != "acc_1" {
			t.Fatalf("Lookup() through the hash = %+v, %v", a, lookErr)
		}
		if red, findErr := store.Find(ctx, "team", "red"); findErr != nil || len(red) != 1 {
			t.Fatalf("Find() through the hash = %+v, %v", red, findErr)
		}
		requireCode(t, store.Insert(ctx, account{ID: "acc_2", Email: "ada@x.dev"}), coredocstore.CodeUniqueKeyTaken, "a hashed unique key")
		must(t, store.Delete(ctx, "acc_1"))
		if n := len(engine.snapshot().ix); n != 0 {
			t.Fatalf("%d index rows left after the only document went", n)
		}
	})
}

// TestSQLHooksAfterTheWriteStood pins the announcements outside a caller's
// transaction: after the write committed — a hook reading the store finds it
// — and never for a refused write.
func TestSQLHooksAfterTheWriteStood(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		ctx, store := t.Context(), openSQL(t, dialect).store
		var written, deleted []string
		var seen []bool
		store.OnWrite(func(key string) {
			written = append(written, key)
			_, err := store.Get(context.Background(), key)
			seen = append(seen, err == nil)
		})
		removeDelete := store.OnDelete(func(key string) { deleted = append(deleted, key) })
		must(t, store.Insert(ctx, account{ID: "acc_1", Email: "ada@x.dev"}))
		requireCode(t, store.Insert(ctx, account{ID: "acc_2", Email: "ada@x.dev"}), coredocstore.CodeUniqueKeyTaken, "a refused write")
		_, err := store.Update(ctx, "acc_1", func(a *account) error { a.Name = "Ada"; return nil })
		must(t, err)
		must(t, store.Delete(ctx, "acc_1"))
		removeDelete()
		must(t, store.Put(ctx, account{ID: "acc_3"}))
		must(t, store.Delete(ctx, "acc_3"))
		if !slices.Equal(written, []string{"acc_1", "acc_1", "acc_3"}) || !slices.Equal(deleted, []string{"acc_1"}) {
			t.Fatalf("OnWrite saw %v and OnDelete %v", written, deleted)
		}
		if slices.Contains(seen, false) {
			t.Fatalf("a hook ran before its write was committed: %v", seen)
		}
	})
}

// TestSQLConfigurationRefusals pins every configuration OpenSQL and
// SQLMigration refuse, before any statement: each is STORE_MISCONFIGURED
// naming the setting.
func TestSQLConfigurationRefusals(t *testing.T) {
	t.Parallel()
	engine := newSQLEngine(coresql.DialectSQLite, accountsTable)
	tm := newSQLTransactor(t, engine, coresql.DialectSQLite)
	valid := docstore.SQLConfig[account]{Key: accountKey, Transactor: tm, Dialect: coresql.DialectSQLite, Table: accountsTable}
	//: the parallel subtests run once this function has returned, so what
	//: they sent is read in a cleanup, which runs after all of them.
	t.Cleanup(func() {
		if roles := engine.roles(); len(roles) != 0 {
			t.Errorf("a refused configuration reached the database: %v", roles)
		}
	})
	for name, tc := range map[string]struct {
		mutate  func(*docstore.SQLConfig[account])
		indexes []coredocstore.IndexSpec[account]
		setting string
	}{
		"no key function":               {mutate: func(c *docstore.SQLConfig[account]) { c.Key = nil }, setting: "Key"},
		"no transactor":                 {mutate: func(c *docstore.SQLConfig[account]) { c.Transactor = nil }, setting: "Transactor"},
		"a transactor that cannot join": {mutate: func(c *docstore.SQLConfig[account]) { c.Transactor = bareTransactor{tm} }, setting: "Transactor"},
		"no dialect":                    {mutate: func(c *docstore.SQLConfig[account]) { c.Dialect = coresql.DialectUnknown }, setting: "Dialect"},
		"an upper-case table":           {mutate: func(c *docstore.SQLConfig[account]) { c.Table = "Members" }, setting: "Table"},
		"an injected table":             {mutate: func(c *docstore.SQLConfig[account]) { c.Table = "t; DROP TABLE users" }, setting: "Table"},
		"a table past the limit":        {mutate: func(c *docstore.SQLConfig[account]) { c.Table = strings.Repeat("t", docstore.MaxSQLTableLen+1) }, setting: "Table"},
		"a derived table's separator":   {mutate: func(c *docstore.SQLConfig[account]) { c.Table = "members___ix" }, setting: "Table"},
		"a name SQLite reserves":        {mutate: func(c *docstore.SQLConfig[account]) { c.Table = "sqlite_accounts" }, setting: "Table"},
		"an index name past the limit": {
			indexes: []coredocstore.IndexSpec[account]{coredocstore.Unique(strings.Repeat("i", docstore.MaxSQLIndexNameLen+1), func(a account) string { return a.Email })},
			setting: "Indexes",
		},
		"an index declared twice": {
			indexes: []coredocstore.IndexSpec[account]{
				coredocstore.Unique("email", func(a account) string { return a.Email }),
				coredocstore.Unique("email", func(a account) string { return a.Name }),
			},
			setting: "Indexes",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := valid
			if tc.mutate != nil {
				tc.mutate(&cfg)
			}
			_, err := docstore.OpenSQL(cfg, tc.indexes...)
			requireCode(t, err, coredocstore.CodeStoreMisconfigured, name)
			if setting := fieldValue(errs.FieldsOf(err), "setting"); setting != tc.setting {
				t.Fatalf("the refusal names setting %q, want %q", setting, tc.setting)
			}
		})
	}
	must(t, openErr(docstore.OpenSQL(docstore.SQLConfig[account]{
		Key: accountKey, Transactor: tm, Dialect: coresql.DialectSQLite, Table: strings.Repeat("t", docstore.MaxSQLTableLen),
	})))
}

// openErr drops the store OpenSQL built and keeps its verdict.
func openErr[T any](_ *docstore.SQLStore[T], err error) error { return err }

// bareTransactor is a transactor that is ONLY a Transactor: a wrapper that
// forgot the siblings, which the store must refuse rather than run beside.
type bareTransactor struct{ inner coresql.Transactor }

// Transact delegates.
func (b bareTransactor) Transact(ctx context.Context, opts coresql.TxOptionsValue, fn coresql.TxFunc) error {
	return b.inner.Transact(ctx, opts, fn)
}

// TestSQLConcurrentWriters pins the two contention cases on every dialect:
// thirty-two writers inserting one unique key, exactly one winning, and
// thirty-two Updates of one document, none lost.
func TestSQLConcurrentWriters(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		ctx, store := t.Context(), openSQL(t, dialect).store
		var wg sync.WaitGroup
		var mu sync.Mutex
		wins, conflicts := 0, 0
		for i := range 32 {
			wg.Go(func() {
				err := store.Insert(ctx, account{ID: fmt.Sprintf("acc_%02d", i), Email: "same@x.dev"})
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					wins++
				case errs.HasCode(err, coredocstore.CodeUniqueKeyTaken):
					conflicts++
				default:
					t.Errorf("unexpected %v", err)
				}
			})
		}
		wg.Wait()
		if wins != 1 || conflicts != 31 {
			t.Fatalf("%d writers won and %d conflicted, want 1 and 31", wins, conflicts)
		}
		must(t, store.Put(ctx, account{ID: "counter", Name: "0"}))
		for range 32 {
			wg.Go(func() {
				_, err := store.Update(ctx, "counter", func(a *account) error {
					a.Name = fmt.Sprint(len(a.Name) + 1)
					a.Teams = append(a.Teams, "x")
					return nil
				})
				if err != nil {
					t.Errorf("Update() = %v", err)
				}
			})
		}
		wg.Wait()
		if got, err := store.Get(ctx, "counter"); err != nil || len(got.Teams) != 32 {
			t.Fatalf("after 32 Updates the document holds %d, want 32: %v", len(got.Teams), err)
		}
	})
}

// fieldValue returns the string value of an error field, or "".
func fieldValue(fields []errs.FieldValue, key string) string {
	for _, field := range fields {
		if field.Key() == key {
			return field.StringValue()
		}
	}
	return ""
}
