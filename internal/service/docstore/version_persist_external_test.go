// Package docstore_test — a document's versions in the files: in the
// document's own overlay entry, pruned there, folded into a file of their own,
// loaded back as written, and never ahead of or behind their document
// whatever point a crash interrupted.
package docstore_test

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	corevfs "github.com/kitsunium/sdk/internal/core/vfs"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/docstore"
)

// crashVersioned opens a store keeping versions former versions over fsys,
// and abandons it without Close once fn has run against it.
func crashVersioned(t *testing.T, fsys corevfs.FullFS, versions int, fn func(store *docstore.Store[account])) {
	t.Helper()
	cfg := versionedConfig(fsys, versions, clock.NewManualClock(epoch))
	cfg.FoldAt = -1
	store, err := openWith(cfg)
	must(t, err)
	fn(store)
}

// reopenedVersions opens a store keeping versions former versions over fsys
// and returns every document's versions, by key, closing it after.
func reopenedVersions(t *testing.T, fsys corevfs.FullFS, versions int) map[string][]docstore.VersionValue {
	t.Helper()
	store, err := openWith(versionedConfig(fsys, versions, nil))
	must(t, err)
	defer func() { must(t, store.Close()) }()
	all, err := store.List()
	must(t, err)
	out := make(map[string][]docstore.VersionValue, len(all))
	for _, a := range all {
		out[a.ID] = versionsOf(t, store, a.ID)
	}
	return out
}

// sameVersions fails the test unless got and want hold the same documents
// with the same versions: numbers, instants, metadata and JSON.
func sameVersions(t *testing.T, what string, got, want map[string][]docstore.VersionValue) {
	t.Helper()
	if !slices.Equal(slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want))) {
		t.Fatalf("%s: the documents are %v, want %v", what, slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
	}
	for key, versions := range want {
		if !slices.EqualFunc(got[key], versions, func(a, b docstore.VersionValue) bool {
			return a.Number == b.Number && a.At.Equal(b.At) && maps.Equal(a.Meta, b.Meta) && string(a.JSON) == string(b.JSON)
		}) {
			t.Fatalf("%s: %s has versions\n%+v\nwant\n%+v", what, key, got[key], versions)
		}
	}
}

// history writes the history most persistence cases start from: two
// documents, one edited three times and stamped, one deleted, one created.
func history(t *testing.T, store *docstore.Store[account], clk *clock.ManualClock) {
	t.Helper()
	must(t, store.PutStamped(account{ID: "acc_1", Name: "first"}, docstore.StampValue{Meta: map[string]string{"by": "ada"}}))
	must(t, store.Put(account{ID: "acc_2", Name: "doomed"}))
	for _, name := range []string{"second", "third", "fourth"} {
		if clk != nil {
			clk.Advance(time.Minute)
		}
		must(t, store.PutStamped(account{ID: "acc_1", Name: name}, docstore.StampValue{Meta: map[string]string{"by": name}}))
	}
	must(t, store.Delete("acc_2"))
	must(t, store.Put(account{ID: "acc_3", Name: "third doc"}))
}

// TestVersionsAreDurableWithTheirDocument pins the promise for versions: once
// a write returns, a process that dies loses neither the document nor its
// versions — the entry holds both — and the pruning the write made is in that
// same entry.
func TestVersionsAreDurableWithTheirDocument(t *testing.T) {
	t.Parallel()
	fsys := memFS()
	var before map[string][]docstore.VersionValue
	crashVersioned(t, fsys, 2, func(store *docstore.Store[account]) {
		history(t, store, nil)
		before = map[string][]docstore.VersionValue{"acc_1": versionsOf(t, store, "acc_1"), "acc_3": versionsOf(t, store, "acc_3")}
		//: the write's own entry already holds the pruned versions.
		var entry struct {
			Versions struct {
				Former []struct {
					Number uint64 `json:"number"`
				} `json:"former"`
				Current struct {
					Number uint64 `json:"number"`
				} `json:"current"`
			} `json:"versions"`
		}
		must(t, json.Unmarshal([]byte(readFile(t, fsys, overlayPath+"/"+entryNameOf(t, "acc_1"))), &entry))
		if entry.Versions.Current.Number != 4 || len(entry.Versions.Former) != 2 || entry.Versions.Former[0].Number != 3 {
			t.Fatalf("the entry of the fourth write holds %+v, want version 4 and the former 3 and 2", entry.Versions)
		}
	})
	if got := numbers(before["acc_1"]); !slices.Equal(got, []uint64{4, 3, 2}) {
		t.Fatalf("before the crash acc_1 had %v", got)
	}
	sameVersions(t, "after a crash", reopenedVersions(t, fsys, 2), before)
}

// TestAFoldInterruptedAnywhereKeepsVersionsWithTheirDocuments pins the fold's
// two publications: the versions file, then the snapshot, then the removals.
// A fold stopped after the first, before the second — or before either — or
// after both with some or all of its entries left, loads exactly the documents
// and the versions a clean fold loads: an entry holds its key's document and
// versions together, and a key without one is the same in the old files as in
// the new.
func TestAFoldInterruptedAnywhereKeepsVersionsWithTheirDocuments(t *testing.T) {
	t.Parallel()
	clean := memFS()
	crashVersioned(t, clean, 2, func(store *docstore.Store[account]) {
		history(t, store, clock.NewManualClock(epoch))
		must(t, store.Fold())
	})
	want := reopenedVersions(t, clean, 2)
	for _, c := range []struct {
		name string
		// refused is the file whose publication the fold's filesystem refuses.
		refused string
		// kept is how many folded entries survive a fold that completed: -1 all.
		kept int
	}{
		{"the snapshot refused after the versions file was written", snapshotPath, 0},
		{"the versions file refused, nothing written", versionsPath, 0},
		{"every folded entry survives", "", -1},
		{"one folded entry survives", "", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			faulty := &faultyFS{FullFS: memFS()}
			var saved map[string][]byte
			crashVersioned(t, faulty, 2, func(store *docstore.Store[account]) {
				//: an earlier fold, so both files exist and are older.
				must(t, store.Put(account{ID: "acc_0", Name: "folded earlier"}))
				must(t, store.Fold())
				must(t, store.Delete("acc_0"))
				history(t, store, clock.NewManualClock(epoch))
				saved = saveOverlay(t, faulty)
				if c.refused != "" {
					faulty.set(c.refused, true, false)
					requireCode(t, store.Fold(), docstore.CodePersistFailed, "a fold whose publication is refused")
					faulty.set("", false, false)
					return
				}
				must(t, store.Fold())
			})
			restored := 0
			for _, name := range slices.Sorted(maps.Keys(saved)) {
				if c.refused != "" || (c.kept >= 0 && restored >= c.kept) {
					break
				}
				must(t, faulty.WriteAtomic(overlayPath+"/"+name, saved[name], 0o600))
				restored++
			}
			sameVersions(t, c.name, reopenedVersions(t, faulty, 2), want)
		})
	}
}

// TestAWriteTheDiskRefusedChangesNoVersion pins PersistFailed for versions:
// the refused write changed neither the document nor its versions, in memory
// or on the next open.
func TestAWriteTheDiskRefusedChangesNoVersion(t *testing.T) {
	t.Parallel()
	faulty := &faultyFS{FullFS: memFS()}
	store := openVersioned(t, versionedConfig(faulty, 3, nil))
	must(t, store.Put(account{ID: "acc_1", Name: "kept"}))
	faulty.set("", true, false)
	requireCode(t, store.Put(account{ID: "acc_1", Name: "refused"}), docstore.CodePersistFailed, "a refused Put")
	err := store.RewriteVersions("acc_1", func([]docstore.VersionValue) ([]docstore.VersionValue, error) { return nil, nil })
	requireCode(t, err, docstore.CodePersistFailed, "a refused rewrite")
	faulty.set("", false, false)
	if got := namesIn(t, versionsOf(t, store, "acc_1")); !slices.Equal(got, []string{"kept"}) {
		t.Fatalf("after a refused write the versions hold %v", got)
	}
	must(t, store.Close())
	if got := namesIn(t, reopenedVersions(t, faulty, 3)["acc_1"]); !slices.Equal(got, []string{"kept"}) {
		t.Fatalf("after a reopen the versions hold %v", got)
	}
}

// TestTheVersionsFile pins the resting state: after Close the snapshot is the
// bare object it always was, the versions live in a file of their own beside
// it, and a reopen reads back every version as it was — after which a write
// of the same document makes none, although the snapshot indents it.
func TestTheVersionsFile(t *testing.T) {
	t.Parallel()
	fsys := memFS()
	clk := clock.NewManualClock(epoch)
	store, err := openWith(versionedConfig(fsys, 2, clk))
	must(t, err)
	history(t, store, clk)
	before := map[string][]docstore.VersionValue{"acc_1": versionsOf(t, store, "acc_1"), "acc_3": versionsOf(t, store, "acc_3")}
	must(t, store.Close())
	var snapshot map[string]account
	must(t, json.Unmarshal([]byte(readFile(t, fsys, snapshotPath)), &snapshot))
	if len(snapshot) != 2 || snapshot["acc_1"].Name != "fourth" {
		t.Fatalf("the snapshot is %+v", snapshot)
	}
	versionsFile := readFile(t, fsys, versionsPath)
	var records map[string]json.RawMessage
	must(t, json.Unmarshal([]byte(versionsFile), &records))
	if len(records) != 2 || !strings.Contains(versionsFile, `"number": 4`) || !strings.Contains(versionsFile, `"by": "third"`) {
		t.Fatalf("the versions file is\n%s", versionsFile)
	}
	sameVersions(t, "after a reopen", reopenedVersions(t, fsys, 2), before)
	store, err = openWith(versionedConfig(fsys, 2, clk))
	must(t, err)
	must(t, store.Put(account{ID: "acc_1", Name: "fourth"}))
	if got := numbers(versionsOf(t, store, "acc_1")); !slices.Equal(got, []uint64{4, 3, 2}) {
		t.Fatalf("a write of the same document after a reopen left %v", got)
	}
	must(t, store.Close())
}

// TestADocumentStoredBeforeVersionsIsVersionOne pins a store that begins to
// keep versions: each document it already holds is version 1, made at an
// instant nobody recorded, until its first write makes version 2.
func TestADocumentStoredBeforeVersionsIsVersionOne(t *testing.T) {
	t.Parallel()
	fsys := memFS()
	plain, err := openWith(accountConfig(fsys))
	must(t, err)
	must(t, plain.Put(account{ID: "acc_1", Name: "before"}))
	must(t, plain.Close())
	store := openVersioned(t, versionedConfig(fsys, 3, clock.NewManualClock(epoch)))
	all := versionsOf(t, store, "acc_1")
	if len(all) != 1 || all[0].Number != 1 || !all[0].At.IsZero() || all[0].Meta != nil {
		t.Fatalf("a document stored before versions = %+v, want version 1 at an unknown instant", all)
	}
	must(t, store.Put(account{ID: "acc_1", Name: "after"}))
	all = versionsOf(t, store, "acc_1")
	if got := numbers(all); !slices.Equal(got, []uint64{2, 1}) || !all[0].At.Equal(epoch) || !all[1].At.IsZero() {
		t.Fatalf("after its first versioned write = %+v", all)
	}
}

// TestFilesThatKeepVersionsTheStoreDoesNot pins the refusal of a store opened
// without versions over files that keep them — the versions file, or an entry
// a crash left — rather than a store that would leave a deleted document's
// versions to the next one stored under its key and drop the rest at its next
// fold. The files stay as they were.
func TestFilesThatKeepVersionsTheStoreDoesNot(t *testing.T) {
	t.Parallel()
	folded := memFS()
	store, err := openWith(versionedConfig(folded, 2, nil))
	must(t, err)
	must(t, store.Put(account{ID: "acc_1"}))
	must(t, store.Close())
	//: an entry a crash left, with the versions file removed as the first
	//: refusal says to: the entry still keeps versions.
	crashed := memFS()
	crashVersioned(t, crashed, 2, func(store *docstore.Store[account]) { must(t, store.Put(account{ID: "acc_1"})) })
	must(t, crashed.Remove(versionsPath))
	for name, c := range map[string]struct {
		fsys corevfs.FullFS
		file string
	}{
		"the versions file":         {folded, versionsPath},
		"an entry keeping versions": {crashed, overlayPath + "/" + entryNameOf(t, "acc_1")},
	} {
		before := readFile(t, c.fsys, c.file)
		_, err := openWith(accountConfig(c.fsys))
		requireCode(t, err, docstore.CodeLoadFailed, name)
		if file := fieldValue(errs.FieldsOf(err), "file"); file != c.file {
			t.Fatalf("%s: the refusal names %q, want %q", name, file, c.file)
		}
		if after := readFile(t, c.fsys, c.file); after != before {
			t.Fatalf("%s: the refused open changed the file", name)
		}
	}
}

// TestVersionsFilesThatAreNotAStores pins LoadFailed for every versions file
// or entry no store wrote — and that no refusal quotes what it held.
func TestVersionsFilesThatAreNotAStores(t *testing.T) {
	t.Parallel()
	const secret = "s3cr3t-in-the-file"
	snapshot := `{"acc_1": {"id": "acc_1"}}`
	entryName := entryNameOf(t, "acc_1")
	former := `{"number": 1, "doc": {"id": "` + secret + `"}}`
	for _, c := range []struct {
		name  string
		files map[string]string
	}{
		{"a versions file that is not JSON", map[string]string{versionsPath: "{" + secret}},
		{"a versions file that is null", map[string]string{versionsPath: "null"}},
		{"versions under the empty key", map[string]string{versionsPath: `{"": {"current": {"number": 1}}}`}},
		{"versions that are null", map[string]string{versionsPath: `{"acc_1": null}`}},
		{"a version numbered 0", map[string]string{versionsPath: `{"acc_1": {"current": {"number": 0}}}`}},
		{"a former version numbered 0", map[string]string{versionsPath: `{"acc_1": {"current": {"number": 2}, "former": [{"number": 0, "doc": {"id": "` + secret + `"}}]}}`}},
		{"versions out of order", map[string]string{versionsPath: `{"acc_1": {"current": {"number": 1}, "former": [` + former + `]}}`}},
		{"a version without a document", map[string]string{versionsPath: `{"acc_1": {"current": {"number": 2}, "former": [{"number": 1}]}}`}},
		{"versions of no document", map[string]string{versionsPath: `{"acc_9": {"current": {"number": 2}, "former": [` + former + `]}}`}},
		{"a deletion keeping versions", map[string]string{overlayPath + "/" + entryName: `{"key": "acc_1", "deleted": true, "versions": {"current": {"number": 2}, "former": [` + former + `]}}`}},
		{"an entry keeping versions out of order", map[string]string{overlayPath + "/" + entryName: `{"key": "acc_1", "doc": {"id": "acc_1"}, "versions": {"current": {"number": 1}, "former": [` + former + `]}}`}},
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
			requireCode(t, err, docstore.CodeLoadFailed, c.name)
			if quotes(err.Error()+errs.PublicOf(err)+errs.PrivateOf(err)+fieldsText(err), secret, "acc_9") {
				t.Errorf("%s: the refusal quotes the file: %s", c.name, fieldsText(err))
			}
		})
	}
}

// TestARewrittenVersionReadsBackAlikeAfterAReopen pins the bytes a rewrite
// stores: the JSON a function returns is compacted and escaped as json.Marshal
// escapes a document, so what Versions answers before a restart is what it
// answers after one, although the files re-escape what they hold.
func TestARewrittenVersionReadsBackAlikeAfterAReopen(t *testing.T) {
	t.Parallel()
	fsys := memFS()
	store, err := openWith(versionedConfig(fsys, 2, nil))
	must(t, err)
	must(t, store.Put(account{ID: "acc_1", Name: "a"}))
	must(t, store.Put(account{ID: "acc_1", Name: "b"}))
	must(t, store.RewriteVersions("acc_1", func(former []docstore.VersionValue) ([]docstore.VersionValue, error) {
		former[0].JSON = json.RawMessage(`{ "id": "acc_1", "name": "<b>&</b>" }`)
		return former, nil
	}))
	before := versionsOf(t, store, "acc_1")
	if want := `{"id":"acc_1","name":"\u003cb\u003e\u0026\u003c/b\u003e"}`; string(before[1].JSON) != want {
		t.Fatalf("the rewritten version is %s, want %s", before[1].JSON, want)
	}
	must(t, store.Close())
	sameVersions(t, "after a reopen", reopenedVersions(t, fsys, 2), map[string][]docstore.VersionValue{"acc_1": before})
}

// TestAnInstantIsKeptToTheNanosecondInTheFiles pins the instant through the
// files: years far from 1970 read back exactly after a reopen, and a clock
// past the year 9999, which RFC 3339 cannot write, fails the write as
// PERSIST_FAILED — nothing changed — rather than keep a wrong instant.
func TestAnInstantIsKeptToTheNanosecondInTheFiles(t *testing.T) {
	t.Parallel()
	fsys := memFS()
	ancient := time.Date(1066, 10, 14, 9, 0, 0, 123456789, time.UTC)
	far := time.Date(2600, 1, 1, 0, 0, 0, 987654321, time.UTC)
	clk := clock.NewManualClock(ancient)
	store, err := openWith(versionedConfig(fsys, 3, clk))
	must(t, err)
	must(t, store.Put(account{ID: "acc_1", Name: "then"}))
	clk.Set(far)
	must(t, store.Put(account{ID: "acc_1", Name: "later"}))
	clk.Set(time.Date(12000, 1, 1, 0, 0, 0, 0, time.UTC))
	requireCode(t, store.Put(account{ID: "acc_1", Name: "too late"}), docstore.CodePersistFailed, "a write stamped past the year 9999")
	must(t, store.Close())
	all := reopenedVersions(t, fsys, 3)["acc_1"]
	if len(all) != 2 || !all[0].At.Equal(far) || !all[1].At.Equal(ancient) {
		t.Fatalf("after a reopen the versions were made at %+v, want %v and %v", all, far, ancient)
	}
}
