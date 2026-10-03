// Package docstore_test — a document's versions in the memory and file
// stores: numbered, stamped, pruned in the write that stores the document,
// kept from pruning by a hold, rewritten for an erasure, and never separated
// from their document by a crash.
package docstore_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/docstore"
)

// versionsPath is where the accounts store keeps its versions file.
const versionsPath = snapshotPath + ".versions"

// epoch is the instant every versioned case's clock starts at.
var epoch = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

// versionedConfig is the accounts store's configuration over fsys — in memory
// when fsys is nil — keeping versions former versions, on clk.
func versionedConfig(fsys corevfs.FullFS, versions int, clk clock.Clock) docstore.Config[account] {
	cfg := accountConfig(fsys)
	cfg.Versions, cfg.Clock = versions, clk
	return cfg
}

// openVersioned opens cfg and closes it when the test ends.
func openVersioned(t *testing.T, cfg docstore.Config[account]) *docstore.Store[account] {
	t.Helper()
	store, err := openWith(cfg)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	t.Cleanup(func() {
		if closeErr := store.Close(); closeErr != nil && !errs.HasCode(closeErr, docstore.CodeStoreClosed) {
			t.Errorf("Close() = %v", closeErr)
		}
	})
	return store
}

// versionsOf reads key's versions, failing the test on an error.
func versionsOf(t *testing.T, store *docstore.Store[account], key string) []docstore.VersionValue {
	t.Helper()
	all, err := store.Versions(key)
	if err != nil {
		t.Fatalf("Versions(%s) = %v", key, err)
	}
	return all
}

// numbers returns the versions' numbers, in order.
func numbers(all []docstore.VersionValue) []uint64 {
	out := make([]uint64, len(all))
	for i, v := range all {
		out[i] = v.Number
	}
	return out
}

// namesIn decodes each version and returns the names they hold, in order.
func namesIn(t *testing.T, all []docstore.VersionValue) []string {
	t.Helper()
	out := make([]string, len(all))
	for i, v := range all {
		var a account
		if err := json.Unmarshal(v.JSON, &a); err != nil {
			t.Fatalf("version %d is not an account: %v", v.Number, err)
		}
		out[i] = a.Name
	}
	return out
}

// eachBackend runs fn over a memory store and over a store on an in-memory
// filesystem, each in a subtest of its own.
func eachBackend(t *testing.T, fn func(t *testing.T, fsys corevfs.FullFS)) {
	t.Helper()
	for name, fsys := range map[string]func() corevfs.FullFS{"memory": func() corevfs.FullFS { return nil }, "filesystem": memFS} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fn(t, fsys())
		})
	}
}

// TestVersionsAreNumberedStampedAndPruned pins what a write leaves: a
// creation is version 1, every write that changes the document makes the
// next one — with the instant of the store's clock and the stamp's metadata —
// and the oldest beyond Versions are pruned, newest first, the document itself
// always the newest.
func TestVersionsAreNumberedStampedAndPruned(t *testing.T) {
	t.Parallel()
	eachBackend(t, func(t *testing.T, fsys corevfs.FullFS) {
		clk := clock.NewManualClock(epoch)
		store := openVersioned(t, versionedConfig(fsys, 2, clk))
		must(t, store.InsertStamped(account{ID: "acc_1", Name: "v1"}, docstore.StampValue{Meta: map[string]string{"by": "ada"}}))
		clk.Advance(time.Minute)
		must(t, store.Put(account{ID: "acc_1", Name: "v2"}))
		clk.Advance(time.Minute)
		_, err := store.UpdateStamped("acc_1", docstore.StampValue{Meta: map[string]string{"by": "grace", "command": "rename"}},
			func(a *account) error { a.Name = "v3"; return nil })
		must(t, err)
		clk.Advance(time.Minute)
		must(t, store.ReplaceStamped(account{ID: "acc_1", Name: "v4"}, docstore.StampValue{Meta: map[string]string{"by": "alan"}}))

		all := versionsOf(t, store, "acc_1")
		if got := numbers(all); !slices.Equal(got, []uint64{4, 3, 2}) {
			t.Fatalf("numbers = %v, want the current 4 and the two newest former ones", got)
		}
		if got := namesIn(t, all); !slices.Equal(got, []string{"v4", "v3", "v2"}) {
			t.Fatalf("the versions hold %v", got)
		}
		for i, want := range []time.Time{epoch.Add(3 * time.Minute), epoch.Add(2 * time.Minute), epoch.Add(time.Minute)} {
			if !all[i].At.Equal(want) || all[i].At.Location() != time.UTC {
				t.Fatalf("version %d was made at %v, want %v in UTC", all[i].Number, all[i].At, want)
			}
		}
		if !maps.Equal(all[0].Meta, map[string]string{"by": "alan"}) ||
			!maps.Equal(all[1].Meta, map[string]string{"by": "grace", "command": "rename"}) || all[2].Meta != nil {
			t.Fatalf("the stamps are %v, %v, %v", all[0].Meta, all[1].Meta, all[2].Meta)
		}
		//: the current version is the document.
		if doc, getErr := store.Get("acc_1"); getErr != nil || doc.Name != "v4" {
			t.Fatalf("Get() = %+v, %v", doc, getErr)
		}
		one, err := store.Version("acc_1", 3)
		if err != nil || one.Number != 3 || !strings.Contains(string(one.JSON), `"v3"`) {
			t.Fatalf("Version(3) = %+v, %v", one, err)
		}
		_, err = store.Version("acc_1", 1)
		requireCode(t, err, docstore.CodeVersionNotFound, "a pruned version")
		quotesNothing(t, err, "acc_1")
		_, err = store.Version("acc_404", 1)
		requireCode(t, err, docstore.CodeDocumentNotFound, "a version of no document")
		//: a read is a copy.
		all[0].JSON[0], all[0].Meta["by"] = '[', "mallory"
		if again := versionsOf(t, store, "acc_1"); again[0].JSON[0] != '{' || again[0].Meta["by"] != "alan" {
			t.Fatalf("a read aliased the store: %s %v", again[0].JSON, again[0].Meta)
		}
	})
}

// TestAWriteThatChangesNothingMakesNoVersion pins the two writes that keep the
// current version: one storing the JSON already stored, and one stamped
// InPlace — which changes the document and keeps the version's number,
// instant and metadata. A creation is version 1 even stamped InPlace.
func TestAWriteThatChangesNothingMakesNoVersion(t *testing.T) {
	t.Parallel()
	eachBackend(t, func(t *testing.T, fsys corevfs.FullFS) {
		clk := clock.NewManualClock(epoch)
		store := openVersioned(t, versionedConfig(fsys, 5, clk))
		must(t, store.InsertStamped(account{ID: "acc_1", Name: "draft"}, docstore.StampValue{InPlace: true, Meta: map[string]string{"by": "ada"}}))
		clk.Advance(time.Hour)
		must(t, store.Put(account{ID: "acc_1", Name: "draft"}))
		_, err := store.Update("acc_1", func(*account) error { return nil })
		must(t, err)
		must(t, store.ReplaceStamped(account{ID: "acc_1", Name: "published"}, docstore.StampValue{InPlace: true, Meta: map[string]string{"by": "workflow"}}))
		all := versionsOf(t, store, "acc_1")
		if len(all) != 1 || all[0].Number != 1 || !all[0].At.Equal(epoch) || all[0].Meta["by"] != "ada" {
			t.Fatalf("versions = %+v, want version 1 as the creation made it", all)
		}
		if got := namesIn(t, all); !slices.Equal(got, []string{"published"}) {
			t.Fatalf("the current version holds %v, want the document as the in-place write left it", got)
		}
		must(t, store.Put(account{ID: "acc_1", Name: "edited"}))
		if got := namesIn(t, versionsOf(t, store, "acc_1")); !slices.Equal(got, []string{"edited", "published"}) {
			t.Fatalf("after an edit the versions hold %v", got)
		}
	})
}

// TestVersionsAreNeverIndexed pins that Lookup and Find read the current
// version only: an e-mail a former version holds files nothing, and another
// document may take it.
func TestVersionsAreNeverIndexed(t *testing.T) {
	t.Parallel()
	eachBackend(t, func(t *testing.T, fsys corevfs.FullFS) {
		store := openVersioned(t, versionedConfig(fsys, 3, nil))
		must(t, store.Put(account{ID: "acc_1", Email: "old@x.dev", Teams: []string{"red"}}))
		must(t, store.Put(account{ID: "acc_1", Email: "new@x.dev", Teams: []string{"blue"}}))
		_, err := store.Lookup("email", "old@x.dev")
		requireCode(t, err, docstore.CodeDocumentNotFound, "Lookup of a former version's e-mail")
		if red, findErr := store.Find("team", "red"); findErr != nil || len(red) != 0 {
			t.Fatalf("Find of a former version's team = %v, %v", ids(red), findErr)
		}
		must(t, store.Insert(account{ID: "acc_2", Email: "old@x.dev"}))
	})
}

// TestADeletionTakesTheVersions pins that a deleted document leaves no
// version, and that one inserted again under its key is a new document,
// numbered from 1.
func TestADeletionTakesTheVersions(t *testing.T) {
	t.Parallel()
	eachBackend(t, func(t *testing.T, fsys corevfs.FullFS) {
		store := openVersioned(t, versionedConfig(fsys, 3, nil))
		must(t, store.Put(account{ID: "acc_1", Name: "a"}))
		must(t, store.Put(account{ID: "acc_1", Name: "b"}))
		must(t, store.Delete("acc_1"))
		_, err := store.Versions("acc_1")
		requireCode(t, err, docstore.CodeDocumentNotFound, "Versions of a deleted document")
		must(t, store.Insert(account{ID: "acc_1", Name: "again"}))
		if got := numbers(versionsOf(t, store, "acc_1")); !slices.Equal(got, []uint64{1}) {
			t.Fatalf("a document inserted again has versions %v, want 1", got)
		}
	})
}

// TestAHeldDocumentKeepsItsVersions pins the legal hold: while Held says so,
// no write prunes, and the versions pile up beyond Versions; the first write
// after the release prunes them — even one that makes no version. Held is
// asked only by a write that would prune.
func TestAHeldDocumentKeepsItsVersions(t *testing.T) {
	t.Parallel()
	eachBackend(t, func(t *testing.T, fsys corevfs.FullFS) {
		var mu sync.Mutex
		held, asked := map[string]bool{"acc_1": true}, 0
		cfg := versionedConfig(fsys, 1, nil)
		cfg.Held = func(key string) bool {
			mu.Lock()
			defer mu.Unlock()
			asked++
			return held[key]
		}
		store := openVersioned(t, cfg)
		must(t, store.Put(account{ID: "acc_1", Name: "v1"}))
		must(t, store.Put(account{ID: "acc_1", Name: "v2"}))
		mu.Lock()
		askedBefore := asked
		mu.Unlock()
		if askedBefore != 0 {
			t.Fatalf("Held was asked %d times with nothing to prune", askedBefore)
		}
		for _, name := range []string{"v3", "v4", "v5"} {
			must(t, store.Put(account{ID: "acc_1", Name: name}))
		}
		if got := numbers(versionsOf(t, store, "acc_1")); !slices.Equal(got, []uint64{5, 4, 3, 2, 1}) {
			t.Fatalf("a held document keeps %v, want every version", got)
		}
		//: the hold is lifted; a write that makes no version prunes all the same.
		mu.Lock()
		held["acc_1"] = false
		mu.Unlock()
		must(t, store.Put(account{ID: "acc_1", Name: "v5"}))
		if got := numbers(versionsOf(t, store, "acc_1")); !slices.Equal(got, []uint64{5, 4}) {
			t.Fatalf("after the release the document keeps %v, want 5 and 4", got)
		}
	})
}

// TestRewriteVersions pins the erasure's rewrite: the former versions,
// newest first, cleared or dropped by the caller's function in one durable
// write that calls no hook — the current version untouched — and every result
// a rewrite may not return refused, changing nothing.
func TestRewriteVersions(t *testing.T) {
	t.Parallel()
	eachBackend(t, func(t *testing.T, fsys corevfs.FullFS) {
		store := openVersioned(t, versionedConfig(fsys, 5, clock.NewManualClock(epoch)))
		for _, email := range []string{"a@x.dev", "b@x.dev", "c@x.dev", "d@x.dev"} {
			must(t, store.Put(account{ID: "acc_1", Name: "Ada", Email: email}))
		}
		hooked := 0
		store.OnWrite(func(string) { hooked++ })
		must(t, store.RewriteVersions("acc_1", func(former []docstore.VersionValue) ([]docstore.VersionValue, error) {
			if got := numbers(former); !slices.Equal(got, []uint64{3, 2, 1}) {
				t.Fatalf("the rewrite was given %v, want the former versions newest first", got)
			}
			kept := []docstore.VersionValue{former[0], former[2]}
			for i := range kept {
				kept[i].JSON = json.RawMessage(`{"id": "acc_1", "name": "[erased]"}`)
				kept[i].Meta = map[string]string{"erased": "yes"}
			}
			return kept, nil
		}))
		all := versionsOf(t, store, "acc_1")
		if got := numbers(all); !slices.Equal(got, []uint64{4, 3, 1}) {
			t.Fatalf("after the rewrite the versions are %v, want 4, 3 and 1", got)
		}
		if got := namesIn(t, all); !slices.Equal(got, []string{"Ada", "[erased]", "[erased]"}) {
			t.Fatalf("after the rewrite the versions hold %v", got)
		}
		if string(all[1].JSON) != `{"id":"acc_1","name":"[erased]"}` || all[1].Meta["erased"] != "yes" {
			t.Fatalf("a rewritten version = %s %v, want its JSON compact and its metadata as rewritten", all[1].JSON, all[1].Meta)
		}
		if hooked != 0 {
			t.Fatalf("a rewrite called OnWrite %d times", hooked)
		}
		refusals := map[string]func([]docstore.VersionValue) []docstore.VersionValue{
			"a version it was not given": func(f []docstore.VersionValue) []docstore.VersionValue {
				f[0].Number = 7
				return f
			},
			"out of order": func(f []docstore.VersionValue) []docstore.VersionValue { return []docstore.VersionValue{f[1], f[0]} },
			"another instant": func(f []docstore.VersionValue) []docstore.VersionValue {
				f[1].At = f[1].At.Add(time.Second)
				return f
			},
			"not JSON": func(f []docstore.VersionValue) []docstore.VersionValue {
				f[0].JSON = json.RawMessage(`{"id": "s3cr3t`)
				return f
			},
		}
		for name, rewrite := range refusals {
			err := store.RewriteVersions("acc_1", func(f []docstore.VersionValue) ([]docstore.VersionValue, error) { return rewrite(f), nil })
			requireCode(t, err, docstore.CodeVersionsRewriteRefused, name)
			quotesNothing(t, err, "s3cr3t", "acc_1")
		}
		refusal := errors.New("the caller's own refusal")
		if err := store.RewriteVersions("acc_1", func([]docstore.VersionValue) ([]docstore.VersionValue, error) { return nil, refusal }); !errors.Is(err, refusal) {
			t.Fatalf("a failing rewrite = %v, want the function's own error", err)
		}
		if got := numbers(versionsOf(t, store, "acc_1")); !slices.Equal(got, []uint64{4, 3, 1}) {
			t.Fatalf("a refused rewrite changed the versions: %v", got)
		}
		requireCode(t, store.RewriteVersions("acc_404", func(f []docstore.VersionValue) ([]docstore.VersionValue, error) { return f, nil }),
			docstore.CodeDocumentNotFound, "a rewrite of no document")
	})
}

// TestAStoreWithoutVersions pins the zero value: a store that keeps no
// versions refuses every read and rewrite of them, accepts stamped writes as
// plain ones, and writes exactly the entry it wrote before versions existed.
func TestAStoreWithoutVersions(t *testing.T) {
	t.Parallel()
	fsys := memFS()
	cfg := accountConfig(fsys)
	cfg.FoldAt = -1
	store := openVersioned(t, cfg)
	must(t, store.PutStamped(account{ID: "acc_1", Name: "Ada"}, docstore.StampValue{Meta: map[string]string{"by": "ada"}}))
	_, err := store.Versions("acc_1")
	requireCode(t, err, docstore.CodeVersionsNotKept, "Versions")
	_, err = store.Version("acc_1", 1)
	requireCode(t, err, docstore.CodeVersionsNotKept, "Version")
	err = store.RewriteVersions("acc_1", func(f []docstore.VersionValue) ([]docstore.VersionValue, error) { return f, nil })
	requireCode(t, err, docstore.CodeVersionsNotKept, "RewriteVersions")
	entry := readFile(t, fsys, overlayPath+"/"+entryNameOf(t, "acc_1"))
	if entry != `{"doc":{"id":"acc_1","name":"Ada"},"key":"acc_1"}` {
		t.Fatalf("a store without versions wrote the entry %s", entry)
	}
	must(t, store.Close())
	if _, statErr := fsys.Open(versionsPath); statErr == nil {
		t.Fatalf("a store without versions wrote %s", versionsPath)
	}
}

// TestVersionsConfigurationRefusals pins the two settings refused before
// anything is read: a negative Versions, and a Held on a store that keeps no
// versions, which would hold nothing.
func TestVersionsConfigurationRefusals(t *testing.T) {
	t.Parallel()
	negative := versionedConfig(nil, -1, nil)
	_, err := openWith(negative)
	requireCode(t, err, docstore.CodeStoreMisconfigured, "a negative Versions")
	heldWithout := accountConfig(nil)
	heldWithout.Held = func(string) bool { return true }
	_, err = openWith(heldWithout)
	requireCode(t, err, docstore.CodeStoreMisconfigured, "Held without Versions")
	if setting := fieldValue(errs.FieldsOf(err), "setting"); setting != "Held" {
		t.Fatalf("the refusal names %q, want Held", setting)
	}
}

// TestVersionsUnderContention pins the versions under the race detector:
// writers stamping, updating, rewriting and deleting shared documents while
// readers read their versions, on both engines. Whatever the interleaving,
// every document ends with its current version first, numbers strictly
// decreasing, and no more former versions than the store keeps.
func TestVersionsUnderContention(t *testing.T) {
	t.Parallel()
	eachBackend(t, func(t *testing.T, fsys corevfs.FullFS) {
		cfg := versionedConfig(fsys, 3, nil)
		cfg.FoldAt = 8
		store := openVersioned(t, cfg)
		var wg sync.WaitGroup
		for worker := range 8 {
			wg.Go(func() {
				for i := range 40 {
					key := fmt.Sprintf("acc_%d", i%4)
					stamp := docstore.StampValue{Meta: map[string]string{"by": strconv.Itoa(worker)}}
					tolerate(t, store.PutStamped(account{ID: key, Name: fmt.Sprintf("w%d-%d", worker, i)}, stamp))
					_, err := store.UpdateStamped(key, stamp, func(a *account) error { a.Teams = []string{strconv.Itoa(i)}; return nil })
					tolerate(t, err, docstore.CodeDocumentNotFound)
					_, err = store.Versions(key)
					tolerate(t, err, docstore.CodeDocumentNotFound)
					err = store.RewriteVersions(key, func(f []docstore.VersionValue) ([]docstore.VersionValue, error) { return f[:len(f)/2], nil })
					tolerate(t, err, docstore.CodeDocumentNotFound)
					if i%9 == 0 {
						tolerate(t, store.Delete(key), docstore.CodeDocumentNotFound)
					}
				}
			})
		}
		wg.Wait()
		all, err := store.List()
		must(t, err)
		for _, a := range all {
			versions := versionsOf(t, store, a.ID)
			if len(versions) > 4 {
				t.Fatalf("%s keeps %d versions, more than the current one and three former ones", a.ID, len(versions))
			}
			for i := 1; i < len(versions); i++ {
				if versions[i].Number >= versions[i-1].Number {
					t.Fatalf("%s's versions are numbered %v, not strictly decreasing", a.ID, numbers(versions))
				}
			}
			var current account
			must(t, json.Unmarshal(versions[0].JSON, &current))
			if current.Name != a.Name || !slices.Equal(current.Teams, a.Teams) {
				t.Fatalf("%s's current version holds %+v, the document %+v", a.ID, current, a)
			}
		}
	})
}
