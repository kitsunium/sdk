// Package docstore_test — the file store's trash in its files: a trashed
// document and its versions in the trashing's own overlay entry, then in the
// versions file, loaded back as written, and refused when no store wrote it.
package docstore_test

import (
	"slices"
	"testing"
	"time"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/docstore"
)

// TestTheTrashIsDurable pins the trash through a crash and a fold: once Trash
// returns, a process that dies keeps the trashed document out of every read,
// with its versions and the instant it was trashed — the entry holds them —
// and a fold carries them into the versions file, from which Restore brings
// the document back.
func TestTheTrashIsDurable(t *testing.T) {
	t.Parallel()
	fsys := memFS()
	trashedAt := epoch.Add(time.Hour)
	clk := clock.NewManualClock(epoch)
	cfg := versionedConfig(fsys, 2, clk)
	cfg.FoldAt = -1
	crashed, err := openWith(cfg)
	must(t, err)
	must(t, crashed.Insert(account{ID: "acc_1", Name: "v1", Email: "ada@x.dev"}))
	must(t, crashed.Put(account{ID: "acc_1", Name: "v2", Email: "ada@x.dev"}))
	must(t, crashed.Put(account{ID: "acc_2", Name: "kept"}))
	before := versionsOf(t, crashed, "acc_1")
	clk.Set(trashedAt)
	must(t, crashed.Trash("acc_1"))
	//: abandoned without Close: the overlay is all there is.

	check := func(what string) {
		t.Helper()
		store := openVersioned(t, versionedConfig(fsys, 2, nil))
		_, getErr := store.Get("acc_1")
		requireCode(t, getErr, coredocstore.CodeDocumentNotFound, what+": Get of the trashed document")
		_, getErr = store.Lookup("email", "ada@x.dev")
		requireCode(t, getErr, coredocstore.CodeDocumentNotFound, what+": Lookup of its e-mail")
		if after := versionsOf(t, store, "acc_1"); !slices.EqualFunc(after, before, sameVersion) {
			t.Fatalf("%s: the trashed document's versions are %+v, want %+v", what, after, before)
		}
		tombs, tombErr := store.Tombstones()
		if tombErr != nil || len(tombs) != 1 || tombs[0].Key != "acc_1" || !tombs[0].At.Equal(trashedAt) {
			t.Fatalf("%s: Tombstones() = %+v, %v", what, tombs, tombErr)
		}
		must(t, store.Close())
	}
	check("after a crash")
	if snapshot := readFile(t, fsys, snapshotPath); quotes(snapshot, "acc_1") {
		t.Fatalf("the folded snapshot holds the trashed document: %s", snapshot)
	}
	if versions := readFile(t, fsys, versionsPath); !quotes(versions, `"trashed"`) {
		t.Fatalf("the versions file keeps no trash: %s", versions)
	}
	check("after a fold")

	store := openVersioned(t, versionedConfig(fsys, 2, nil))
	must(t, store.Restore("acc_1"))
	must(t, store.Close())
	reopened := openVersioned(t, versionedConfig(fsys, 2, nil))
	if a, lookupErr := reopened.Lookup("email", "ada@x.dev"); lookupErr != nil || a.Name != "v2" {
		t.Fatalf("a restored document after a reopen = %+v, %v", a, lookupErr)
	}
	if after := versionsOf(t, reopened, "acc_1"); !slices.EqualFunc(after, before, sameVersion) {
		t.Fatalf("a restored document's versions after a reopen are %+v, want %+v", after, before)
	}
	if tombs, tombErr := reopened.Tombstones(); tombErr != nil || len(tombs) != 0 {
		t.Fatalf("Tombstones() after a restore and a reopen = %+v, %v", tombs, tombErr)
	}
}

// TestAPurgeIsDurable pins Purge through a crash: the trashed document and its
// versions are gone from the files, and a document written again under its key
// after a reopen starts at version 1.
func TestAPurgeIsDurable(t *testing.T) {
	t.Parallel()
	fsys := memFS()
	crashVersioned(t, fsys, 2, func(store *docstore.Store[account]) {
		must(t, store.Put(account{ID: "acc_1", Name: "v1"}))
		must(t, store.Put(account{ID: "acc_1", Name: "v2"}))
		must(t, store.Trash("acc_1"))
		must(t, store.Purge("acc_1"))
	})
	store := openVersioned(t, versionedConfig(fsys, 2, nil))
	if tombs, err := store.Tombstones(); err != nil || len(tombs) != 0 {
		t.Fatalf("Tombstones() after a purge and a crash = %+v, %v", tombs, err)
	}
	_, err := store.Versions("acc_1")
	requireCode(t, err, coredocstore.CodeDocumentNotFound, "Versions of a purged document after a crash")
	must(t, store.Insert(account{ID: "acc_1", Name: "again"}))
	if got := numbers(versionsOf(t, store, "acc_1")); !slices.Equal(got, []uint64{1}) {
		t.Fatalf("a document written after a purge has versions %v", got)
	}
}

// TestARefusedTrashChangesNothing pins the trash's writes to the filesystem's
// verdict: a publication refused leaves the document, its index keys and its
// versions as they were — or the trash as it was, for Restore and Purge.
func TestARefusedTrashChangesNothing(t *testing.T) {
	t.Parallel()
	faulty := &faultyFS{FullFS: memFS()}
	store := openVersioned(t, versionedConfig(faulty, 2, nil))
	must(t, store.Insert(account{ID: "acc_1", Name: "v1", Email: "ada@x.dev"}))
	faulty.set("", true, false)
	requireCode(t, store.Trash("acc_1"), coredocstore.CodePersistFailed, "a refused Trash")
	faulty.set("", false, false)
	if a, err := store.Lookup("email", "ada@x.dev"); err != nil || a.Name != "v1" {
		t.Fatalf("a refused Trash left Lookup() = %+v, %v", a, err)
	}
	if tombs, err := store.Tombstones(); err != nil || len(tombs) != 0 {
		t.Fatalf("a refused Trash left Tombstones() = %+v, %v", tombs, err)
	}
	must(t, store.Trash("acc_1"))
	faulty.set("", true, false)
	requireCode(t, store.Restore("acc_1"), coredocstore.CodePersistFailed, "a refused Restore")
	requireCode(t, store.Purge("acc_1"), coredocstore.CodePersistFailed, "a refused Purge")
	faulty.set("", false, false)
	if tombs, err := store.Tombstones(); err != nil || !slices.Equal(trashedKeys(tombs), []string{"acc_1"}) {
		t.Fatalf("a refused Restore or Purge left the trash %+v, %v", tombs, err)
	}
	_, err := store.Lookup("email", "ada@x.dev")
	requireCode(t, err, coredocstore.CodeDocumentNotFound, "Lookup after a refused Restore")
}

// TestTrashFilesThatAreNotAStores pins LoadFailed for every trash no store
// wrote — a trashed document beside the same key's document, a trash holding
// no document, an entry holding a document and a trash — and that no refusal
// quotes what it held.
func TestTrashFilesThatAreNotAStores(t *testing.T) {
	t.Parallel()
	const secret = "s3cr3t-in-the-trash"
	snapshot := `{"acc_1": {"id": "acc_1"}}`
	entryName := entryNameOf(t, "acc_1")
	trashed := `"trashed": {"at": "2026-10-14T00:00:00Z", "doc": {"id": "acc_1", "name": "` + secret + `"}}`
	for _, c := range []struct {
		name  string
		files map[string]string
	}{
		{"a trashed document the store also holds", map[string]string{versionsPath: `{"acc_1": {"current": {"number": 1}, ` + trashed + `}}`}},
		{"a trash without a document", map[string]string{versionsPath: `{"acc_9": {"current": {"number": 1}, "trashed": {"at": "2026-10-14T00:00:00Z"}}}`}},
		{"an entry holding a document and a trash", map[string]string{overlayPath + "/" + entryName: `{"key": "acc_1", "doc": {"id": "acc_1"}, "versions": {"current": {"number": 1}, ` + trashed + `}}`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			fsys := memFS()
			must(t, fsys.MkdirAll(overlayPath, 0o700))
			must(t, fsys.WriteAtomic(snapshotPath, []byte(snapshot), 0o600))
			for name, content := range c.files {
				must(t, fsys.WriteAtomic(name, []byte(content), 0o600))
			}
			_, err := openWith(versionedConfig(fsys, 2, nil))
			requireCode(t, err, coredocstore.CodeLoadFailed, c.name)
			if quotes(err.Error()+errs.PublicOf(err)+errs.PrivateOf(err)+fieldsText(err), secret, "acc_9") {
				t.Errorf("%s: the refusal quotes the file: %s", c.name, fieldsText(err))
			}
		})
	}
}
