// Package docstore_test — the documents, stores and filesystems the suite
// stands up.
package docstore_test

import (
	"io/fs"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	corevfs "github.com/kitsunium/sdk/internal/core/vfs"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/docstore"
	svcvfs "github.com/kitsunium/sdk/internal/service/vfs"
)

// account is the document most cases store: a unique e-mail, the teams it
// belongs to, and a name.
type account struct {
	ID    string   `json:"id"`
	Email string   `json:"email,omitempty"`
	Name  string   `json:"name,omitempty"`
	Teams []string `json:"teams,omitempty"`
}

// accountKey is the accounts' key function.
func accountKey(a account) string { return a.ID }

// accountIndexes are the accounts store's indexes: a unique "email" and a
// multi-valued "team".
func accountIndexes() []docstore.IndexSpec[account] {
	return []docstore.IndexSpec[account]{
		docstore.Unique("email", func(a account) string { return a.Email }),
		docstore.Index("team", func(a account) []string { return a.Teams }),
	}
}

// openWith opens an accounts store from cfg, with the accounts indexes.
func openWith(cfg docstore.Config[account]) (*docstore.Store[account], error) {
	return docstore.Open(cfg, accountIndexes()...)
}

// accountConfig is the configuration of an accounts store over fsys — in
// memory when fsys is nil.
func accountConfig(fsys corevfs.FullFS) docstore.Config[account] {
	cfg := docstore.Config[account]{Key: accountKey}
	//: a persistent store lives at a fixed path, the way a framework lays out
	//: one store per service.
	if fsys != nil {
		cfg.FS, cfg.Path = fsys, "members/accounts.json"
	}
	return cfg
}

// openAccounts opens an accounts store over fsys and closes it when the test
// ends, if the test did not.
func openAccounts(t *testing.T, fsys corevfs.FullFS) *docstore.Store[account] {
	t.Helper()
	store, err := openWith(accountConfig(fsys))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	t.Cleanup(func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Errorf("Close() = %v", closeErr)
		}
	})
	return store
}

// must fails the test on an error.
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// requireCode fails the test unless err carries code.
func requireCode(t *testing.T, err error, code errs.Code, what string) {
	t.Helper()
	if !errs.HasCode(err, code) {
		t.Fatalf("%s = %v, want %s", what, err, code)
	}
}

// ids returns the accounts' keys, in order.
func ids(accounts []account) []string {
	out := make([]string, len(accounts))
	for i, a := range accounts {
		out[i] = a.ID
	}
	return out
}

// readFile reads a file of fsys, failing the test when it cannot.
func readFile(t *testing.T, fsys fs.FS, name string) string {
	t.Helper()
	raw, err := fs.ReadFile(fsys, name)
	if err != nil {
		t.Fatalf("ReadFile(%s) = %v", name, err)
	}
	return string(raw)
}

// overlayEntries lists the overlay entry files of the accounts store.
func overlayEntries(t *testing.T, fsys fs.FS) []string {
	t.Helper()
	entries, err := fs.ReadDir(fsys, "members/accounts.json.d")
	if err != nil {
		t.Fatalf("ReadDir(overlay) = %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names
}

// diskFS returns a vfs.NewOS filesystem over a fresh directory, and the
// directory. Windows refuses it by design (ADR 0056): the refusal is
// asserted, then the case is skipped, because it needs a filesystem that
// exists.
func diskFS(t *testing.T) (corevfs.FullFS, string) {
	t.Helper()
	root := t.TempDir()
	fsys, err := svcvfs.NewOS(root)
	//: the platform's refusal, asserted before anything is skipped.
	if runtime.GOOS == "windows" {
		if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) || fsys != nil {
			t.Fatalf("NewOS on windows = (%v, %v), want (nil, UNSUPPORTED_PLATFORM)", fsys, err)
		}
		t.Skip("vfs.NewOS refuses windows by design (ADR 0018, ADR 0056); that refusal is asserted above, and this case needs a disk filesystem")
	}
	if err != nil {
		t.Fatalf("NewOS() = %v", err)
	}
	return fsys, root
}

// faultyFS wraps a filesystem and fails WriteAtomic on demand: refused
// outright (nothing published), or published and then reported as a
// directory flush that failed — for every name, or for one.
type faultyFS struct {
	corevfs.FullFS
	mu sync.Mutex
	// only limits the faults to one name; empty means every name.
	only string
	// refuse fails WriteAtomic before anything is written.
	refuse bool
	// unconfirmed publishes, then reports DirectorySyncFailed.
	unconfirmed bool
}

// WriteAtomic publishes through the wrapped filesystem unless told not to.
func (f *faultyFS) WriteAtomic(name string, data []byte, perm fs.FileMode) error {
	f.mu.Lock()
	refuse, unconfirmed := f.refuse, f.unconfirmed
	if f.only != "" && f.only != name {
		refuse, unconfirmed = false, false
	}
	f.mu.Unlock()
	if refuse {
		return errs.Wrap(corevfs.PublishFailed, errs.WrapParams{}, errs.String("path", name))
	}
	if err := f.FullFS.WriteAtomic(name, data, perm); err != nil {
		return err
	}
	if unconfirmed {
		return errs.Wrap(svcvfs.DirectorySyncFailed, errs.WrapParams{}, errs.String("path", name))
	}
	return nil
}

// set changes what the next publications of only — every name when empty —
// do.
func (f *faultyFS) set(only string, refuse, unconfirmed bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.only, f.refuse, f.unconfirmed = only, refuse, unconfirmed
}

// gatedFS wraps a filesystem and holds every WriteAtomic until released,
// announcing each one it holds.
type gatedFS struct {
	corevfs.FullFS
	// entered receives once per publication that is being held.
	entered chan string
	// release lets the held publications through when closed.
	release chan struct{}
}

// WriteAtomic announces the publication, waits for the gate, then publishes.
func (g *gatedFS) WriteAtomic(name string, data []byte, perm fs.FileMode) error {
	g.entered <- name
	<-g.release
	return g.FullFS.WriteAtomic(name, data, perm)
}

// quotes reports whether text quotes any of the given values.
func quotes(text string, values ...string) bool {
	for _, value := range values {
		if value != "" && strings.Contains(text, value) {
			return true
		}
	}
	return false
}

// memFS returns a fresh in-memory filesystem: every OS, no cleanup.
func memFS() corevfs.FullFS {
	return svcvfs.NewMem()
}
