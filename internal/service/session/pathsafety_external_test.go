//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

// Package session_test — the store's location is a file tree other accounts
// may write parts of, and the store refuses to be led anywhere through it: a
// link at a record's name, at the lock file's, or at a component of Dir planted
// where anybody could have planted it, and a parent swapped once the store is
// built.
//
// Every attack here was first run against the store as it shipped, on
// darwin/arm64, and every one of them worked: a Load served from a link's
// target, a lock file created through a dangling link, a whole store filed
// under a planted parent, and records following a swapped parent into the
// directory that replaced it. Each test's doc comment carries what it saw.
//
// Tagged like the store itself: the links and the modes these tests plant mean
// what they say only where the file store exists, and everywhere else its
// constructor refuses before a path is looked at.
package session_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	coresession "github.com/kitsunium/sdk/internal/core/session"
	svcsession "github.com/kitsunium/sdk/internal/service/session"
)

// pathConfig is the configuration every store in this file is built from.
func pathConfig(t *testing.T, dir string) svcsession.FileConfig {
	t.Helper()
	return svcsession.FileConfig{
		IdleTimeout: idleWindow, AbsoluteTimeout: absoluteCeiling,
		Clock: clock.NewManualClock(origin), Dir: dir, Key: testKey(t),
	}
}

// mkdirMode creates path and chmods it to mode, so neither a umask nor a
// default ACL leaves it as anything else.
func mkdirMode(t *testing.T, path string, mode fs.FileMode) string {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("Mkdir %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("Chmod %s: %v", path, err)
	}
	return path
}

// fieldValue returns one field of an SDK error, or "".
func fieldValue(err error, key string) string {
	for _, field := range errs.FieldsOf(err) {
		if field.Key() == key {
			return field.StringValue()
		}
	}
	return ""
}

// absent fails unless nothing exists at path — not even a dangling link.
func absent(t *testing.T, path, why string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s: %s exists (%v)", why, path, err)
	}
}

// TestALinkAtARecordNameIsNeverReadThrough pins the first exposure: the store
// read a record by following whatever stood at its name.
//
// Measured before the change, with a copy of the session's own earlier record
// kept outside the directory and a link to it planted at the record's name:
//
//	Load : err=<nil> where="outside"
//
// The session came back from the link's target — a record of the planter's
// choosing, here a rollback to an earlier state. Writes were never redirected
// (rename(2) replaces a link), only the read. A link inside the directory is
// the case os.Root would follow by itself, which is why both are here.
//
// Mutation: reading the record with os.Root.ReadFile instead of readEntry
// failed both rows — "inside" with `Load = <nil> (where="planted")`, and
// "outside" with a STORE_UNAVAILABLE where RecordCorrupt was wanted, os.Root
// refusing the escape on its own terms rather than this store's.
func TestALinkAtARecordNameIsNeverReadThrough(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// keep returns where the planted copy lives and how the link names it.
		keep func(t *testing.T, dir string) (path, linkTarget string)
	}{
		{"a link out of the directory", func(t *testing.T, _ string) (path, linkTarget string) {
			kept := filepath.Join(t.TempDir(), "kept.session")
			return kept, kept
		}},
		{"a link inside the directory", func(_ *testing.T, dir string) (path, linkTarget string) {
			//: relative, so os.Root would resolve it without leaving the root.
			return filepath.Join(dir, "kept"), "kept"
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fixture := newFileFixture(t, clock.NewManualClock(origin))
			fresh, err := fixture.store.New(ctx)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if saveErr := fixture.store.Save(ctx, fresh.Set("where", "planted")); saveErr != nil {
				t.Fatalf("Save: %v", saveErr)
			}
			record := filepath.Join(fixture.dir, fresh.ID().Digest()+".session")
			stale, readErr := os.ReadFile(record)
			if readErr != nil {
				t.Fatalf("ReadFile: %v", readErr)
			}
			kept, linkTarget := tc.keep(t, fixture.dir)
			if writeErr := os.WriteFile(kept, stale, 0o600); writeErr != nil {
				t.Fatalf("WriteFile: %v", writeErr)
			}
			//: the store's own record moves on; the copy is a record it once
			//: wrote, so it opens under this digest and this key.
			if saveErr := fixture.store.Save(ctx, fresh.Set("where", "stored")); saveErr != nil {
				t.Fatalf("Save: %v", saveErr)
			}
			if rmErr := os.Remove(record); rmErr != nil {
				t.Fatalf("Remove: %v", rmErr)
			}
			if linkErr := os.Symlink(linkTarget, record); linkErr != nil {
				t.Fatalf("Symlink: %v", linkErr)
			}

			loaded, loadErr := fixture.store.Load(ctx, fresh.ID())
			if !errs.HasCode(loadErr, svcsession.CodeRecordCorrupt) {
				where, _ := loaded.Get("where")
				t.Fatalf("Load through a planted link = %v (where=%q), want CodeRecordCorrupt", loadErr, where)
			}
			//: the target was not touched, and neither was the link: a refused
			//: read publishes nothing.
			if after, keptErr := os.ReadFile(kept); keptErr != nil || !bytes.Equal(after, stale) {
				t.Errorf("the link's target changed: %v", keptErr)
			}
			if info, lstatErr := os.Lstat(record); lstatErr != nil || info.Mode()&fs.ModeSymlink == 0 {
				t.Errorf("the planted link was replaced by a refused read: %v", lstatErr)
			}
			//: a sweep removes what it cannot read — the LINK, never its target.
			sweeper, ok := fixture.store.(coresession.Sweeper)
			if !ok {
				t.Fatal("the file store does not implement Sweeper")
			}
			if removed, sweepErr := sweeper.Sweep(ctx); sweepErr != nil || removed != 1 {
				t.Errorf("Sweep = (%d, %v), want the one planted link removed", removed, sweepErr)
			}
			absent(t, record, "after the sweep")
			if after, keptErr := os.ReadFile(kept); keptErr != nil || !bytes.Equal(after, stale) {
				t.Errorf("the sweep reached the link's target: %v", keptErr)
			}
		})
	}
}

// TestALinkAtTheLockFileIsRefused pins the second: the lock file's name is
// fixed, so a planter knows it before the store exists.
//
// Measured before the change, with a dangling link at .lock in an existing
// owner-only directory:
//
//	NewFileStore : err=<nil>, and the link's target now exists
//
// os.OpenFile(O_CREATE) created the target and the store-wide flock was taken
// on it — the lock the store serialises every read-modify-write under, on a
// file whoever planted the link chose.
//
// Mutation: opening the lock file with os.Root.OpenFile instead of openEntry
// built a store over the "inside" row's link — and created its target — and
// answered STORE_UNAVAILABLE rather than PATH_REDIRECTED for the two links
// that leave the directory.
func TestALinkAtTheLockFileIsRefused(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// plant returns what the link points at, as written, and the path that
		// must not come into existence (empty when the target already exists).
		plant func(t *testing.T, dir string) (linkTarget, mustNotExist string)
	}{
		{"a dangling link out of the directory", func(t *testing.T, _ string) (linkTarget, mustNotExist string) {
			target := filepath.Join(t.TempDir(), "planted")
			return target, target
		}},
		{"a link to an existing file out of the directory", func(t *testing.T, _ string) (linkTarget, mustNotExist string) {
			target := filepath.Join(t.TempDir(), "pidfile")
			if err := os.WriteFile(target, []byte("48213\n"), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			return target, ""
		}},
		{"a dangling link inside the directory", func(_ *testing.T, dir string) (linkTarget, mustNotExist string) {
			return "elsewhere.lock", filepath.Join(dir, "elsewhere.lock")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := mkdirMode(t, filepath.Join(t.TempDir(), "sessions"), 0o700)
			linkTarget, mustNotExist := tc.plant(t, dir)
			if err := os.Symlink(linkTarget, filepath.Join(dir, ".lock")); err != nil {
				t.Fatalf("Symlink: %v", err)
			}
			store, err := svcsession.NewFileStore(pathConfig(t, dir))
			if !errs.HasCode(err, svcsession.CodePathRedirected) {
				t.Fatalf("NewFileStore over a planted lock link = %v, want CodePathRedirected", err)
			}
			if store != nil {
				t.Error("a refused store came back with a store attached")
			}
			if kind := fieldValue(err, "kind"); kind != "symlink" {
				t.Errorf("kind = %q, want symlink", kind)
			}
			//: nothing was created through the link.
			if mustNotExist != "" {
				absent(t, mustNotExist, "the refused open created the link's target")
			}
			//: and an existing target is exactly as it was.
			if mustNotExist == "" {
				if content, readErr := os.ReadFile(linkTarget); readErr != nil || string(content) != "48213\n" {
					t.Errorf("the link's target changed: %q (%v)", content, readErr)
				}
			}
		})
	}
}

// TestALinkInDirsPathIsRefusedWhereAnybodyCouldHavePlantedIt pins the third,
// and the rule that decides it.
//
// Measured before the change, with Dir = …/pub/app/sessions, pub 1777 — what
// /tmp is — and app a link planted to …/elsewhere:
//
//	NewFileStore : err=<nil>
//	New          : err=<nil>
//	record landed under the planted target = true
//
// The owner-only check could not see it: it judged the 0700 directory the
// store had created inside the planter's tree. The rule is the lock domain's
// (ADR 0083): an indirection is refused when the directory HOLDING it is
// world-writable, sticky or not — planting creates an entry, which sticky does
// not govern — and accepted otherwise, because /tmp on macOS and /var/run on
// Linux are links the operating system planted. The accepting rows are half
// of the table on purpose: a guard that refused every link would pass the
// refusing rows and break every macOS deployment.
//
// Mutation: removing the checkChain call from openStoreDir failed every
// refusing row with `NewFileStore = <nil>, want CodePathRedirected`, and left
// the accepting rows green — as it should, since they do not depend on it.
func TestALinkInDirsPathIsRefusedWhereAnybodyCouldHavePlantedIt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		container fs.FileMode
		// atDir plants the link at Dir itself rather than at its parent.
		atDir   bool
		refused bool
	}{
		{"a parent planted in 1777, what /tmp is", 0o777 | os.ModeSticky, false, true},
		{"Dir itself planted in 1777", 0o777 | os.ModeSticky, true, true},
		{"a parent planted in 0777", 0o777, false, true},
		{"a parent in a group-writable directory", 0o770, false, false},
		{"a parent in a directory only its owner writes", 0o755, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			base := t.TempDir()
			pub := mkdirMode(t, filepath.Join(base, "pub"), tc.container)
			elsewhere := mkdirMode(t, filepath.Join(base, "elsewhere"), 0o700)
			//: where the records would land if the link were followed.
			landing := filepath.Join(elsewhere, "sessions")
			link, dir := filepath.Join(pub, "app"), filepath.Join(pub, "app", "sessions")
			target := elsewhere
			if tc.atDir {
				mkdirMode(t, landing, 0o700)
				link, dir, target = filepath.Join(pub, "sessions"), filepath.Join(pub, "sessions"), landing
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatalf("Symlink: %v", err)
			}
			store, err := svcsession.NewFileStore(pathConfig(t, dir))
			if !tc.refused {
				//: honoured: the link is the operator's, and the records land
				//: where it points.
				if err != nil {
					t.Fatalf("NewFileStore through a link only trusted accounts could plant = %v, want a store", err)
				}
				fresh, newErr := store.New(ctx)
				if newErr != nil {
					t.Fatalf("New: %v", newErr)
				}
				if _, statErr := os.Stat(filepath.Join(landing, fresh.ID().Digest()+".session")); statErr != nil {
					t.Errorf("the record did not land where the link points: %v", statErr)
				}
				return
			}
			if !errs.HasCode(err, svcsession.CodePathRedirected) {
				t.Fatalf("NewFileStore = %v, want CodePathRedirected", err)
			}
			if store != nil {
				t.Error("a refused store came back with a store attached")
			}
			//: the component is named, with where it leads exactly as planted.
			if got := filepath.Base(fieldValue(err, "path")); got != filepath.Base(link) {
				t.Errorf("path field names %q, want the planted component %q", got, filepath.Base(link))
			}
			if got := fieldValue(err, "target"); got != target {
				t.Errorf("target field = %q, want %q", got, target)
			}
			//: refused BEFORE anything was created inside the planter's tree.
			if !tc.atDir {
				absent(t, landing, "the refused store created its directory under the planted parent")
			}
		})
	}
}

// TestAParentSwappedAfterConstructionMovesNothing pins the half no audit at
// construction can reach.
//
// Measured before the change, with Dir = …/pub/sessions and pub 0777 — no
// link anywhere, so nothing to refuse — the store built, and its directory
// then renamed away and replaced by another:
//
//	New : err=<nil>
//	record in the swapped-in directory = true, in the checked one = false
//
// Every operation re-resolved the path, so after the swap the store wrote
// into a directory nobody had checked. It now holds the directory it checked
// as an os.Root and resolves every name against that handle.
//
// Mutation: publishing through os.CreateTemp and os.Rename on the configured
// path, as before, failed with `the record was written into the swapped-in
// directory`.
func TestAParentSwappedAfterConstructionMovesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := t.TempDir()
	pub := mkdirMode(t, filepath.Join(base, "pub"), 0o777)
	dir := filepath.Join(pub, "sessions")
	store, err := svcsession.NewFileStore(pathConfig(t, dir))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	before, err := store.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	//: anyone can rename an entry of a world-writable, non-sticky directory.
	held := filepath.Join(pub, "held")
	if renameErr := os.Rename(dir, held); renameErr != nil {
		t.Fatalf("Rename: %v", renameErr)
	}
	planted := mkdirMode(t, dir, 0o700)

	after, err := store.New(ctx)
	if err != nil {
		t.Fatalf("New after the swap: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(held, after.ID().Digest()+".session")); statErr != nil {
		t.Errorf("the record is not in the directory the store checked: %v", statErr)
	}
	if entries, readErr := os.ReadDir(planted); readErr != nil || len(entries) != 0 {
		t.Errorf("the record was written into the swapped-in directory: %d entries (%v)", len(entries), readErr)
	}
	//: reads, removals and the listing a sweep takes follow the handle too.
	if _, loadErr := store.Load(ctx, before.ID()); loadErr != nil {
		t.Errorf("Load of a record written before the swap = %v", loadErr)
	}
	if destroyErr := store.Destroy(ctx, before.ID()); destroyErr != nil {
		t.Fatalf("Destroy: %v", destroyErr)
	}
	absent(t, filepath.Join(held, before.ID().Digest()+".session"), "Destroy left the record behind")
	sweeper, ok := store.(coresession.Sweeper)
	if !ok {
		t.Fatal("the file store does not implement Sweeper")
	}
	if _, sweepErr := sweeper.Sweep(ctx); sweepErr != nil {
		t.Errorf("Sweep after the swap: %v", sweepErr)
	}
	if _, loadErr := store.Load(ctx, after.ID()); loadErr != nil {
		t.Errorf("Load after the sweep = %v, want the live record", loadErr)
	}
}

// TestAnEntryThatIsNotAFileIsNeverOpened pins what the look before every open
// buys besides refusing links: a directory or a FIFO standing at a name the
// store opens is reported, never opened.
//
// A FIFO is the case that matters. Opening one for reading waits for a writer,
// and the store opens records while it holds the store-wide lock — so before
// the look, one FIFO at one record's name would have parked a Load inside the
// lock and every other operation, in every process, behind it. If that
// regresses, this test does not fail: it hangs until the test binary's own
// timeout, which is the symptom it guards against.
func TestAnEntryThatIsNotAFileIsNeverOpened(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		plant func(path string) error
	}{
		{"a FIFO", func(path string) error { return syscall.Mkfifo(path, 0o600) }},
		{"a directory", func(path string) error { return os.Mkdir(path, 0o700) }},
	}
	for _, tc := range tests {
		t.Run("at a record's name: "+tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fixture := newFileFixture(t, clock.NewManualClock(origin))
			fresh, err := fixture.store.New(ctx)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			record := filepath.Join(fixture.dir, fresh.ID().Digest()+".session")
			if rmErr := os.Remove(record); rmErr != nil {
				t.Fatalf("Remove: %v", rmErr)
			}
			if plantErr := tc.plant(record); plantErr != nil {
				t.Fatalf("plant: %v", plantErr)
			}
			_, loadErr := fixture.store.Load(ctx, fresh.ID())
			if !errs.HasCode(loadErr, coresession.CodeStoreUnavailable) || fieldValue(loadErr, "kind") != "not-regular" {
				t.Errorf("Load = %v (kind %q), want CodeStoreUnavailable naming a non-regular entry",
					loadErr, fieldValue(loadErr, "kind"))
			}
		})
		t.Run("at the lock file's name: "+tc.name, func(t *testing.T) {
			t.Parallel()
			dir := mkdirMode(t, filepath.Join(t.TempDir(), "sessions"), 0o700)
			if plantErr := tc.plant(filepath.Join(dir, ".lock")); plantErr != nil {
				t.Fatalf("plant: %v", plantErr)
			}
			_, err := svcsession.NewFileStore(pathConfig(t, dir))
			if !errs.HasCode(err, coresession.CodeStoreUnavailable) || fieldValue(err, "kind") != "not-regular" {
				t.Errorf("NewFileStore = %v (kind %q), want CodeStoreUnavailable naming a non-regular entry",
					err, fieldValue(err, "kind"))
			}
		})
	}
}
