// Package docstore_test — the SQL store inside its caller's transaction:
// joined, each write a savepoint, its hooks held until the commit; and the
// failures a database can answer, raced collisions among them.
package docstore_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/docstore"
)

// errWork is the failure a test's unit of work returns to roll it back.
var errWork = errors.New("the unit of work failed")

// TestSQLJoinsTheCallersTransaction pins the reason the store takes a
// context: a call made under the caller's transaction runs IN it — its write
// is a savepoint of it, read back through the same context before the commit,
// invisible to the pool until the commit — and its hook waits for the commit.
func TestSQLJoinsTheCallersTransaction(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		fx := openSQL(t, dialect)
		var written []string
		fx.store.OnWrite(func(key string) { written = append(written, key) })
		err := fx.tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
			if insertErr := fx.store.Insert(ctx, account{ID: "acc_1", Email: "ada@x.dev"}); insertErr != nil {
				return insertErr
			}
			if a, getErr := fx.store.Get(ctx, "acc_1"); getErr != nil || a.ID != "acc_1" {
				t.Errorf("the transaction does not read its own write: %+v, %v", a, getErr)
			}
			if a, lookErr := fx.store.Lookup(ctx, "email", "ada@x.dev"); lookErr != nil || a.ID != "acc_1" {
				t.Errorf("the transaction does not read its own index row: %+v, %v", a, lookErr)
			}
			if _, getErr := fx.store.Get(t.Context(), "acc_1"); !errs.HasCode(getErr, docstore.CodeDocumentNotFound) {
				t.Errorf("the pool saw an uncommitted write: %v", getErr)
			}
			if len(written) != 0 {
				t.Errorf("a hook ran before the commit: %v", written)
			}
			return nil
		})
		must(t, err)
		if !slices.Equal(written, []string{"acc_1"}) {
			t.Fatalf("OnWrite saw %v after the commit, want acc_1 once", written)
		}
		if _, getErr := fx.store.Get(t.Context(), "acc_1"); getErr != nil {
			t.Fatalf("the committed write is not there: %v", getErr)
		}
		roles := fx.engine.roles()
		if begins := countOf(roles, "BEGIN"); begins != 1 {
			t.Fatalf("statements = %v, want ONE transaction — the store joined no second", roles)
		}
		if !slices.Contains(roles, "SAVEPOINT ktn_sp_1") || !slices.Contains(roles, "RELEASE SAVEPOINT ktn_sp_1") {
			t.Fatalf("statements = %v, want the write in a savepoint", roles)
		}
	})
}

// TestSQLARolledBackTransactionLeavesNothing pins the other half: every write
// the caller's transaction held is undone with it, and no hook ever runs.
func TestSQLARolledBackTransactionLeavesNothing(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		fx := openSQL(t, dialect)
		must(t, fx.store.Put(t.Context(), account{ID: "kept", Name: "before"}))
		var written, deleted []string
		fx.store.OnWrite(func(key string) { written = append(written, key) })
		fx.store.OnDelete(func(key string) { deleted = append(deleted, key) })
		err := fx.tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
			must(t, fx.store.Insert(ctx, account{ID: "acc_1", Email: "ada@x.dev"}))
			_, updErr := fx.store.Update(ctx, "kept", func(a *account) error { a.Name = "after"; return nil })
			must(t, updErr)
			must(t, fx.store.Delete(ctx, "kept"))
			return errWork
		})
		if !errors.Is(err, errWork) {
			t.Fatalf("Transact() = %v, want the unit of work's own error", err)
		}
		if a, getErr := fx.store.Get(t.Context(), "kept"); getErr != nil || a.Name != "before" {
			t.Fatalf("a rolled-back transaction changed a document: %+v, %v", a, getErr)
		}
		if _, getErr := fx.store.Get(t.Context(), "acc_1"); !errs.HasCode(getErr, docstore.CodeDocumentNotFound) {
			t.Fatalf("a rolled-back insertion is there: %v", getErr)
		}
		if len(written)+len(deleted) != 0 {
			t.Fatalf("hooks ran for a rolled-back transaction: %v %v", written, deleted)
		}
	})
}

// TestSQLARefusedWriteLeavesTheCallersTransactionUsable pins what the
// savepoint buys: a write the store refuses — DocumentExists, UniqueKeyTaken —
// is undone alone, even where the refusal was a statement the engine failed,
// and the caller may catch it and commit the rest.
func TestSQLARefusedWriteLeavesTheCallersTransactionUsable(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		fx := openSQL(t, dialect)
		var written []string
		fx.store.OnWrite(func(key string) { written = append(written, key) })
		err := fx.tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
			must(t, fx.store.Insert(ctx, account{ID: "acc_1", Email: "ada@x.dev"}))
			requireCode(t, fx.store.Insert(ctx, account{ID: "acc_1"}), docstore.CodeDocumentExists, "a taken key, caught")
			requireCode(t, fx.store.Insert(ctx, account{ID: "acc_2", Email: "ada@x.dev"}),
				docstore.CodeUniqueKeyTaken, "a taken unique key, caught")
			return fx.store.Put(ctx, account{ID: "acc_3"})
		})
		must(t, err)
		for key, want := range map[string]bool{"acc_1": true, "acc_2": false, "acc_3": true} {
			_, getErr := fx.store.Get(t.Context(), key)
			if (getErr == nil) != want {
				t.Fatalf("after the commit, %s present = %v, want %v", key, getErr == nil, want)
			}
		}
		if !slices.Equal(written, []string{"acc_1", "acc_3"}) {
			t.Fatalf("OnWrite saw %v, want the two writes that stood", written)
		}
	})
}

// TestSQLANestedFailureUndoesOnlyItsWrites pins a caller's own savepoint: a
// nested Transact that fails undoes the store's writes inside it, drops their
// hooks, and the outer transaction commits what it wrote itself.
func TestSQLANestedFailureUndoesOnlyItsWrites(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		fx := openSQL(t, dialect)
		var written []string
		fx.store.OnWrite(func(key string) { written = append(written, key) })
		err := fx.tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
			must(t, fx.store.Insert(ctx, account{ID: "outer"}))
			nested := fx.tm.Transact(ctx, coresql.TxOptionsValue{}, func(inner context.Context, _ coresql.Executor) error {
				must(t, fx.store.Insert(inner, account{ID: "inner"}))
				return errWork
			})
			if !errors.Is(nested, errWork) {
				t.Errorf("the nested Transact = %v", nested)
			}
			return nil
		})
		must(t, err)
		if _, getErr := fx.store.Get(t.Context(), "inner"); !errs.HasCode(getErr, docstore.CodeDocumentNotFound) {
			t.Fatalf("the failed savepoint's write is there: %v", getErr)
		}
		if !slices.Equal(written, []string{"outer"}) {
			t.Fatalf("OnWrite saw %v, want only the write that was committed", written)
		}
	})
}

// TestSQLARacedCollisionIsStillTheRefusal pins the collisions a check cannot
// see: another transaction commits the same unique key, or the same store
// key, between the store's check and its write. The write collides in the
// engine, rolls back, and the store asks the database what it collided with —
// in a transaction of its own, and inside the caller's — so the answer is the
// refusal, naming no key.
func TestSQLARacedCollisionIsStillTheRefusal(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		raceCollisions(t, dialect, false)
		raceCollisions(t, dialect, true)
	})
}

// raceCollisions races a unique key and a store key into the tables between
// the store's check and its write, in a transaction of the store's own or,
// joined, inside the caller's, and asserts both writes are still refused as
// the store's own refusals.
func raceCollisions(t *testing.T, dialect coresql.Dialect, joined bool) {
	t.Helper()
	fx := openSQL(t, dialect)
	fx.engine.beforeNext("insertIndexRows", func() {
		fx.engine.commitElsewhere(func(tb *tables) {
			tb.docs["rival"] = docRow{doc: []byte(`{"id":"rival"}`), rev: 1}
			tb.ix[ixKey{name: "email", key: "ada@x.dev", doc: "rival"}] = true
		})
	})
	fx.engine.beforeNext("insert", func() {
		fx.engine.commitElsewhere(func(tb *tables) {
			tb.docs["acc_2"] = docRow{doc: []byte(`{"id":"acc_2"}`), rev: 1}
		})
	})
	var uniqueErr, existsErr error
	write := func(ctx context.Context) {
		uniqueErr = fx.store.Put(ctx, account{ID: "acc_1", Email: "ada@x.dev"})
		existsErr = fx.store.Insert(ctx, account{ID: "acc_2"})
	}
	if joined {
		must(t, fx.tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
			write(ctx)
			return nil
		}))
	} else {
		write(t.Context())
	}
	requireCode(t, uniqueErr, docstore.CodeUniqueKeyTaken, "a unique key raced in")
	quotesNothing(t, uniqueErr, "ada@x.dev")
	requireCode(t, existsErr, docstore.CodeDocumentExists, "a store key raced in")
	if _, getErr := fx.store.Get(t.Context(), "acc_1"); !errs.HasCode(getErr, docstore.CodeDocumentNotFound) {
		t.Fatalf("joined=%v: the collided write left its document: %v", joined, getErr)
	}
}

// TestSQLAFailedStatementWithholdsTheDriversText pins STATEMENT_FAILED: the
// verdict a caller maps to 503, the driver's error reachable through
// errors.As and errors.Is — a context's end included — and its text, which
// quotes the row a constraint refused, in no rendering.
func TestSQLAFailedStatementWithholdsTheDriversText(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		fx := openSQL(t, dialect)
		must(t, fx.store.Put(t.Context(), account{ID: "acc_1", Email: "ada@x.dev"}))
		driverErr := errDuplicate{key: "ada@x.dev"}
		for role, call := range map[string]func() error{
			"getDoc": func() error { _, err := fx.store.Get(t.Context(), "acc_1"); return err },
			"lookup": func() error { _, err := fx.store.Lookup(t.Context(), "email", "ada@x.dev"); return err },
			"upsert": func() error { return fx.store.Put(t.Context(), account{ID: "acc_1", Email: "ada@x.dev"}) },
		} {
			fx.engine.failNext(role, driverErr)
			err := call()
			requireCode(t, err, docstore.CodeStatementFailed, role)
			quotesNothing(t, err, "ada@x.dev")
			if dup, reachable := errors.AsType[errDuplicate](err); !reachable || dup.key != "ada@x.dev" {
				t.Fatalf("%s: the driver's own error is not reachable through errors.As", role)
			}
			if status := errs.HTTPStatusOf(err); status != http.StatusServiceUnavailable {
				t.Fatalf("%s: HTTP status %d, want 503", role, status)
			}
		}
		cancelled, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := fx.store.Get(cancelled, "acc_1")
		requireCode(t, err, docstore.CodeStatementFailed, "a cancelled read")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("a cancelled read = %v, want errors.Is(context.Canceled)", err)
		}
	})
}

// TestSQLRoundTripsPerCall pins what each call sends, which is what it costs:
// one statement for a read, one for a write to a store without indexes outside
// a transaction, a transaction of the store's own — READ COMMITTED on MySQL —
// for a write that files index rows, and a savepoint of the caller's for any
// write inside one.
func TestSQLRoundTripsPerCall(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		begin := "BEGIN"
		if dialect == coresql.DialectMySQL {
			begin = "BEGIN READ COMMITTED"
		}
		fx := openSQL(t, dialect)
		bare := openSQL(t, dialect, []docstore.IndexSpec[account]{}...)
		for name, tc := range map[string]struct {
			call func(ctx context.Context) error
			fx   *sqlFixture
			want []string
		}{
			"Get": {
				call: func(ctx context.Context) error { _, err := fx.store.Get(ctx, "acc_1"); return err },
				fx:   fx, want: []string{"getDoc"},
			},
			"Put without indexes": {
				call: func(ctx context.Context) error { return bare.store.Put(ctx, account{ID: "acc_1"}) },
				fx:   bare, want: []string{"upsert"},
			},
			"Put of a new document": {
				call: func(ctx context.Context) error { return fx.store.Put(ctx, account{ID: "acc_2", Email: "b@x.dev"}) },
				fx:   fx, want: []string{begin, "upsert", "uniqueTaken", "insertIndexRows", "COMMIT"},
			},
			"Put over a stored document": {
				call: func(ctx context.Context) error { return fx.store.Put(ctx, account{ID: "acc_1", Email: "a@x.dev"}) },
				fx:   fx, want: []string{begin, "upsert", "uniqueTaken", "deleteIndex", "insertIndexRows", "COMMIT"},
			},
			"Update": {
				call: func(ctx context.Context) error {
					_, err := fx.store.Update(ctx, "acc_1", func(*account) error { return nil })
					return err
				},
				fx: fx, want: []string{begin, "lockDoc", "writeLocked", "uniqueTaken", "deleteIndex", "insertIndexRows", "COMMIT"},
			},
		} {
			must(t, fx.store.Put(t.Context(), account{ID: "acc_1", Email: "a@x.dev"}))
			tc.fx.engine.resetLog()
			must(t, tc.call(t.Context()))
			if got := tc.fx.engine.roles(); !slices.Equal(got, tc.want) {
				t.Fatalf("%s sent %v, want %v", name, got, tc.want)
			}
		}
		fx.engine.resetLog()
		must(t, fx.tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
			return fx.store.Put(ctx, account{ID: "acc_1", Email: "a@x.dev"})
		}))
		want := []string{
			"BEGIN", "SAVEPOINT ktn_sp_1", "upsert", "uniqueTaken", "deleteIndex", "insertIndexRows",
			"RELEASE SAVEPOINT ktn_sp_1", "COMMIT",
		}
		if got := fx.engine.roles(); !slices.Equal(got, want) {
			t.Fatalf("a write inside the caller's transaction sent %v, want %v", got, want)
		}
		bare.engine.resetLog()
		must(t, bare.tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
			return bare.store.Put(ctx, account{ID: "acc_1"})
		}))
		want = []string{"BEGIN", "SAVEPOINT ktn_sp_1", "upsert", "RELEASE SAVEPOINT ktn_sp_1", "COMMIT"}
		if got := bare.engine.roles(); !slices.Equal(got, want) {
			t.Fatalf("a one-statement write inside the caller's transaction sent %v, want %v", got, want)
		}
	})
}

// TestSQLAFailedWriteLeavesTheCallersTransactionUsable pins what the savepoint
// buys even a write of one statement: a statement the database fails — here,
// on PostgreSQL, one that leaves the transaction aborted — is undone alone, and
// the caller who catches it goes on and commits the rest.
func TestSQLAFailedWriteLeavesTheCallersTransactionUsable(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		fx := openSQL(t, dialect, []docstore.IndexSpec[account]{}...)
		fx.engine.failNext("upsert", errors.New("the statement was cancelled"))
		err := fx.tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
			requireCode(t, fx.store.Put(ctx, account{ID: "failed"}), docstore.CodeStatementFailed, "a failed statement, caught")
			return fx.store.Put(ctx, account{ID: "kept"})
		})
		must(t, err)
		if _, getErr := fx.store.Get(t.Context(), "kept"); getErr != nil {
			t.Fatalf("the write after the caught failure was not committed: %v", getErr)
		}
		if _, getErr := fx.store.Get(t.Context(), "failed"); !errs.HasCode(getErr, docstore.CodeDocumentNotFound) {
			t.Fatalf("the failed write is there: %v", getErr)
		}
	})
}

// countOf counts the occurrences of role in roles.
func countOf(roles []string, role string) int {
	n := 0
	for _, r := range roles {
		if r == role {
			n++
		}
	}
	return n
}
