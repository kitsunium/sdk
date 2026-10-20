//go:build unix

package ipc_test

import (
	"bufio"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/ipc"
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
	done := make(chan coreipc.PeerValue, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			t.Error(err)
			done <- coreipc.PeerValue{}
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
	if _, err := ipc.NewListener(&cfg); !errs.HasCode(err, coreipc.CodeInUse) {
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
	if _, err := ipc.NewListener(&cfg); !errs.HasCode(err, coreipc.CodeDirectoryUnsafe) {
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
	if _, err := ipc.NewListener(&cfg); !errs.HasCode(err, coreipc.CodeDirectoryUnsafe) {
		t.Errorf("Listen in a world-writable directory: %v", err)
	}
	if _, err := ipc.Dial(t.Context(), &cfg); !errs.HasCode(err, coreipc.CodeDirectoryUnsafe) {
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
	if _, err := ipc.NewListener(&ipc.Config{Path: filepath.Join(link, "d.sock")}); !errs.HasCode(err, coreipc.CodeDirectoryUnsafe) {
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
		if _, err := ipc.NewListener(&cfg); !errs.HasCode(err, coreipc.CodeMisconfigured) {
			t.Errorf("NewListener(%+v) = %v, want MISCONFIGURED", cfg, err)
		}
	}
}

func TestADialToNothingSaysSo(t *testing.T) {
	cfg := ipc.Config{Path: filepath.Join(socketDir(t), "none.sock")}
	if err := os.MkdirAll(filepath.Dir(cfg.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ipc.Dial(t.Context(), &cfg); !errs.HasCode(err, coreipc.CodeDialFailed) {
		t.Errorf("Dial to no listener: %v, want DIAL_FAILED", err)
	}
}

// The first client of a daemon that never started finds no directory at all:
// nobody listens, which is DIAL_FAILED — not a directory another account
// made unsafe.
func TestADialBeforeTheDirectoryExistsIsNobodyListening(t *testing.T) {
	cfg := new(ipc.Config{Path: filepath.Join(socketDir(t), "never", "d.sock")})
	if _, err := ipc.Dial(t.Context(), cfg); !errs.HasCode(err, coreipc.CodeDialFailed) {
		t.Errorf("Dial with no directory: %v, want DIAL_FAILED", err)
	}
}

// TestTheEnginesAreReachedThroughThePorts accepts and dials through the core
// ports alone, the way code that holds a coreipc.Listener and a
// coreipc.Dialer — and is handed these engines in production — does
// (ADR 0160). A dialer refuses its configuration before it touches anything,
// as the listener does.
//
// Goroutine lifecycle: one goroutine accepts one connection, closes it and
// reports on a buffered channel; the test waits on that channel.
func TestTheEnginesAreReachedThroughThePorts(t *testing.T) {
	if _, err := ipc.NewDialer(&ipc.Config{Path: "relative.sock"}); !errs.HasCode(err, coreipc.CodeMisconfigured) {
		t.Errorf("NewDialer with a relative path: %v, want MISCONFIGURED", err)
	}
	cfg := ipc.Config{Path: filepath.Join(socketDir(t), "p.sock")}
	engine, err := ipc.NewListener(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	var ln coreipc.Listener = engine
	defer closeOrLog(t, ln)
	dialer, err := ipc.NewDialer(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	var d coreipc.Dialer = dialer
	accepted := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			closeOrLog(t, c)
		}
		accepted <- err
	}()
	c, err := d.Dial(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	closeOrLog(t, c)
	if err := <-accepted; err != nil {
		t.Errorf("Accept through the port: %v", err)
	}
	if ln.Path() != cfg.Path || ln.Refused() != 0 || ln.Addr() == nil {
		t.Errorf("the port's view: path %q, refused %d, addr %v", ln.Path(), ln.Refused(), ln.Addr())
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
	if _, err := ln.Accept(); !errs.HasCode(err, coreipc.CodeClosed) {
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

// TestRuntimeDirTakesTheFirstOfSeveral pins issue #455: with several
// RuntimeDirectory= systemd joins their absolute paths with ':' in
// $RUNTIME_DIRECTORY (systemd.exec(5)), and the list as a whole names no
// directory. The first entry is taken; one that is empty or not absolute
// passes the chain on, as an unusable single value does.
func TestRuntimeDirTakesTheFirstOfSeveral(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	for _, c := range []struct{ value, want string }{
		{"/run/app", "/run/app"},
		{"/run/app:/run/app-extra", "/run/app"},
		{"/run/app:/run/app-extra:/run/app-third", "/run/app"},
		{"/run/app:", "/run/app"},
		{":/run/app", "/run/user/1000/app"},
		{"relative:/run/app", "/run/user/1000/app"},
		{":", "/run/user/1000/app"},
	} {
		t.Setenv("RUNTIME_DIRECTORY", c.value)
		if got := ipc.RuntimeDirOr("app", "/fallback"); got != c.want {
			t.Errorf("RUNTIME_DIRECTORY=%q: RuntimeDirOr = %q, want %q", c.value, got, c.want)
		}
		if got := ipc.RuntimeDir("app"); got != c.want {
			t.Errorf("RUNTIME_DIRECTORY=%q: RuntimeDir = %q, want %q", c.value, got, c.want)
		}
	}
}

// TestABusyLiveSocketIsNeverTakenOver pins issue #452: a live owner whose
// accept queue is full answers a dial with EAGAIN on Linux, and the stale
// check read every failed dial as "nobody listens" — it removed the running
// server's socket and bound over it. Only ECONNREFUSED (or the file gone)
// makes a socket a leftover now.
//
// The BSDs and macOS answer a full backlog with ECONNREFUSED itself, which a
// dead socket gives too: there the two cannot be told apart, and the case is
// Linux's.
func TestABusyLiveSocketIsNeverTakenOver(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("%s answers a full accept queue with ECONNREFUSED, as it answers a dead socket", runtime.GOOS)
	}
	cfg := ipc.Config{Path: filepath.Join(socketDir(t), "d.sock")}
	if err := os.MkdirAll(filepath.Dir(cfg.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	//: a live owner that never accepts, its backlog as short as the kernel
	//: allows, so a handful of dials fill it.
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := syscall.Close(fd); err != nil {
			t.Logf("close: %v", err)
		}
	}()
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: cfg.Path}); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Listen(fd, 0); err != nil {
		t.Fatal(err)
	}
	full := false
	for range 64 {
		c, err := net.Dial("unix", cfg.Path)
		if err != nil {
			if !errors.Is(err, syscall.EAGAIN) {
				t.Fatalf("filling the accept queue: %v, want EAGAIN", err)
			}
			full = true
			break
		}
		defer closeOrLog(t, c)
	}
	if !full {
		t.Fatal("64 connections never filled a backlog of 0")
	}
	before, err := os.Stat(cfg.Path)
	if err != nil {
		t.Fatal(err)
	}

	ln, err := ipc.NewListener(&cfg)

	if err == nil {
		closeOrLog(t, ln)
		t.Fatal("a busy live socket was taken over")
	}
	if !errs.HasCode(err, coreipc.CodeInUse) {
		t.Fatalf("a listener on a busy live socket: %v, want IN_USE", err)
	}
	after, err := os.Stat(cfg.Path)
	if err != nil {
		t.Fatalf("the refused listener removed the live socket: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Error("the live socket's file was replaced")
	}
}

func TestRuntimeDirOrEndsOnTheCallersDirectory(t *testing.T) {
	t.Setenv("RUNTIME_DIRECTORY", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("LOCALAPPDATA", "")
	if got := ipc.RuntimeDirOr("statusline", "/home/u/.local/state/statusline/run"); got != "/home/u/.local/state/statusline/run" {
		t.Errorf("RuntimeDirOr = %q, want the caller's fallback", got)
	}
	if got := ipc.RuntimeDir("statusline"); got != filepath.Join(os.TempDir(), "statusline-"+strconv.Itoa(os.Geteuid())) {
		t.Errorf("RuntimeDir = %q, want <tmp>/<app>-<uid>", got)
	}
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	if got := ipc.RuntimeDirOr("statusline", "/elsewhere"); got != "/run/user/1000/statusline" {
		t.Errorf("RuntimeDirOr = %q, want $XDG_RUNTIME_DIR/<app> before the fallback", got)
	}
}
