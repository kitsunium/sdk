package docstore_test

import (
	"encoding/json"
	"testing"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestFileChangeFeedAfterARefusedDeleteStillReplaysIntoTheStore: a Delete
// whose publication the filesystem refuses leaves the document in the store,
// and its record, a delete, names no version a reader could skip on
// (kitsunium/sdk#831). The record that restates the key after the refusal
// makes a feed replayed over an empty store give the store's documents.
func TestFileChangeFeedAfterARefusedDeleteStillReplaysIntoTheStore(t *testing.T) {
	t.Parallel()
	faulty := &faultyFS{FullFS: memFS()}
	feed := changeStream(t)
	cfg := accountConfig(faulty)
	cfg.Versions, cfg.Changes, cfg.ChangesWithDocument = 2, feed, true
	store, err := openWith(cfg)
	must(t, err)
	must(t, store.Put(account{ID: "a", Name: "kept"}))
	faulty.set("", true, false)
	requireCode(t, store.Delete("a"), coredocstore.CodePersistFailed, "a refused delete")
	faulty.set("", false, false)
	if _, getErr := store.Get("a"); getErr != nil {
		t.Fatalf("Get after a refused delete = %v, want the document", getErr)
	}
	must(t, store.Close())
	replicaFS := memFS()
	replica, err := openWith(accountConfig(replicaFS))
	must(t, err)
	for _, change := range readChanges(t, feed) {
		switch change.Op {
		case coredocstore.ChangePut, coredocstore.ChangeRestore:
			var doc account
			must(t, json.Unmarshal(change.Document, &doc))
			must(t, replica.Put(doc))
		default:
			if delErr := replica.Delete(change.Key); delErr != nil && !errs.HasCode(delErr, coredocstore.CodeDocumentNotFound) {
				t.Fatal(delErr)
			}
		}
	}
	if _, getErr := replica.Get("a"); getErr != nil {
		t.Fatalf("the replayed store lost a document the source still holds: %v (changes %+v)", getErr, readChanges(t, feed))
	}
	must(t, replica.Close())
}

// A Put of a new key whose publication the filesystem refuses is answered by
// a delete: the store holds no document under the key, and a reader replaying
// the feed ends without one, whether or not it skips on versions.
func TestFileChangeFeedAfterARefusedPutOfANewKeyRestatesItAbsent(t *testing.T) {
	t.Parallel()
	faulty := &faultyFS{FullFS: memFS()}
	feed := changeStream(t)
	cfg := accountConfig(faulty)
	cfg.Changes = feed
	store, err := openWith(cfg)
	must(t, err)
	t.Cleanup(func() { must(t, store.Close()) })
	faulty.set("", true, false)
	requireCode(t, store.Put(account{ID: "n", Name: "never"}), coredocstore.CodePersistFailed, "a refused put")
	faulty.set("", false, false)
	changes := readChanges(t, feed)
	if len(changes) != 2 || changes[0].Op != coredocstore.ChangePut || changes[1].Op != coredocstore.ChangeDelete || changes[1].Key != "n" {
		t.Fatalf("changes = %+v, want the refused put, then a delete restating the key absent", changes)
	}
}
