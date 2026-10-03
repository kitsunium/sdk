//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package secret

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcvfs "github.com/kitsunium/sdk/internal/service/vfs"
)

// Test_checkHeldRoot pins the re-check NewFile makes after opening its root:
// the mode is judged on the HELD directory, and the path must still name it.
// Both failures are what a swap of the path between the check and the open
// looks like from inside the store.
func Test_checkHeldRoot(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		swap  func(t *testing.T, dir string)
		valid bool
	}
	tests := []tc{
		{"the directory checked is the one held", func(*testing.T, string) {}, true},
		{"the held directory was opened to other accounts", func(t *testing.T, dir string) {
			if err := os.Chmod(dir, 0o750); err != nil {
				t.Fatalf("chmod: %v", err)
			}
		}, false},
		{"the path was swapped for another directory", func(t *testing.T, dir string) {
			if err := os.Rename(dir, dir+"-moved"); err != nil {
				t.Fatalf("rename: %v", err)
			}
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
		}, false},
		{"the path no longer names anything", func(t *testing.T, dir string) {
			if err := os.Rename(dir, dir+"-gone"); err != nil {
				t.Fatalf("rename: %v", err)
			}
		}, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dir := filepath.Join(t.TempDir(), "secrets")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		root, err := svcvfs.NewOS(dir)
		if err != nil {
			t.Fatalf("NewOS: %v", err)
		}
		t.Cleanup(func() {
			if closeErr := rootCloser(root).Close(); closeErr != nil {
				t.Errorf("close: %v", closeErr)
			}
		})
		c.swap(t, dir)
		heldErr := checkHeldRoot(root, dir)
		if c.valid && heldErr != nil {
			t.Fatalf("%s: checkHeldRoot = %v, want nil", c.name, heldErr)
		}
		if !c.valid && !errs.HasCode(heldErr, CodeInvalidConfig) {
			t.Fatalf("%s: checkHeldRoot = %v, want INVALID_CONFIG", c.name, heldErr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
