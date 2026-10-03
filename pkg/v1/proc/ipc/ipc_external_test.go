//go:build unix

package ipc_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/proc/ipc"
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

// stubListener and stubDialer are doubles written with the facade's names
// alone: a port a consumer is told to hold must be one it can implement
// without importing anything internal (ADR 0090).
type stubListener struct{}

// Accept has nothing to give: the double is closed from the start.
func (stubListener) Accept() (*ipc.Conn, error) { return nil, net.ErrClosed }

// Addr is no address.
func (stubListener) Addr() net.Addr { return nil }

// Close closes nothing.
func (stubListener) Close() error { return nil }

// Path names no file.
func (stubListener) Path() string { return "" }

// Refused counts nothing.
func (stubListener) Refused() int64 { return 0 }

// stubDialer reaches nobody.
type stubDialer struct{}

// Dial refuses, as an engine with no listener does.
func (stubDialer) Dial(context.Context) (*ipc.Conn, error) { return nil, net.ErrClosed }

// The doubles satisfy the ports.
var (
	_ ipc.Listener = stubListener{}
	_ ipc.Dialer   = stubDialer{}
)

// TestTheFacadeDialsThroughAPort reaches the engine behind the Dialer port,
// and refuses a configuration before it touches anything.
//
// Goroutine lifecycle: one goroutine accepts one connection and closes it,
// reporting on a buffered channel the test waits on.
func TestTheFacadeDialsThroughAPort(t *testing.T) {
	if _, err := ipc.NewDialer(ipc.Config{Path: "relative"}); !errs.HasCode(err, ipc.CodeMisconfigured) {
		t.Errorf("NewDialer with a relative path: %v", err)
	}
	dir, err := os.MkdirTemp("/tmp", "ipcf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})
	cfg := ipc.Config{Path: filepath.Join(dir, "run", "d.sock")}
	ln, err := ipc.Listen(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := ln.Close(); err != nil {
			t.Logf("close: %v", err)
		}
	}()
	accepted := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			err = c.Close()
		}
		accepted <- err
	}()
	d, err := ipc.NewDialer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c, err := d.Dial(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-accepted; err != nil {
		t.Errorf("the accepting end: %v", err)
	}
}
