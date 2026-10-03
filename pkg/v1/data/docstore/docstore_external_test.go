package docstore_test

import (
	"encoding/json"
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
