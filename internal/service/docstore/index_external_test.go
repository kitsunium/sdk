// Package docstore_test — the secondary indexes: kept with every write, read
// by Lookup, Find and Filter, rebuilt on open.
package docstore_test

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/docstore"
)

// TestUniqueIndex pins the unique index: a second holder of a key is refused
// without quoting the key, the refused write leaves the store and the index
// untouched, the index follows every rewrite and deletion, a document may
// rewrite itself with its own key, and an empty key is no key.
func TestUniqueIndex(t *testing.T) {
	t.Parallel()
	store := openAccounts(t, nil)
	must(t, store.Insert(account{ID: "acc_1", Email: "ada@example.com", Name: "Ada"}))
	err := store.Insert(account{ID: "acc_2", Email: "ada@example.com", Name: "Impostor"})
	requireCode(t, err, docstore.CodeUniqueKeyTaken, "a second holder of a unique key")
	if quotes(err.Error()+errs.PublicOf(err)+errs.PrivateOf(err)+fieldsText(err), "ada@example.com") {
		t.Errorf("the refusal quotes the index key: %v %s", err, fieldsText(err))
	}
	if _, getErr := store.Get("acc_2"); !errs.HasCode(getErr, docstore.CodeDocumentNotFound) {
		t.Fatalf("the refused insert was stored: %v", getErr)
	}
	got, err := store.Lookup("email", "ada@example.com")
	if err != nil || got.Name != "Ada" {
		t.Fatalf("Lookup() = %+v, %v", got, err)
	}

	//: a rewrite moves the document in the index.
	must(t, store.Put(account{ID: "acc_1", Email: "ada@lovelace.dev", Name: "Ada"}))
	_, oldKey := store.Lookup("email", "ada@example.com")
	requireCode(t, oldKey, docstore.CodeDocumentNotFound, "the old key after a rewrite")
	must(t, store.Insert(account{ID: "acc_2", Email: "ada@example.com", Name: "Second"}))

	//: an update into a key another document holds is refused, and changes
	//: nothing.
	_, err = store.Update("acc_2", func(a *account) error { a.Email = "ada@lovelace.dev"; a.Name = "Thief"; return nil })
	requireCode(t, err, docstore.CodeUniqueKeyTaken, "an update into a held key")
	if a, getErr := store.Get("acc_2"); getErr != nil || a.Name != "Second" || a.Email != "ada@example.com" {
		t.Fatalf("the refused update changed the document: %+v, %v", a, getErr)
	}
	//: re-writing a document with its own key is not a conflict.
	must(t, store.Put(account{ID: "acc_2", Email: "ada@example.com", Name: "Renamed"}))

	//: a deletion frees the key.
	must(t, store.Delete("acc_1"))
	_, freed := store.Lookup("email", "ada@lovelace.dev")
	requireCode(t, freed, docstore.CodeDocumentNotFound, "a deleted document's key")
	must(t, store.Insert(account{ID: "acc_3", Email: "ada@lovelace.dev"}))

	//: empty keys are not indexed: any number of documents may have none.
	must(t, store.Insert(account{ID: "acc_4"}))
	must(t, store.Insert(account{ID: "acc_5"}))
	_, empty := store.Lookup("email", "")
	requireCode(t, empty, docstore.CodeDocumentNotFound, "the empty key")
}

// TestFindAndFilter pins the multi-valued index and the full scan: Find
// orders by store key and files each document once whatever its function
// repeats, follows updates, answers an empty slice for a key nobody holds,
// and reads a unique index too; Lookup on a multi-valued index and any read
// of an undeclared one are refused.
func TestFindAndFilter(t *testing.T) {
	t.Parallel()
	store := openAccounts(t, nil)
	for _, a := range []account{
		{ID: "acc_c", Email: "c@x.dev", Teams: []string{"red", "blue"}},
		{ID: "acc_a", Email: "a@x.dev", Teams: []string{"red", "red"}},
		{ID: "acc_b", Email: "b@x.dev", Teams: []string{"blue"}},
	} {
		must(t, store.Put(a))
	}
	red, err := store.Find("team", "red")
	if err != nil || !slices.Equal(ids(red), []string{"acc_a", "acc_c"}) {
		t.Fatalf("Find(red) = %v, %v: ordered by store key, each document once", ids(red), err)
	}
	_, err = store.Update("acc_c", func(a *account) error { a.Teams = []string{"blue"}; return nil })
	must(t, err)
	if red, findErr := store.Find("team", "red"); findErr != nil || !slices.Equal(ids(red), []string{"acc_a"}) {
		t.Errorf("after an update, Find(red) = %v, %v", ids(red), findErr)
	}
	none, err := store.Find("team", "green")
	if err != nil || none == nil || len(none) != 0 {
		t.Errorf("Find of a key nobody holds = %v, %v, want an empty slice", none, err)
	}
	if one, err := store.Find("email", "b@x.dev"); err != nil || !slices.Equal(ids(one), []string{"acc_b"}) {
		t.Errorf("Find on a unique index = %v, %v", ids(one), err)
	}
	_, notUnique := store.Lookup("team", "blue")
	requireCode(t, notUnique, docstore.CodeIndexNotUnique, "Lookup on a multi-valued index")
	_, unknown := store.Find("nope", "x")
	requireCode(t, unknown, docstore.CodeIndexUnknown, "Find on an undeclared index")
	_, unknownLookup := store.Lookup("nope", "x")
	requireCode(t, unknownLookup, docstore.CodeIndexUnknown, "Lookup on an undeclared index")
	blue, err := store.Filter(func(a account) bool { return slices.Contains(a.Teams, "blue") })
	if err != nil || !slices.Equal(ids(blue), []string{"acc_b", "acc_c"}) {
		t.Errorf("Filter() = %v, %v", ids(blue), err)
	}
}

// TestUniqueIndexUnderContention pins the unique index against a race:
// thirty-two writers inserting one key at once, and exactly one wins.
func TestUniqueIndexUnderContention(t *testing.T) {
	t.Parallel()
	for _, backend := range []struct {
		name string
		open func(t *testing.T) *docstore.Store[account]
	}{
		{"memory", func(t *testing.T) *docstore.Store[account] { return openAccounts(t, nil) }},
		{"filesystem", func(t *testing.T) *docstore.Store[account] { return openAccounts(t, memFS()) }},
	} {
		t.Run(backend.name, func(t *testing.T) {
			t.Parallel()
			store := backend.open(t)
			var wg sync.WaitGroup
			var mu sync.Mutex
			wins, conflicts := 0, 0
			for i := range 32 {
				wg.Go(func() {
					err := store.Insert(account{ID: fmt.Sprintf("acc_%02d", i), Email: "same@x.dev"})
					mu.Lock()
					defer mu.Unlock()
					switch {
					case err == nil:
						wins++
					case errs.HasCode(err, docstore.CodeUniqueKeyTaken):
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
			if n := store.Stats().Documents; n != 1 {
				t.Fatalf("the store holds %d accounts", n)
			}
		})
	}
}

// TestIndexesAreRebuiltOnOpen pins the load: an index is rebuilt from the
// documents when the store opens, and documents that break a unique index
// refuse to open — without the shared key in the public text — rather than
// answer lies.
func TestIndexesAreRebuiltOnOpen(t *testing.T) {
	t.Parallel()
	fsys := memFS()
	first, err := openWith(accountConfig(fsys))
	must(t, err)
	must(t, first.Put(account{ID: "acc_1", Email: "kept@x.dev", Teams: []string{"red"}}))
	must(t, first.Close())

	second, err := openWith(accountConfig(fsys))
	must(t, err)
	if a, err := second.Lookup("email", "kept@x.dev"); err != nil || a.ID != "acc_1" {
		t.Fatalf("after a reopen, Lookup() = %+v, %v", a, err)
	}
	if red, findErr := second.Find("team", "red"); findErr != nil || len(red) != 1 {
		t.Fatalf("after a reopen, Find() = %v, %v", red, findErr)
	}
	must(t, second.Close())

	//: the snapshot edited by hand, the way a framework's files are: two
	//: documents sharing a unique key.
	broken := `{"acc_1":{"id":"acc_1","email":"twice@x.dev"},"acc_2":{"id":"acc_2","email":"twice@x.dev"}}`
	must(t, fsys.WriteAtomic("members/accounts.json", []byte(broken), 0o600))
	_, err = openWith(accountConfig(fsys))
	requireCode(t, err, docstore.CodeIndexBroken, "an open over documents breaking a unique index")
	if strings.Contains(errs.PublicOf(err), "twice@x.dev") || strings.Contains(fieldsText(err), "twice@x.dev") {
		t.Errorf("the refusal quotes the key: %q %s", errs.PublicOf(err), fieldsText(err))
	}
}

// TestAKeyFunctionThatPanicsOnOpen pins the one recovery the store makes: a
// key function that panics while the indexes are rebuilt refuses the open
// with IndexBroken, carrying the panic as a field.
func TestAKeyFunctionThatPanicsOnOpen(t *testing.T) {
	t.Parallel()
	fsys := memFS()
	plain, err := docstore.Open(docstore.Config[account]{Key: accountKey, FS: fsys, Path: "members/accounts.json"})
	must(t, err)
	must(t, plain.Put(account{ID: "acc_1"}))
	must(t, plain.Close())
	cfg := docstore.Config[account]{Key: accountKey, FS: fsys, Path: "members/accounts.json"}
	_, err = docstore.Open(cfg, docstore.Index("boom", func(account) []string { panic("key function panicked") }))
	requireCode(t, err, docstore.CodeIndexBroken, "an open whose key function panics")
}

// TestIndexDeclarationProblems pins every configuration Open refuses before a
// filesystem is touched.
func TestIndexDeclarationProblems(t *testing.T) {
	t.Parallel()
	keys := func(account) []string { return nil }
	for _, c := range []struct {
		name    string
		cfg     docstore.Config[account]
		indexes []docstore.IndexSpec[account]
	}{
		{name: "no key function", cfg: docstore.Config[account]{}},
		{name: "an index without a name", cfg: docstore.Config[account]{Key: accountKey}, indexes: []docstore.IndexSpec[account]{docstore.Index("", keys)}},
		{name: "an index declared twice", cfg: docstore.Config[account]{Key: accountKey}, indexes: []docstore.IndexSpec[account]{
			docstore.Unique("email", func(a account) string { return a.Email }), docstore.Index("email", keys),
		}},
		{name: "a multi-valued index without a function", cfg: docstore.Config[account]{Key: accountKey}, indexes: []docstore.IndexSpec[account]{docstore.Index[account]("team", nil)}},
		{name: "a unique index without a function", cfg: docstore.Config[account]{Key: accountKey}, indexes: []docstore.IndexSpec[account]{docstore.Unique[account]("email", nil)}},
		{name: "a path without a filesystem", cfg: docstore.Config[account]{Key: accountKey, Path: "members/accounts.json"}},
		{name: "a filesystem without a path", cfg: docstore.Config[account]{Key: accountKey, FS: memFS()}},
		{name: "a path that climbs out", cfg: docstore.Config[account]{Key: accountKey, FS: memFS(), Path: "../accounts.json"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			store, err := docstore.Open(c.cfg, c.indexes...)
			requireCode(t, err, docstore.CodeStoreMisconfigured, c.name)
			if store != nil {
				t.Fatal("a refused configuration returned a store")
			}
		})
	}
}
