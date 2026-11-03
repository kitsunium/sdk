// Package docstore_test — a document's versions in the memory and file
// stores: the helpers the versions' tests share, and what is the file
// engine's own. The contract every engine keeps — numbered, stamped, pruned
// in the write that stores the document, kept from pruning by a hold,
// rewritten for an erasure — is conformance_external_test.go's.
package docstore_test

import (
	"encoding/json"
	"testing"
	"time"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
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
	return openIndexed(t, cfg, accountIndexes())
}

// openIndexed opens cfg with indexes and closes it when the test ends.
func openIndexed(t *testing.T, cfg docstore.Config[account], indexes []coredocstore.IndexSpec[account]) *docstore.Store[account] {
	t.Helper()
	store, err := docstore.Open(cfg, indexes...)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	t.Cleanup(func() {
		if closeErr := store.Close(); closeErr != nil && !errs.HasCode(closeErr, coredocstore.CodeStoreClosed) {
			t.Errorf("Close() = %v", closeErr)
		}
	})
	return store
}

// versionsOf reads key's versions, failing the test on an error.
func versionsOf(t *testing.T, store *docstore.Store[account], key string) []coredocstore.VersionValue {
	t.Helper()
	all, err := store.Versions(key)
	if err != nil {
		t.Fatalf("Versions(%s) = %v", key, err)
	}
	return all
}

// numbers returns the versions' numbers, in order.
func numbers(all []coredocstore.VersionValue) []uint64 {
	out := make([]uint64, len(all))
	for i, v := range all {
		out[i] = v.Number
	}
	return out
}

// namesIn decodes each version and returns the names they hold, in order.
func namesIn(t *testing.T, all []coredocstore.VersionValue) []string {
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

// TestAStoreWithoutVersionsWritesItsEntryAsBefore pins what is the file
// engine's own in a store that keeps no versions — the conformance row
// AStoreWithoutVersions holds the refusals on every engine: it writes exactly
// the entry it wrote before versions existed, and no versions file.
func TestAStoreWithoutVersionsWritesItsEntryAsBefore(t *testing.T) {
	t.Parallel()
	fsys := memFS()
	cfg := accountConfig(fsys)
	cfg.FoldAt = -1
	store := openVersioned(t, cfg)
	must(t, store.PutStamped(account{ID: "acc_1", Name: "Ada"}, coredocstore.StampValue{Meta: map[string]string{"by": "ada"}}))
	entry := readFile(t, fsys, overlayPath+"/"+entryNameOf(t, "acc_1"))
	if entry != `{"doc":{"id":"acc_1","name":"Ada"},"key":"acc_1"}` {
		t.Fatalf("a store without versions wrote the entry %s", entry)
	}
	must(t, store.Close())
	if _, statErr := fsys.Open(versionsPath); statErr == nil {
		t.Fatalf("a store without versions wrote %s", versionsPath)
	}
}
