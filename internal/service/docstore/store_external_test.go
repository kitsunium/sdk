// Package docstore_test — the store's reads and writes, as a caller uses them.
package docstore_test

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/docstore"
)

// TestTheWriteModes pins what each write expects under its key: Put anything,
// Insert nothing (DocumentExists otherwise), Replace a document
// (DocumentNotFound otherwise, so a deleted document is never brought back) —
// and that every read hands back a copy.
func TestTheWriteModes(t *testing.T) {
	t.Parallel()
	store := openAccounts(t, nil)
	must(t, store.Insert(account{ID: "acc_1", Name: "Ada"}))
	requireCode(t, store.Insert(account{ID: "acc_1", Name: "Twin"}), docstore.CodeDocumentExists, "Insert over an existing key")
	requireCode(t, store.Replace(account{ID: "acc_2", Name: "Nobody"}), docstore.CodeDocumentNotFound, "Replace of a missing key")
	must(t, store.Replace(account{ID: "acc_1", Name: "Ada Lovelace"}))
	must(t, store.Put(account{ID: "acc_2", Name: "Grace"}))
	must(t, store.Put(account{ID: "acc_2", Name: "Grace Hopper"}))

	got, err := store.Get("acc_1")
	if err != nil || got.Name != "Ada Lovelace" {
		t.Fatalf("Get(acc_1) = %+v, %v", got, err)
	}
	//: a read is a copy: changing it changes nothing stored.
	got.Name = "Mutated"
	if again, againErr := store.Get("acc_1"); againErr != nil || again.Name != "Ada Lovelace" {
		t.Fatalf("a read aliased the store: %+v, %v", again, againErr)
	}
	all, err := store.List()
	if err != nil || !slices.Equal(ids(all), []string{"acc_1", "acc_2"}) {
		t.Fatalf("List() = %v, %v, want both, in key order", ids(all), err)
	}
	//: a deletion, then everything that needs the document refuses.
	must(t, store.Delete("acc_1"))
	requireCode(t, store.Delete("acc_1"), docstore.CodeDocumentNotFound, "Delete of a missing key")
	requireCode(t, store.Replace(account{ID: "acc_1"}), docstore.CodeDocumentNotFound, "Replace of a deleted key")
	_, getErr := store.Get("acc_1")
	requireCode(t, getErr, docstore.CodeDocumentNotFound, "Get of a deleted key")
	if n := store.Stats().Documents; n != 1 {
		t.Fatalf("Stats().Documents = %d, want 1", n)
	}
}

// TestAKeyIsNeverEmpty pins the refusal of a document the store could never
// find again.
func TestAKeyIsNeverEmpty(t *testing.T) {
	t.Parallel()
	store := openAccounts(t, nil)
	requireCode(t, store.Put(account{Name: "No ID"}), docstore.CodeDocumentKeyEmpty, "Put of an empty key")
	if n := store.Stats().Documents; n != 0 {
		t.Fatalf("the refused document was stored: %d documents", n)
	}
}

// TestUpdate pins the read-modify-write: the stored result is returned; an
// error from the function is returned untouched and changes nothing; a result
// under another key is refused; the function may read the store it updates.
func TestUpdate(t *testing.T) {
	t.Parallel()
	store := openAccounts(t, nil)
	must(t, store.Put(account{ID: "acc_1", Name: "Ada", Email: "ada@x.dev"}))
	must(t, store.Put(account{ID: "acc_2", Name: "Grace", Email: "grace@x.dev"}))

	updated, err := store.Update("acc_1", func(a *account) error {
		//: a read of the same store, from inside the update.
		other, readErr := store.Get("acc_2")
		if readErr != nil {
			return readErr
		}
		a.Name = "Ada, after " + other.Name
		return nil
	})
	if err != nil || updated.Name != "Ada, after Grace" {
		t.Fatalf("Update() = %+v, %v", updated, err)
	}
	if got, getErr := store.Get("acc_1"); getErr != nil || got.Name != "Ada, after Grace" {
		t.Fatalf("the update was not stored: %+v, %v", got, getErr)
	}

	refusal := errors.New("the caller's own refusal")
	_, err = store.Update("acc_1", func(a *account) error { a.Name = "Lost"; return refusal })
	if !errors.Is(err, refusal) {
		t.Fatalf("Update() with a failing function = %v, want the function's own error", err)
	}
	_, err = store.Update("acc_1", func(a *account) error { a.ID = "acc_9"; return nil })
	requireCode(t, err, docstore.CodeDocumentKeyChanged, "an update that renames")
	_, err = store.Update("acc_404", func(*account) error { return nil })
	requireCode(t, err, docstore.CodeDocumentNotFound, "an update of a missing key")
	if got, getErr := store.Get("acc_1"); getErr != nil || got.Name != "Ada, after Grace" {
		t.Fatalf("a refused update changed the document: %+v, %v", got, getErr)
	}
	if _, found := store.Get("acc_9"); !errs.HasCode(found, docstore.CodeDocumentNotFound) {
		t.Fatalf("a refused rename stored the new key: %v", found)
	}
}

// TestHooks pins the two announcements: OnWrite after every write that took
// effect and OnDelete after every deletion, with the key, outside every lock —
// a hook may use the store it is told about — and never for a refused write.
func TestHooks(t *testing.T) {
	t.Parallel()
	store := openAccounts(t, nil)
	var written, deleted []string
	removeWrite := store.OnWrite(func(key string) {
		//: outside every lock: the hook reads the store.
		if _, err := store.Get(key); err != nil {
			t.Errorf("the hook could not read %s: %v", key, err)
		}
		written = append(written, key)
	})
	store.OnDelete(func(key string) { deleted = append(deleted, key) })

	must(t, store.Put(account{ID: "acc_1"}))
	must(t, store.Insert(account{ID: "acc_2"}))
	must(t, store.Replace(account{ID: "acc_2", Name: "Grace"}))
	_, err := store.Update("acc_1", func(a *account) error { a.Name = "Ada"; return nil })
	must(t, err)
	//: refused writes announce nothing.
	requireCode(t, store.Insert(account{ID: "acc_1"}), docstore.CodeDocumentExists, "Insert over an existing key")
	_, err = store.Update("acc_1", func(*account) error { return errors.New("refused") })
	if err == nil {
		t.Fatal("the failing update succeeded")
	}
	must(t, store.Delete("acc_2"))
	removeWrite()
	must(t, store.Put(account{ID: "acc_3"}))

	if !slices.Equal(written, []string{"acc_1", "acc_2", "acc_2", "acc_1"}) {
		t.Errorf("OnWrite saw %v", written)
	}
	if !slices.Equal(deleted, []string{"acc_2"}) {
		t.Errorf("OnDelete saw %v", deleted)
	}
}

// TestEntries pins the raw view: documents as stored, in key order, a
// positive limit capping them, and every JSON the caller's own copy.
func TestEntries(t *testing.T) {
	t.Parallel()
	store := openAccounts(t, nil)
	for _, id := range []string{"acc_c", "acc_a", "acc_b"} {
		must(t, store.Put(account{ID: id}))
	}
	all, err := store.Entries(0)
	if err != nil || len(all) != 3 || all[0].Key != "acc_a" || all[2].Key != "acc_c" {
		t.Fatalf("Entries(0) = %+v, %v", all, err)
	}
	two, err := store.Entries(2)
	if err != nil || len(two) != 2 || two[1].Key != "acc_b" {
		t.Fatalf("Entries(2) = %+v, %v", two, err)
	}
	var decoded account
	if err := json.Unmarshal(two[0].JSON, &decoded); err != nil || decoded.ID != "acc_a" {
		t.Fatalf("Entries JSON = %s, %v", two[0].JSON, err)
	}
	//: the caller's copy: overwriting it changes nothing stored.
	for i := range two[0].JSON {
		two[0].JSON[i] = ' '
	}
	if got, err := store.Get("acc_a"); err != nil || got.ID != "acc_a" {
		t.Fatalf("editing an entry reached the store: %+v, %v", got, err)
	}
}

// TestAClosedStore pins what Close leaves: every call StoreClosed, Close again
// nil, Stats still answering.
func TestAClosedStore(t *testing.T) {
	t.Parallel()
	store, err := openWith(accountConfig(nil))
	must(t, err)
	must(t, store.Put(account{ID: "acc_1", Email: "a@x.dev"}))
	must(t, store.Close())
	must(t, store.Close())
	_, getErr := store.Get("acc_1")
	requireCode(t, getErr, docstore.CodeStoreClosed, "Get after Close")
	_, listErr := store.List()
	requireCode(t, listErr, docstore.CodeStoreClosed, "List after Close")
	_, lookupErr := store.Lookup("email", "a@x.dev")
	requireCode(t, lookupErr, docstore.CodeStoreClosed, "Lookup after Close")
	_, findErr := store.Find("email", "a@x.dev")
	requireCode(t, findErr, docstore.CodeStoreClosed, "Find after Close")
	_, entriesErr := store.Entries(1)
	requireCode(t, entriesErr, docstore.CodeStoreClosed, "Entries after Close")
	requireCode(t, store.Put(account{ID: "acc_2"}), docstore.CodeStoreClosed, "Put after Close")
	requireCode(t, store.Delete("acc_1"), docstore.CodeStoreClosed, "Delete after Close")
	_, updateErr := store.Update("acc_1", func(*account) error { return nil })
	requireCode(t, updateErr, docstore.CodeStoreClosed, "Update after Close")
	requireCode(t, store.Fold(), docstore.CodeStoreClosed, "Fold after Close")
	if n := store.Stats().Documents; n != 1 {
		t.Fatalf("Stats().Documents after Close = %d, want 1", n)
	}
}

// storedAs is a document whose type changes between two opens of one store.
type storedAs struct {
	ID    string `json:"id"`
	Stock string `json:"stock"`
}

// readAs is the same document, read by a program that changed Stock's type.
type readAs struct {
	ID    string `json:"id"`
	Stock int    `json:"stock"`
}

// TestADocumentTheTypeNoLongerFits pins DocumentUndecodable: a store opened,
// as a type they no longer fit, over documents an older program wrote is
// refused at Open — without indexes too, since every document is decoded to
// check the key it is stored under — and no text of the refusal carries the
// stored value.
func TestADocumentTheTypeNoLongerFits(t *testing.T) {
	t.Parallel()
	fsys := memFS()
	older, err := docstore.Open(docstore.Config[storedAs]{Key: func(v storedAs) string { return v.ID }, FS: fsys, Path: "shop/items.json"})
	must(t, err)
	must(t, older.Put(storedAs{ID: "item_1", Stock: "s3cr3t-quantity"}))
	must(t, older.Close())
	_, openErr := docstore.Open(docstore.Config[readAs]{Key: func(v readAs) string { return v.ID }, FS: fsys, Path: "shop/items.json"})
	requireCode(t, openErr, docstore.CodeDocumentUndecodable, "an open over a document the type no longer fits")
	if quotes(errs.PublicOf(openErr)+errs.PrivateOf(openErr)+openErr.Error()+fieldsText(openErr), "s3cr3t-quantity") {
		t.Fatalf("the refusal quotes the stored value: %v %v", openErr, errs.FieldsOf(openErr))
	}
}

// fieldsText renders an error's fields for a leak check.
func fieldsText(err error) string {
	text := ""
	for _, field := range errs.FieldsOf(err) {
		text += field.Key() + "=" + field.StringValue() + " "
	}
	return text
}
