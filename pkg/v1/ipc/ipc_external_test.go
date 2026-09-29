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
