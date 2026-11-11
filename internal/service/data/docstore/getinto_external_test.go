package docstore_test

import (
	"reflect"
	"testing"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
)

// staleAccount is a value a caller decoded an earlier document into: every
// field set, and a Teams slice whose backing array the caller still holds.
func staleAccount() (account, []string) {
	teams := []string{"old-a", "old-b", "old-c"}
	//: the slice dst holds, and the same backing array kept by the caller.
	return account{ID: "acc_old", Email: "old@x.dev", Name: "Old", Teams: teams}, teams
}

// TestGetIntoIsGetInTheCallersValue pins #813's GetInto on the file engine:
// the value it leaves equals what Get returns, no field of what dst held
// survives (Email is omitted from the stored JSON), the slice dst held is not
// written through, and a miss or a closed store leaves dst zero.
func TestGetIntoIsGetInTheCallersValue(t *testing.T) {
	t.Parallel()
	store := openAccounts(t, nil)
	must(t, store.Put(account{ID: "acc_1", Name: "Ada", Teams: []string{"red"}}))
	want, err := store.Get("acc_1")
	must(t, err)
	dst, held := staleAccount()
	must(t, store.GetInto("acc_1", &dst))
	if !reflect.DeepEqual(dst, want) {
		t.Fatalf("GetInto = %+v, want what Get returns, %+v", dst, want)
	}
	if held[0] != "old-a" {
		t.Fatalf("GetInto wrote through the slice dst held: %q", held)
	}
	dst.Teams[0] = "changed"
	if again, _ := store.Get("acc_1"); again.Teams[0] != "red" {
		t.Fatalf("a change to GetInto's value reached the store: %+v", again)
	}
	dst, _ = staleAccount()
	requireCode(t, store.GetInto("acc_9", &dst), coredocstore.CodeDocumentNotFound, "GetInto of a missing key")
	if !reflect.DeepEqual(dst, account{}) {
		t.Fatalf("GetInto after a miss left %+v, want the zero value", dst)
	}
	var _ coredocstore.IntoGetter[account] = store
	must(t, store.Close())
	dst, _ = staleAccount()
	requireCode(t, store.GetInto("acc_1", &dst), coredocstore.CodeStoreClosed, "GetInto after Close")
	if !reflect.DeepEqual(dst, account{}) {
		t.Fatalf("GetInto after Close left %+v, want the zero value", dst)
	}
}

// TestSQLGetIntoIsGetInTheCallersValue pins GetInto on the SQL engine, every
// dialect: equal to Get, nothing of what dst held kept, zero on a miss.
func TestSQLGetIntoIsGetInTheCallersValue(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		ctx, store := t.Context(), openSQL(t, dialect).store
		must(t, store.Put(ctx, account{ID: "acc_1", Name: "Ada", Teams: []string{"red"}}))
		want, err := store.Get(ctx, "acc_1")
		must(t, err)
		dst, held := staleAccount()
		must(t, store.GetInto(ctx, "acc_1", &dst))
		if !reflect.DeepEqual(dst, want) || held[0] != "old-a" {
			t.Fatalf("GetInto = %+v (held %q), want %+v and the held slice untouched", dst, held, want)
		}
		dst, _ = staleAccount()
		requireCode(t, store.GetInto(ctx, "acc_9", &dst), coredocstore.CodeDocumentNotFound, "GetInto of a missing key")
		if !reflect.DeepEqual(dst, account{}) {
			t.Fatalf("GetInto after a miss left %+v, want the zero value", dst)
		}
		var _ coredocstore.IntoGetterContext[account] = store
	})
}
