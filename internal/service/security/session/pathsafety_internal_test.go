//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

// Package session — the branches of the path checks that only a race reaches:
// a directory swapped between its check and its open, and a name swapped
// between the look and the open. A test cannot win those races on demand, so
// the helpers that decide them are driven directly, with the swap already
// made.
package session

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// heldRoot opens dir as a root and closes it when the test ends.
func heldRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot %s: %v", dir, err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("closing the root: %v", closeErr)
		}
	})
	return root
}

// modeDir creates path with exactly mode.
func modeDir(t *testing.T, path string, mode fs.FileMode) string {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	return path
}

// TestTheHeldDirectoryMustBeTheOneDirNames pins assertHeldDir: the mode is
// judged on the directory the store HOLDS, and the configured path must still
// lead to it. Between the checks by path and os.OpenRoot, a rename of any
// component could otherwise hand the store a directory nobody checked.
func TestTheHeldDirectoryMustBeTheOneDirNames(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	checked := modeDir(t, filepath.Join(base, "checked"), dirMode)
	other := modeDir(t, filepath.Join(base, "other"), dirMode)
	wide := modeDir(t, filepath.Join(base, "wide"), 0o755)
	tests := []struct {
		name string
		held string
		dir  string
		want errs.Code
	}{
		{"the directory dir names, owner-only", checked, checked, 0},
		{"dir now names another directory", checked, other, CodePathRedirected},
		{"dir now names nothing", checked, filepath.Join(base, "gone"), CodePathRedirected},
		{"the held directory is readable by others", wide, wide, CodeDirectoryUnsafe},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := assertHeldDir(heldRoot(t, tc.held), tc.dir)
			if tc.want == 0 {
				if err != nil {
					t.Fatalf("assertHeldDir = %v, want nil", err)
				}
				return
			}
			if !errs.HasCode(err, tc.want) {
				t.Fatalf("assertHeldDir = %v, want code %v", err, tc.want)
			}
		})
	}
}

// TestAHandleIsProvenAgainstItsName pins sameEntry, the half of openEntry that
// catches a name swapped between the look and the open: nothing is read from,
// or locked on, a handle that is not the regular file the name leads to.
func TestAHandleIsProvenAgainstItsName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	root := heldRoot(t, dir)
	for _, name := range []string{"named", "other"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	if err := os.Symlink("named", filepath.Join(dir, "link")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	named, err := root.Lstat("named")
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	tests := []struct {
		name    string
		entry   string
		handle  string
		before  fs.FileInfo
		present bool
		want    string
	}{
		{"the handle is the file the name led to", "named", "named", named, true, ""},
		{"the name was swapped for another file", "named", "other", named, true, kindReplaced},
		{"created, and the name is a link now", "link", "named", nil, false, kindSymlink},
		{"created, and the name is gone again", "missing", "named", nil, false, kindReplaced},
		{"created, and it is the file the name leads to", "other", "other", nil, false, ""},
		{"the open landed on a directory", "subdir", "subdir", nil, false, kindNotRegular},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			handle, openErr := root.Open(tc.handle)
			if openErr != nil {
				t.Fatalf("Open: %v", openErr)
			}
			defer func() {
				if closeErr := handle.Close(); closeErr != nil {
					t.Errorf("Close: %v", closeErr)
				}
			}()
			found, proveErr := sameEntry(root, tc.entry, handle, tc.before, tc.present)
			if proveErr != nil {
				t.Fatalf("sameEntry: %v", proveErr)
			}
			if found != tc.want {
				t.Errorf("sameEntry = %q, want %q", found, tc.want)
			}
		})
	}
}

// TestAFailedOpenIsReadAsALinkOnlyWhenItIsOne pins isLinkNow, the diagnosis
// openEntry runs after an open fails: os.Root refuses a link that leaves the
// directory at the open rather than following it, and that refusal is a
// verdict on the name, not a fault of the medium.
func TestAFailedOpenIsReadAsALinkOnlyWhenItIsOne(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	root := heldRoot(t, dir)
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(dir, "escaping")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plain"), nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	//: the escaping link really is refused at the open, which is the path
	//: isLinkNow exists to classify.
	if _, openErr := root.OpenFile("escaping", os.O_CREATE|os.O_RDWR, fileMode); openErr == nil {
		t.Fatal("os.Root opened a link that leaves the directory; isLinkNow is unreachable as described")
	}
	for name, want := range map[string]bool{"escaping": true, "plain": false, "missing": false} {
		if got := isLinkNow(root, name); got != want {
			t.Errorf("isLinkNow(%q) = %v, want %v", name, got, want)
		}
	}
}
