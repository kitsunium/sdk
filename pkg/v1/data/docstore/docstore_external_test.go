package docstore_test

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/data/vfs"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// member is the document the facade cases store.
type member struct {
	ID    string   `json:"id"`
	Email string   `json:"email,omitempty"`
	Teams []string `json:"teams,omitempty"`
}

// open opens a members store over fsys through public names only.
func open(t *testing.T, fsys vfs.FullFS) *docstore.Store[member] {
	t.Helper()
	store, err := docstore.Open(
		docstore.Config[member]{Key: func(m member) string { return m.ID }, FS: fsys, Path: "members.json"},
		docstore.Unique("email", func(m member) string { return m.Email }),
		docstore.Index("team", func(m member) []string { return m.Teams }),
	)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	return store
}

// TestFacade pins the store through public names: the write modes and their
// codes, both index reads, and a reopen over the same filesystem that finds
// every document and rebuilds every index.
func TestFacade(t *testing.T) {
	t.Parallel()
	fsys := vfs.NewMem()
	store := open(t, fsys)
	if err := store.Insert(member{ID: "m1", Email: "ada@example.com", Teams: []string{"red"}}); err != nil {
		t.Fatalf("Insert() = %v", err)
	}
	if err := store.Insert(member{ID: "m1"}); !errs.HasCode(err, docstore.CodeDocumentExists) {
		t.Fatalf("a second Insert = %v, want DocumentExists", err)
	}
	if err := store.Put(member{ID: "m2", Email: "ada@example.com"}); !errs.HasCode(err, docstore.CodeUniqueKeyTaken) {
		t.Fatalf("a second holder of a unique key = %v, want UniqueKeyTaken", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	reopened := open(t, fsys)
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("Close() = %v", err)
		}
	}()
	ada, err := reopened.Lookup("email", "ada@example.com")
	if err != nil || ada.ID != "m1" {
		t.Fatalf("Lookup() after a reopen = %+v, %v", ada, err)
	}
	red, err := reopened.Find("team", "red")
	if err != nil || len(red) != 1 {
		t.Fatalf("Find() after a reopen = %v, %v", red, err)
	}
	if _, err := reopened.Get("m2"); !errs.HasCode(err, docstore.CodeDocumentNotFound) {
		t.Fatalf("Get of a refused document = %v, want DocumentNotFound", err)
	}
	var written []string
	reopened.OnWrite(func(key string) { written = append(written, key) })
	if _, err := reopened.Update("m1", func(m *member) error { m.Teams = nil; return nil }); err != nil {
		t.Fatalf("Update() = %v", err)
	}
	if !slices.Equal(written, []string{"m1"}) {
		t.Fatalf("OnWrite saw %v", written)
	}
	if stats := reopened.Stats(); stats.Documents != 1 || stats.Pending != 1 {
		t.Fatalf("Stats() = %+v", stats)
	}
	if docstore.DefaultFoldAt != 1024 {
		t.Fatalf("DefaultFoldAt = %d", docstore.DefaultFoldAt)
	}
}

// TestFacadeInMemory pins a store with no filesystem: nothing is written, and
// a configuration naming a path without one is refused.
func TestFacadeInMemory(t *testing.T) {
	t.Parallel()
	store, err := docstore.Open(docstore.Config[member]{Key: func(m member) string { return m.ID }})
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	if err := store.Put(member{ID: "m1"}); err != nil {
		t.Fatalf("Put() = %v", err)
	}
	entries, err := store.Entries(0)
	if err != nil || len(entries) != 1 || entries[0].Key != "m1" {
		t.Fatalf("Entries() = %+v, %v", entries, err)
	}
	if _, err := docstore.Open(docstore.Config[member]{Key: func(m member) string { return m.ID }, Path: "members.json"}); !errs.HasCode(err, docstore.CodeStoreMisconfigured) {
		t.Fatalf("a path without a filesystem = %v, want StoreMisconfigured", err)
	}
}

// TestFacadeVersions pins a document's versions through public names: kept in
// the write that stores the document, stamped, pruned, read back after a
// reopen, rewritten, and refused under their own codes.
func TestFacadeVersions(t *testing.T) {
	t.Parallel()
	fsys, clk := vfs.NewMem(), clock.NewManualClock(time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC))
	cfg := docstore.Config[member]{Key: func(m member) string { return m.ID }, FS: fsys, Path: "members.json", Versions: 1, Clock: clk}
	store, err := docstore.Open(cfg)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	for _, email := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		clk.Advance(time.Minute)
		if err := store.PutStamped(member{ID: "m1", Email: email}, docstore.Stamp{Meta: map[string]string{"by": email}}); err != nil {
			t.Fatalf("PutStamped() = %v", err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	reopened, err := docstore.Open(cfg)
	if err != nil {
		t.Fatalf("Open() again = %v", err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("Close() = %v", err)
		}
	}()
	versions, err := reopened.Versions("m1")
	if err != nil || len(versions) != 2 || versions[0].Number != 3 || versions[1].Number != 2 || versions[1].Meta["by"] != "b@example.com" {
		t.Fatalf("Versions() after a reopen = %+v, %v", versions, err)
	}
	if _, err := reopened.Version("m1", 1); !errs.HasCode(err, docstore.CodeVersionNotFound) {
		t.Fatalf("a pruned version = %v, want VersionNotFound", err)
	}
	err = reopened.RewriteVersions("m1", func(former []docstore.Version) ([]docstore.Version, error) {
		former[0].JSON = json.RawMessage(`{"id":"m1"}`)
		return former, nil
	})
	if err != nil {
		t.Fatalf("RewriteVersions() = %v", err)
	}
	err = reopened.RewriteVersions("m1", func(former []docstore.Version) ([]docstore.Version, error) {
		former[0].Number = 9
		return former, nil
	})
	if !errs.HasCode(err, docstore.CodeVersionsRewriteRefused) {
		t.Fatalf("a rewrite that renumbers = %v, want VersionsRewriteRefused", err)
	}
	plain := open(t, vfs.NewMem())
	defer func() {
		if err := plain.Close(); err != nil {
			t.Errorf("Close() = %v", err)
		}
	}()
	if _, err := plain.Versions("m1"); !errs.HasCode(err, docstore.CodeVersionsNotKept) {
		t.Fatalf("Versions on a store that keeps none = %v, want VersionsNotKept", err)
	}
	if _, err := docstore.SQLVersionsMigration(sql.DialectSQLite, "members__accounts", 2); err != nil {
		t.Fatalf("SQLVersionsMigration() = %v", err)
	}
}

// The engines implement the ports a caller's double stands in for, checked
// through public names only: a consumer that depends on a port compiles
// against the engine it is handed in production.
var (
	_ docstore.Collection[member]        = (*docstore.Store[member])(nil)
	_ docstore.Versioned[member]         = (*docstore.Store[member])(nil)
	_ docstore.KeyLister                 = (*docstore.Store[member])(nil)
	_ docstore.CollectionContext[member] = (*docstore.SQLStore[member])(nil)
	_ docstore.VersionedContext[member]  = (*docstore.SQLStore[member])(nil)
	_ docstore.KeyListerContext          = (*docstore.SQLStore[member])(nil)
	_ docstore.Announcer                 = (*docstore.SQLStore[member])(nil)
)

// TestFacadeKeys lists a store's keys through public names: in key order,
// resumed after a key, and through the port a double stands in for.
func TestFacadeKeys(t *testing.T) {
	t.Parallel()
	store := open(t, vfs.NewMem())
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() = %v", err)
		}
	}()
	for _, id := range []string{"m3", "m1", "m2"} {
		if err := store.Put(member{ID: id}); err != nil {
			t.Fatalf("Put() = %v", err)
		}
	}
	var lister docstore.KeyLister = store
	var keys []string
	for key, err := range lister.Keys("m1") {
		if err != nil {
			t.Fatalf("Keys() yielded %v", err)
		}
		keys = append(keys, key)
	}
	if !slices.Equal(keys, []string{"m2", "m3"}) {
		t.Fatalf("Keys(after m1) = %q, want [m2 m3]", keys)
	}
}

// TestAStoreServesThroughItsPort holds the file engine as the port a double
// stands in for, and finds through it the documents, the index, the refusal
// and the hook the engine answers directly.
func TestFacadeTrash(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	store, err := docstore.Open(
		docstore.Config[member]{Key: func(m member) string { return m.ID }, Versions: 2, Clock: clock.NewManualClock(at)},
		docstore.Unique("email", func(m member) string { return m.Email }),
	)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	var trash docstore.Trashable = store
	if err := store.Insert(member{ID: "m1", Email: "ada@example.com"}); err != nil {
		t.Fatalf("Insert() = %v", err)
	}
	if err := trash.Trash("m1"); err != nil {
		t.Fatalf("Trash() = %v", err)
	}
	if _, err := store.Lookup("email", "ada@example.com"); !errs.HasCode(err, docstore.CodeDocumentNotFound) {
		t.Fatalf("Lookup of a trashed document = %v, want DocumentNotFound", err)
	}
	tombs, err := trash.Tombstones()
	if err != nil || !slices.Equal(tombs, []docstore.Tombstone{{Key: "m1", At: at}}) {
		t.Fatalf("Tombstones() = %+v, %v", tombs, err)
	}
	if versions, err := store.Versions("m1"); err != nil || len(versions) != 1 || versions[0].Number != 1 {
		t.Fatalf("Versions of a trashed document = %+v, %v", versions, err)
	}
	if err := trash.Restore("m1"); err != nil {
		t.Fatalf("Restore() = %v", err)
	}
	if err := trash.Purge("m1"); !errs.HasCode(err, docstore.CodeDocumentNotTrashed) || !errors.Is(err, docstore.DocumentNotTrashed) {
		t.Fatalf("Purge of a restored document = %v, want DocumentNotTrashed", err)
	}
	if got, err := store.Lookup("email", "ada@example.com"); err != nil || got.ID != "m1" {
		t.Fatalf("Lookup after Restore = %+v, %v", got, err)
	}
	var _ docstore.TrashableContext = (*docstore.SQLStore[member])(nil)
}

// TestFacadePutDocument writes a document with its versions through public
// names only — VersionWriter, Version, VersionsInvalid and its code — and
// reads them back as given.
func TestFacadePutDocument(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	store, err := docstore.Open(
		docstore.Config[member]{Key: func(m member) string { return m.ID }, Versions: 2},
		docstore.Unique("email", func(m member) string { return m.Email }),
	)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	var writer docstore.VersionWriter = store
	given := []docstore.Version{
		{Number: 5, At: at, Meta: map[string]string{"by": "ada"}, JSON: json.RawMessage(`{"id":"m1","email":"ada@example.com"}`)},
		{Number: 1, JSON: json.RawMessage(`{"id":"m1"}`)},
	}
	if err := writer.PutDocument("m1", given, time.Time{}); err != nil {
		t.Fatalf("PutDocument() = %v", err)
	}
	versions, err := store.Versions("m1")
	if err != nil || len(versions) != 2 || versions[0].Number != 5 || !versions[0].At.Equal(at) ||
		versions[1].Number != 1 || !versions[1].At.IsZero() {
		t.Fatalf("Versions() = %+v, %v; want the versions given", versions, err)
	}
	if got, err := store.Lookup("email", "ada@example.com"); err != nil || got.ID != "m1" {
		t.Fatalf("Lookup() = %+v, %v", got, err)
	}
	given[1].Number = 5
	if err := writer.PutDocument("m2", given, time.Time{}); !errs.HasCode(err, docstore.CodeVersionsInvalid) ||
		!errors.Is(err, docstore.VersionsInvalid) {
		t.Fatalf("PutDocument with a number twice = %v, want VersionsInvalid", err)
	}
	var _ docstore.VersionWriterContext = (*docstore.SQLStore[member])(nil)
}

// TestFacadeVersionWeight weighs a store's versions through public names
// only — VersionWeigher, VersionWeight, Stats and the refusal of a store that
// keeps none.
func TestFacadeVersionWeight(t *testing.T) {
	t.Parallel()
	store, err := docstore.Open(docstore.Config[member]{Key: func(m member) string { return m.ID }, Versions: 2})
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	for _, email := range []string{"a@example.com", "b@example.com"} {
		if err := store.Put(member{ID: "m1", Email: email}); err != nil {
			t.Fatalf("Put() = %v", err)
		}
	}
	var weigher docstore.VersionWeigher = store
	former := len(`{"id":"m1","email":"a@example.com"}`)
	want := docstore.VersionWeight{Former: 1, Bytes: int64(former)}
	if got, err := weigher.VersionWeight(); err != nil || got != want || store.Stats().Versions != want {
		t.Fatalf("VersionWeight() = %+v, %v, Stats().Versions = %+v; want %+v", got, err, store.Stats().Versions, want)
	}
	plain, err := docstore.Open(docstore.Config[member]{Key: func(m member) string { return m.ID }})
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	if _, err := plain.VersionWeight(); !errs.HasCode(err, docstore.CodeVersionsNotKept) || !errors.Is(err, docstore.VersionsNotKept) {
		t.Fatalf("VersionWeight() without versions = %v, want VersionsNotKept", err)
	}
	var _ docstore.VersionWeigherContext = (*docstore.SQLStore[member])(nil)
}

func TestAStoreServesThroughItsPort(t *testing.T) {
	t.Parallel()
	store := open(t, vfs.NewMem())
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() = %v", err)
		}
	}()
	var docs docstore.Collection[member] = store
	var written []string
	defer docs.OnWrite(func(key string) { written = append(written, key) })()
	if err := docs.Insert(member{ID: "m1", Email: "ada@example.com"}); err != nil {
		t.Fatalf("Insert through the port = %v", err)
	}
	if got, err := docs.Lookup("email", "ada@example.com"); err != nil || got.ID != "m1" {
		t.Fatalf("Lookup through the port = (%+v, %v), want m1", got, err)
	}
	if _, err := docs.Get("absent"); !errs.HasCode(err, docstore.CodeDocumentNotFound) {
		t.Fatalf("Get of an absent key through the port = %v, want DocumentNotFound", err)
	}
	if !slices.Equal(written, []string{"m1"}) {
		t.Fatalf("OnWrite through the port was told %q, want [m1]", written)
	}
}
