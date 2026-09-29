//go:build unix

package ipc_test

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/ipc"
)

// socketDir is a short private directory: macOS's sun_path is short and
// t.TempDir() under /var/folders already spends most of it (ADR 0094).
func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ipc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})
	return filepath.Join(dir, "run")
}

// closeOrLog closes c at the end of a test, logging a failure.
func closeOrLog(t *testing.T, c interface{ Close() error }) {
	t.Helper()
	if err := c.Close(); err != nil {
		t.Logf("close: %v", err)
	}
}

// TestARoundTripKnowsItsPeer echoes one line through a listener and a dialer.
//
// Goroutine lifecycle: one goroutine accepts one connection, echoes one line
// and reports the peer on a buffered channel; it ends when that connection
// closes or Accept fails, and the test waits on the channel before returning.
func TestARoundTripKnowsItsPeer(t *testing.T) {
	cfg := ipc.Config{Path: filepath.Join(socketDir(t), "d.sock")}
	ln, err := ipc.NewListener(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeOrLog(t, ln)
	info, err := os.Stat(filepath.Dir(cfg.Path))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("the directory: %v %v, want 0700", info, err)
	}
	if s, err := os.Stat(cfg.Path); err != nil || s.Mode().Perm() != 0o600 {
		t.Fatalf("the socket: %v %v, want 0600", s, err)
	}
	done := make(chan ipc.PeerValue, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			t.Error(err)
			done <- ipc.PeerValue{}
			return
		}
		defer closeOrLog(t, c)
		line, err := bufio.NewReader(c).ReadString('\n')
		if err != nil {
			t.Error(err)
		}
		if _, err := c.Write([]byte(strings.ToUpper(line))); err != nil {
			t.Error(err)
		}
		done <- c.Peer
	}()
	c, err := ipc.Dial(t.Context(), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeOrLog(t, c)
	if _, err := c.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	if got, err := bufio.NewReader(c).ReadString('\n'); err != nil || got != "PING\n" {
		t.Errorf("the echo: %q %v", got, err)
	}
	peer := <-done
	if runtime.GOOS == "linux" {
		if !peer.Verified || peer.UID != os.Geteuid() || peer.PID != os.Getpid() || !c.Peer.Verified {
			t.Errorf("the kernel's word on the peers: listener saw %+v, dialer saw %+v", peer, c.Peer)
		}
	} else if peer.Verified {
		t.Errorf("a peer claimed verified where the kernel says nothing: %+v", peer)
	}
}

// TestALiveSocketIsNeverTakenOver refuses a second listener on a live socket.
//
// Goroutine lifecycle: one goroutine accepts and closes connections until the
// listener closes, which the deferred Close does before the test returns.
func TestALiveSocketIsNeverTakenOver(t *testing.T) {
	cfg := ipc.Config{Path: filepath.Join(socketDir(t), "d.sock")}
	ln, err := ipc.NewListener(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeOrLog(t, ln)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			closeOrLog(t, c)
		}
	}()
	if _, err := ipc.NewListener(&cfg); !errs.HasCode(err, ipc.CodeInUse) {
		t.Fatalf("a second listener on a live socket: %v, want IN_USE", err)
	}
	if _, err := os.Stat(cfg.Path); err != nil {
		t.Fatalf("the refused listener removed the live socket: %v", err)
	}
}

func TestADeadSocketIsReplaced(t *testing.T) {
	cfg := ipc.Config{Path: filepath.Join(socketDir(t), "d.sock")}
	if err := os.MkdirAll(filepath.Dir(cfg.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	// A socket nobody listens on any more: what a SIGKILLed daemon leaves.
	old, err := net.ListenUnix("unix", &net.UnixAddr{Name: cfg.Path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	old.SetUnlinkOnClose(false)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	ln, err := ipc.NewListener(&cfg)
	if err != nil {
		t.Fatalf("a leftover socket was not replaced: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg.Path); !os.IsNotExist(err) {
		t.Errorf("Close left the socket file: %v", err)
	}
}

func TestSomethingElseAtThePathIsNeverRemoved(t *testing.T) {
	cfg := ipc.Config{Path: filepath.Join(socketDir(t), "d.sock")}
	if err := os.MkdirAll(filepath.Dir(cfg.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.Path, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ipc.NewListener(&cfg); !errs.HasCode(err, ipc.CodeDirectoryUnsafe) {
		t.Fatalf("a regular file at the socket path: %v, want DIRECTORY_UNSAFE", err)
	}
	if b, err := os.ReadFile(cfg.Path); err != nil || string(b) != "keep me" {
		t.Errorf("the file was touched: %q %v", b, err)
	}
}

func TestAnUnsafeDirectoryIsRefusedOnBothSides(t *testing.T) {
	dir := socketDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	cfg := ipc.Config{Path: filepath.Join(dir, "d.sock")}
	if _, err := ipc.NewListener(&cfg); !errs.HasCode(err, ipc.CodeDirectoryUnsafe) {
		t.Errorf("Listen in a world-writable directory: %v", err)
	}
	if _, err := ipc.Dial(t.Context(), &cfg); !errs.HasCode(err, ipc.CodeDirectoryUnsafe) {
		t.Errorf("Dial into a world-writable directory: %v", err)
	}
	link := dir + "-link"
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(link); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ipc.NewListener(&ipc.Config{Path: filepath.Join(link, "d.sock")}); !errs.HasCode(err, ipc.CodeDirectoryUnsafe) {
		t.Errorf("Listen through a link: %v", err)
	}
}

func TestAConfigurationIsCheckedBeforeAnythingIsTouched(t *testing.T) {
	for _, cfg := range []ipc.Config{
		{},
		{Path: "relative.sock"},
		{Path: "/" + strings.Repeat("x", 200)},
		{Path: "/tmp/x.sock", AllowUIDs: []int{-1}},
	} {
		if _, err := ipc.NewListener(&cfg); !errs.HasCode(err, ipc.CodeMisconfigured) {
			t.Errorf("NewListener(%+v) = %v, want MISCONFIGURED", cfg, err)
		}
	}
}

func TestADialToNothingSaysSo(t *testing.T) {
	cfg := ipc.Config{Path: filepath.Join(socketDir(t), "none.sock")}
	if err := os.MkdirAll(filepath.Dir(cfg.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ipc.Dial(t.Context(), &cfg); !errs.HasCode(err, ipc.CodeDialFailed) {
		t.Errorf("Dial to no listener: %v, want DIAL_FAILED", err)
	}
}

// The first client of a daemon that never started finds no directory at all:
// nobody listens, which is DIAL_FAILED — not a directory another account
// made unsafe.
func TestADialBeforeTheDirectoryExistsIsNobodyListening(t *testing.T) {
	cfg := new(ipc.Config{Path: filepath.Join(socketDir(t), "never", "d.sock")})
	if _, err := ipc.Dial(t.Context(), cfg); !errs.HasCode(err, ipc.CodeDialFailed) {
		t.Errorf("Dial with no directory: %v, want DIAL_FAILED", err)
	}
}

func TestAcceptAfterCloseIsClosed(t *testing.T) {
	ln, err := ipc.NewListener(&ipc.Config{Path: filepath.Join(socketDir(t), "d.sock")})
	if err != nil {
		t.Fatal(err)
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ln.Accept(); !errs.HasCode(err, ipc.CodeClosed) {
		t.Errorf("Accept after Close: %v, want CLOSED", err)
	}
}

func TestRuntimeDirPrefersTheServiceManagers(t *testing.T) {
	t.Setenv("RUNTIME_DIRECTORY", "/run/statusline")
	if got := ipc.RuntimeDir("statusline"); got != "/run/statusline" {
		t.Errorf("RuntimeDir = %q", got)
	}
	t.Setenv("RUNTIME_DIRECTORY", "")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	if got := ipc.RuntimeDir("statusline"); got != "/run/user/1000/statusline" {
		t.Errorf("RuntimeDir = %q", got)
	}
}
