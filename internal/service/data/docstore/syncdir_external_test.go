// Package docstore_test — the directories Open creates are durable: their
// parents are flushed through vfs.DirSyncer (#1060, ADR 0110's deferral).
package docstore_test

import (
	"slices"
	"sync"
	"testing"

	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
	"github.com/kitsunium/sdk/internal/service/data/docstore"
	svcvfs "github.com/kitsunium/sdk/internal/service/data/vfs"
)

// syncRecordingFS is a memory filesystem that records every directory SyncDir
// is asked to flush, in order.
type syncRecordingFS struct {
	corevfs.FullFS
	mu     sync.Mutex
	synced []string
}

// SyncDir records name, then answers what the memory filesystem answers.
func (r *syncRecordingFS) SyncDir(name string) error {
	r.mu.Lock()
	r.synced = append(r.synced, name)
	r.mu.Unlock()
	syncer, ok := r.FullFS.(corevfs.DirSyncer)
	if !ok {
		return nil
	}
	return syncer.SyncDir(name)
}

// flushed returns the directories flushed so far, and forgets them.
func (r *syncRecordingFS) flushed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.synced
	r.synced = nil
	return out
}

// TestOpenFlushesTheParentOfEveryDirectoryItCreates pins #1060's docstore
// half: a store opened at a/b/accounts.json creates a, a/b and the overlay
// a/b/accounts.json.d, and flushes the parent of each — a/b, a and "." — once,
// deepest first; a store reopened over directories that exist flushes none.
func TestOpenFlushesTheParentOfEveryDirectoryItCreates(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, path string
		want       []string
	}{
		{"nested", "a/b/accounts.json", []string{"a/b", "a", "."}},
		{"top level", "accounts.json", []string{"."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fsys := &syncRecordingFS{FullFS: svcvfs.NewMem()}
			cfg := accountConfig(fsys)
			cfg.Path = tc.path
			store, err := docstore.Open(cfg, accountIndexes()...)
			if err != nil {
				t.Fatalf("Open = %v", err)
			}
			if got := fsys.flushed(); !slices.Equal(got, tc.want) {
				t.Fatalf("SyncDir calls on a first Open = %v, want %v", got, tc.want)
			}
			must(t, store.Close())
			fsys.flushed()
			again, reopenErr := docstore.Open(cfg, accountIndexes()...)
			if reopenErr != nil {
				t.Fatalf("Open (again) = %v", reopenErr)
			}
			defer func() { must(t, again.Close()) }()
			if got := fsys.flushed(); len(got) != 0 {
				t.Fatalf("SyncDir calls on a reopen = %v, want none: nothing was created", got)
			}
		})
	}
}

// TestOpenOverAFilesystemWithoutDirSyncerStillOpens pins the type assertion:
// a third-party filesystem with no SyncDir opens as it did before.
func TestOpenOverAFilesystemWithoutDirSyncerStillOpens(t *testing.T) {
	t.Parallel()
	type plainFS struct{ corevfs.FullFS }
	store, err := docstore.Open(accountConfig(plainFS{svcvfs.NewMem()}), accountIndexes()...)
	if err != nil {
		t.Fatalf("Open over a filesystem without DirSyncer = %v, want a store", err)
	}
	must(t, store.Close())
}
