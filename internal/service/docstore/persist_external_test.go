// Package docstore_test — the files: durable before a write returns, one
// snapshot at rest, and the same documents loaded whatever point a crash
// interrupted.
package docstore_test

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	corevfs "github.com/kitsunium/sdk/internal/core/vfs"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/docstore"
)

// snapshotPath and overlayPath are where the accounts store keeps its files.
const (
	snapshotPath = "members/accounts.json"
	overlayPath  = "members/accounts.json.d"
)

// crash opens an accounts store over fsys and abandons it without Close — a
// process that died — once fn has run against it.
func crash(t *testing.T, fsys corevfs.FullFS, fn func(store *docstore.Store[account])) {
	t.Helper()
	cfg := accountConfig(fsys)
	cfg.FoldAt = -1
	store, err := openWith(cfg)
	must(t, err)
	fn(store)
}

// documentIDs lists what a fresh open of the accounts store over fsys holds.
func documentIDs(t *testing.T, fsys corevfs.FullFS) []string {
	t.Helper()
	store, err := openWith(accountConfig(fsys))
	must(t, err)
	defer func() { must(t, store.Close()) }()
	all, err := store.List()
	must(t, err)
	return ids(all)
}

// TestAWriteIsDurableBeforeItReturns pins the promise: once a write returns,
// a process that dies loses nothing — the next open replays the overlay.
func TestAWriteIsDurableBeforeItReturns(t *testing.T) {
	t.Parallel()
	fsys := memFS()
	crash(t, fsys, func(store *docstore.Store[account]) {
		must(t, store.Put(account{ID: "acc_1", Email: "a@x.dev"}))
		must(t, store.Put(account{ID: "acc_2"}))
		must(t, store.Delete("acc_2"))
		must(t, store.Put(account{ID: "acc_3"}))
	})
	if got := documentIDs(t, fsys); !slices.Equal(got, []string{"acc_1", "acc_3"}) {
		t.Fatalf("after a crash the store holds %v, want acc_1 and acc_3", got)
	}
}

// TestAClosedStoreRestsAsOneSnapshot pins the resting state: after Close the
// overlay is empty and the snapshot is one JSON object from key to document,
// sorted and readable — the file a person, or a framework's test, opens.
func TestAClosedStoreRestsAsOneSnapshot(t *testing.T) {
	t.Parallel()
	fsys := memFS()
	store, err := openWith(accountConfig(fsys))
	must(t, err)
	must(t, store.Put(account{ID: "item_kept", Name: "kept"}))
	must(t, store.Put(account{ID: "acc_2", Name: "second"}))
	if n := len(overlayEntries(t, fsys)); n != 2 {
		t.Fatalf("before Close the overlay holds %d entries, want 2", n)
	}
	must(t, store.Close())
	if left := overlayEntries(t, fsys); len(left) != 0 {
		t.Fatalf("after Close the overlay still holds %v", left)
	}
	var object map[string]account
	must(t, json.Unmarshal([]byte(readFile(t, fsys, snapshotPath)), &object))
	if object["item_kept"].Name != "kept" || object["acc_2"].Name != "second" || len(object) != 2 {
		t.Fatalf("the snapshot is %v", object)
	}
}

// TestAFoldInterruptedAnywhereLoadsTheSameDocuments pins the property the
// file layout rests on: an entry holds its key's whole latest state, so
// replaying entries the last fold already contains changes nothing. A crash
// after the snapshot and before the removals, or between two removals, loads
// exactly what a clean fold loads — deletions included.
func TestAFoldInterruptedAnywhereLoadsTheSameDocuments(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		// kept is how many of the folded entries survive the crash.
		kept int
	}{
		{"every folded entry survives", -1},
		{"one folded entry survives", 1},
		{"none survives", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			fsys := memFS()
			var saved map[string][]byte
			crash(t, fsys, func(store *docstore.Store[account]) {
				must(t, store.Put(account{ID: "acc_1", Name: "first"}))
				must(t, store.Put(account{ID: "acc_2", Name: "doomed"}))
				must(t, store.Put(account{ID: "acc_1", Name: "rewritten"}))
				must(t, store.Delete("acc_2"))
				must(t, store.Put(account{ID: "acc_3", Name: "third"}))
				saved = saveOverlay(t, fsys)
				must(t, store.Fold())
			})
			//: the removals the crash undid, put back.
			restored := 0
			for _, name := range slices.Sorted(maps.Keys(saved)) {
				if c.kept >= 0 && restored >= c.kept {
					break
				}
				must(t, fsys.WriteAtomic(overlayPath+"/"+name, saved[name], 0o600))
				restored++
			}
			store, err := openWith(accountConfig(fsys))
			must(t, err)
			defer func() { must(t, store.Close()) }()
			all, err := store.List()
			must(t, err)
			if !slices.Equal(ids(all), []string{"acc_1", "acc_3"}) || all[0].Name != "rewritten" {
				t.Fatalf("after an interrupted fold the store holds %+v", all)
			}
			if left := overlayEntries(t, fsys); len(left) != 0 {
				t.Fatalf("the open did not fold the replayed entries: %v", left)
			}
		})
	}
}

// saveOverlay copies every overlay entry's bytes, by name.
func saveOverlay(t *testing.T, fsys corevfs.FullFS) map[string][]byte {
	t.Helper()
	saved := map[string][]byte{}
	for _, name := range overlayEntries(t, fsys) {
		saved[name] = []byte(readFile(t, fsys, overlayPath+"/"+name))
	}
	return saved
}

// TestAFrameworksSnapshotLoads pins the format a framework already writes: a
// bare JSON object from key to document is a snapshot, so its existing files
// open as they are.
func TestAFrameworksSnapshotLoads(t *testing.T) {
	t.Parallel()
	fsys := memFS()
	must(t, fsys.MkdirAll("members", 0o700))
	must(t, fsys.WriteAtomic(snapshotPath, []byte("{\n  \"acc_1\": {\n    \"id\": \"acc_1\",\n    \"email\": \"ada@x.dev\"\n  }\n}"), 0o600))
	store := openAccounts(t, fsys)
	if a, err := store.Lookup("email", "ada@x.dev"); err != nil || a.ID != "acc_1" {
		t.Fatalf("Lookup over a framework's snapshot = %+v, %v", a, err)
	}
}

// TestLeftoversAndStrangers pins what Open does with the overlay's other
// files: a temporary a crashed publication left is removed, and a file that
// is not the store's is left alone.
func TestLeftoversAndStrangers(t *testing.T) {
	t.Parallel()
	fsys := memFS()
	must(t, fsys.MkdirAll(overlayPath, 0o700))
	must(t, fsys.WriteFile(overlayPath+"/.vfs-0123abcd.tmp", []byte("half a record"), 0o600))
	must(t, fsys.WriteFile(overlayPath+"/NOTES.txt", []byte("an operator's note"), 0o600))
	openAccounts(t, fsys)
	if left := overlayEntries(t, fsys); !slices.Equal(left, []string{"NOTES.txt"}) {
		t.Fatalf("the overlay holds %v, want only the operator's note", left)
	}
}

// TestFilesThatAreNotAStore pins LoadFailed for every file Open cannot trust
// — and that no refusal quotes what the file held.
func TestFilesThatAreNotAStore(t *testing.T) {
	t.Parallel()
	const secret = "s3cr3t-in-the-file"
	validName := entryNameOf(t, "acc_1")
	for _, c := range []struct {
		name  string
		files map[string]string
	}{
		{"a snapshot that is not JSON", map[string]string{snapshotPath: "{" + secret}},
		{"a snapshot that is not an object", map[string]string{snapshotPath: `["` + secret + `"]`}},
		{"a snapshot with an empty key", map[string]string{snapshotPath: `{"": {"id": "` + secret + `"}}`}},
		{"an entry that is not JSON", map[string]string{overlayPath + "/" + validName: secret}},
		{"an entry under another key's name", map[string]string{overlayPath + "/" + validName: `{"key": "acc_2", "doc": {"id": "` + secret + `"}}`}},
		{"an entry without a document", map[string]string{overlayPath + "/" + validName: `{"key": "acc_1"}`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			fsys := memFS()
			must(t, fsys.MkdirAll(overlayPath, 0o700))
			for name, content := range c.files {
				must(t, fsys.WriteAtomic(name, []byte(content), 0o600))
			}
			_, err := openWith(accountConfig(fsys))
			requireCode(t, err, docstore.CodeLoadFailed, c.name)
			if quotes(err.Error()+errs.PublicOf(err)+errs.PrivateOf(err)+fieldsText(err), secret) {
				t.Errorf("%s: the refusal quotes the file: %s", c.name, fieldsText(err))
			}
		})
	}
}

// TestARefusedOpenWritesNothing pins the order of Open: every check comes
// before the fold, so an open refused over a broken unique index leaves the
// snapshot an operator edited byte for byte, and the overlay entry it would
// have folded where it was.
func TestARefusedOpenWritesNothing(t *testing.T) {
	t.Parallel()
	fsys := memFS()
	//: one entry pending in the overlay, as a process that died leaves it.
	crash(t, fsys, func(store *docstore.Store[account]) {
		must(t, store.Put(account{ID: "acc_3", Email: "third@x.dev"}))
	})
	//: the snapshot edited by hand: two documents sharing a unique key.
	edited := `{"acc_1":{"id":"acc_1","email":"twice@x.dev"},"acc_2":{"id":"acc_2","email":"twice@x.dev"}}`
	must(t, fsys.WriteAtomic(snapshotPath, []byte(edited), 0o600))
	pending := overlayEntries(t, fsys)
	_, err := openWith(accountConfig(fsys))
	requireCode(t, err, docstore.CodeIndexBroken, "an open over documents breaking a unique index")
	if after := readFile(t, fsys, snapshotPath); after != edited {
		t.Fatalf("the refused open rewrote the snapshot:\n%s", after)
	}
	if left := overlayEntries(t, fsys); len(left) != 1 || !slices.Equal(left, pending) {
		t.Fatalf("the refused open changed the overlay: %v, was %v", left, pending)
	}
}

// entryNameOf finds the overlay entry name the store gives key, by writing it.
func entryNameOf(t *testing.T, key string) string {
	t.Helper()
	fsys := memFS()
	crash(t, fsys, func(store *docstore.Store[account]) { must(t, store.Put(account{ID: key})) })
	names := overlayEntries(t, fsys)
	if len(names) != 1 {
		t.Fatalf("one write left %v", names)
	}
	return names[0]
}

// TestFoldThresholds pins when a write folds: FoldAt entries, never on a write
// when negative — Fold and Close still do.
func TestFoldThresholds(t *testing.T) {
	t.Parallel()
	fsys := memFS()
	cfg := accountConfig(fsys)
	cfg.FoldAt = 3
	store, err := openWith(cfg)
	must(t, err)
	if stats := store.Stats(); stats.Folds != 1 || stats.Pending != 0 {
		t.Fatalf("a first open folds once, to write its snapshot: %+v", stats)
	}
	must(t, store.Put(account{ID: "acc_1"}))
	must(t, store.Put(account{ID: "acc_2"}))
	if stats := store.Stats(); stats.Pending != 2 || stats.Folds != 1 {
		t.Fatalf("below the threshold: %+v", stats)
	}
	must(t, store.Put(account{ID: "acc_3"}))
	if stats := store.Stats(); stats.Pending != 0 || stats.Folds != 2 {
		t.Fatalf("at the threshold: %+v", stats)
	}
	must(t, store.Close())

	never := accountConfig(memFS())
	never.FoldAt = -1
	store, err = openWith(never)
	must(t, err)
	for i := range 10 {
		must(t, store.Put(account{ID: string(rune('a' + i))}))
	}
	if stats := store.Stats(); stats.Pending != 10 || stats.Folds != 1 {
		t.Fatalf("a store that never folds on a write: %+v", stats)
	}
	must(t, store.Fold())
	if stats := store.Stats(); stats.Pending != 0 || stats.Folds != 2 {
		t.Fatalf("after Fold: %+v", stats)
	}
	must(t, store.Close())
}

// TestAWriteTheDiskRefusedChangesNothing pins PersistFailed: memory, indexes
// and hooks all stay as they were, so memory and disk still agree.
func TestAWriteTheDiskRefusedChangesNothing(t *testing.T) {
	t.Parallel()
	faulty := &faultyFS{FullFS: memFS()}
	store := openAccounts(t, faulty)
	must(t, store.Put(account{ID: "acc_1", Email: "a@x.dev"}))
	calls := 0
	store.OnWrite(func(string) { calls++ })
	store.OnDelete(func(string) { calls++ })
	faulty.set("", true, false)
	requireCode(t, store.Put(account{ID: "acc_2", Email: "b@x.dev"}), docstore.CodePersistFailed, "a refused Put")
	requireCode(t, store.Delete("acc_1"), docstore.CodePersistFailed, "a refused Delete")
	_, updateErr := store.Update("acc_1", func(a *account) error { a.Email = "c@x.dev"; return nil })
	requireCode(t, updateErr, docstore.CodePersistFailed, "a refused Update")
	faulty.set("", false, false)
	if _, err := store.Get("acc_2"); !errs.HasCode(err, docstore.CodeDocumentNotFound) {
		t.Fatalf("a refused Put was applied: %v", err)
	}
	if a, err := store.Lookup("email", "a@x.dev"); err != nil || a.ID != "acc_1" {
		t.Fatalf("a refused Delete or Update was applied: %+v, %v", a, err)
	}
	//: the unique key the refused Put wanted is still free.
	must(t, store.Insert(account{ID: "acc_3", Email: "b@x.dev"}))
	if calls != 1 {
		t.Fatalf("the hooks were called %d times, want once, for acc_3", calls)
	}
}

// TestAWriteTheDiskCouldNotConfirmStands pins WriteUnconfirmed: the rename
// happened, so the write took effect — in memory, for the hooks, and on the
// next open — and the caller is told its durability is in doubt.
func TestAWriteTheDiskCouldNotConfirmStands(t *testing.T) {
	t.Parallel()
	faulty := &faultyFS{FullFS: memFS()}
	store, err := openWith(accountConfig(faulty))
	must(t, err)
	var written []string
	store.OnWrite(func(key string) { written = append(written, key) })
	faulty.set("", false, true)
	requireCode(t, store.Put(account{ID: "acc_1", Email: "a@x.dev"}), docstore.CodeWriteUnconfirmed, "an unconfirmed Put")
	faulty.set("", false, false)
	if a, err := store.Lookup("email", "a@x.dev"); err != nil || a.ID != "acc_1" {
		t.Fatalf("an unconfirmed write was not applied: %+v, %v", a, err)
	}
	if !slices.Equal(written, []string{"acc_1"}) {
		t.Fatalf("OnWrite saw %v", written)
	}
	must(t, store.Close())
	if got := documentIDs(t, faulty); !slices.Equal(got, []string{"acc_1"}) {
		t.Fatalf("after a reopen the store holds %v", got)
	}
}

// TestAFoldThatFailsKeepsItsEntries pins the automatic fold's failure: the
// write that triggered it succeeds, Stats reports the failure, the entries
// stay, and a reopen loads everything.
func TestAFoldThatFailsKeepsItsEntries(t *testing.T) {
	t.Parallel()
	faulty := &faultyFS{FullFS: memFS()}
	cfg := accountConfig(faulty)
	cfg.FoldAt = 2
	store, err := openWith(cfg)
	must(t, err)
	faulty.set(snapshotPath, true, false)
	must(t, store.Put(account{ID: "acc_1"}))
	must(t, store.Put(account{ID: "acc_2"}))
	stats := store.Stats()
	if !errs.HasCode(stats.FoldError, docstore.CodePersistFailed) || stats.Pending != 2 {
		t.Fatalf("after a failed fold: %+v", stats)
	}
	requireCode(t, store.Close(), docstore.CodePersistFailed, "Close over a snapshot the disk refuses")
	faulty.set("", false, false)
	if got := documentIDs(t, faulty); !slices.Equal(got, []string{"acc_1", "acc_2"}) {
		t.Fatalf("after a reopen the store holds %v", got)
	}
}

// TestOnTheDisk runs the store over the operating system's filesystem — every
// OS the SDK's disk filesystem runs on — and checks what is on the device:
// the snapshot 0600, the overlay directory 0700, the documents back after a
// reopen.
func TestOnTheDisk(t *testing.T) {
	t.Parallel()
	fsys, root := diskFS(t)
	store, err := openWith(accountConfig(fsys))
	must(t, err)
	must(t, store.Put(account{ID: "acc_1", Email: "a@x.dev", Teams: []string{"red"}}))
	crashed := documentIDs(t, fsys)
	if !slices.Equal(crashed, []string{"acc_1"}) {
		t.Fatalf("a second open saw %v", crashed)
	}
	must(t, store.Close())
	if got := documentIDs(t, fsys); !slices.Equal(got, []string{"acc_1"}) {
		t.Fatalf("after a reopen the store holds %v", got)
	}
	//: modes mean nothing on Windows, where vfs.NewOS refuses anyway.
	if runtime.GOOS == "windows" {
		return
	}
	snapshot, err := os.Stat(filepath.Join(root, "members", "accounts.json"))
	must(t, err)
	if perm := snapshot.Mode().Perm(); perm != 0o600 {
		t.Errorf("the snapshot is %o, want 600", perm)
	}
	overlay, err := os.Stat(filepath.Join(root, "members", "accounts.json.d"))
	must(t, err)
	if perm := overlay.Mode().Perm(); perm != 0o700 || !overlay.IsDir() {
		t.Errorf("the overlay is %v, want a 700 directory", overlay.Mode())
	}
}
