//go:build unix

package ipc_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/ipc"
)

// TestTheFacadeListensAndDials reaches the engine, and its codes match
// through pkg/v1/errs.
//
// Goroutine lifecycle: one goroutine accepts one connection and closes it; it
// ends when that happens or when the deferred Close ends Accept.
func TestTheFacadeListensAndDials(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ipcf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})
	cfg := ipc.Config{Path: filepath.Join(dir, "run", "f.sock")}
	ln, err := ipc.Listen(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := ln.Close(); err != nil {
			t.Logf("close: %v", err)
		}
	}()
	go func() {
		if c, err := ln.Accept(); err == nil {
			if err := c.Close(); err != nil {
				t.Logf("close: %v", err)
			}
		}
	}()
	c, err := ipc.Dial(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ipc.Listen(ipc.Config{Path: "relative"}); !errs.HasCode(err, ipc.CodeMisconfigured) {
		t.Errorf("a relative path: %v", err)
	}
}

// TestTheFacadeRefusesAPathAnybodyCouldSteer plants a link above the socket's
// directory in a world-writable sticky directory — what /tmp is — and expects
// the refusal, matched through pkg/v1/errs, before anything is created where
// the link leads.
func TestTheFacadeRefusesAPathAnybodyCouldSteer(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ipcf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})
	pub := filepath.Join(dir, "pub")
	target := filepath.Join(dir, "elsewhere")
	for _, d := range []string{pub, target} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(target, filepath.Join(pub, "app")); err != nil {
		t.Skipf("cannot create a symbolic link here: %v", err)
	}
	if err := os.Chmod(pub, 0o777|os.ModeSticky); err != nil {
		t.Skipf("cannot set the mode this test needs: %v", err)
	}
	if _, err := ipc.Listen(ipc.Config{Path: filepath.Join(pub, "app", "run", "f.sock")}); !errs.HasCode(err, ipc.CodePathUnsafe) {
		t.Errorf("Listen through a planted link: %v, want PATH_UNSAFE", err)
	}
	if _, err := os.Lstat(filepath.Join(target, "run")); !os.IsNotExist(err) {
		t.Errorf("the refused Listen created its directory where the link leads: %v", err)
	}
}
