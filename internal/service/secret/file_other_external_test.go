//go:build !(linux || darwin || freebsd || openbsd || netbsd || dragonfly)

package secret_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsecret "github.com/kitsunium/sdk/internal/service/secret"
)

// TestFileStoreRefusesOffItsPlatforms pins the ADR 0018 refusal: the typed
// UnsupportedPlatform, decided before the directory is created — a store that
// cannot keep its promises must not leave a directory it could never use.
func TestFileStoreRefusesOffItsPlatforms(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "secrets")
	_, err := svcsecret.NewFile(svcsecret.FileConfig{Dir: dir})
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		t.Fatalf("NewFile = %v, want UnsupportedPlatform", err)
	}
	if _, statErr := os.Stat(dir); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("the refused store created its directory (stat: %v)", statErr)
	}
}

// TestKeyFileRefusesOffItsPlatforms pins that KeyFile refuses where the file
// store does, and creates nothing there.
func TestKeyFileRefusesOffItsPlatforms(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "keys", "store.key")
	_, err := svcsecret.KeyFile(path)
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		t.Fatalf("KeyFile = %v, want UnsupportedPlatform", err)
	}
	if _, statErr := os.Stat(filepath.Dir(path)); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("the refused KeyFile created its directory (stat: %v)", statErr)
	}
}
