//go:build unix || windows

package ipc

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The receiver role of TestHandOverAListener: this test binary,
// run again, dials the socket handoverPathEnv names, takes the listener the
// test passes and serves one connection on it.
const (
	// handoverRoleEnv makes this binary the receiver.
	handoverRoleEnv = "SDK_IPC_HANDOVER_RECEIVER"
	// handoverPathEnv is the private socket the receiver dials.
	handoverPathEnv = "SDK_IPC_HANDOVER_PATH"
	// handoverLifetime bounds the receiver's life, whatever the test does.
	handoverLifetime = 30 * time.Second
	// passDeadline bounds every wait of the descriptor-passing tests, so a
	// defect is a failure and not a hang.
	passDeadline = 5 * time.Second
	// handoverHeld is the byte the receiver answers once it holds the listener.
	handoverHeld = 'H'
)

// The receiver's exit statuses: each names the step that failed.
const (
	receiverServed  = 0
	receiverDial    = 10
	receiverRecv    = 11
	receiverListen  = 12
	receiverAccept  = 13
	receiverEcho    = 14
	receiverExpired = 15
)

// runHandoverReceiver is the receiver role, when this process plays it: it
// returns false at once otherwise. It starts nothing, so a receiver never
// starts a receiver, and it ends within handoverLifetime.
func runHandoverReceiver() bool {
	if os.Getenv(handoverRoleEnv) == "" {
		return false
	}
	time.AfterFunc(handoverLifetime, func() { os.Exit(receiverExpired) })
	os.Exit(serveHandedListener(os.Getenv(handoverPathEnv)))
	return true
}

// serveHandedListener dials path, takes the one listener passed over it,
// answers handoverHeld, accepts one connection on the listener and echoes
// "pong" to its "ping".
func serveHandedListener(path string) int {
	conn, err := Dial(context.Background(), &Config{Path: path})
	if err != nil {
		return receiverDial
	}
	defer conn.Close()
	receiver, ok := conn.Conn.(coreipc.FileReceiver)
	if !ok {
		return receiverRecv
	}
	buf := make([]byte, 8)
	n, files, err := receiver.RecvFiles(buf, 1)
	if err != nil || n != 1 || len(files) != 1 {
		return receiverRecv
	}
	ln, err := net.FileListener(files[0])
	_ = files[0].Close()
	if err != nil {
		return receiverListen
	}
	defer ln.Close()
	if _, err := conn.Write([]byte{handoverHeld}); err != nil {
		return receiverListen
	}
	if err := ln.(*net.TCPListener).SetDeadline(time.Now().Add(passDeadline)); err != nil {
		return receiverAccept
	}
	c, err := ln.Accept()
	if err != nil {
		return receiverAccept
	}
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(passDeadline)); err != nil {
		return receiverEcho
	}
	got := make([]byte, 4)
	if m, err := c.Read(got); err != nil || string(got[:m]) != "ping" {
		return receiverEcho
	}
	if _, err := c.Write([]byte("pong")); err != nil {
		return receiverEcho
	}
	return receiverServed
}

// receiverEnviron is this process's environment for the receiver: the role
// and the path it may have inherited taken out before they are set.
func receiverEnviron(path string) []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		name = strings.ToUpper(name)
		return name == handoverRoleEnv || name == handoverPathEnv
	})
	return append(env, handoverRoleEnv+"=1", handoverPathEnv+"="+path)
}

// receiverRun is one receiving process: done closes once it has ended, and
// state is then its end.
type receiverRun struct {
	proc  *os.Process
	done  chan struct{}
	state *os.ProcessState
}

// startReceiver runs this binary as the receiver of path. The cleanup kills
// it if it still runs and logs what it wrote, on a failed test too.
//
// Goroutine lifecycle: one goroutine waits for the receiver and closes done;
// the kill in the cleanup bounds it.
func startReceiver(t *testing.T, path string) *receiverRun {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("no executable to run again: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "receiver.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	p, err := os.StartProcess(exe, []string{exe, "-test.run=^TestHandOverAListener$", "-test.count=1"},
		&os.ProcAttr{Env: receiverEnviron(path), Files: []*os.File{null, log, log}})
	if err != nil {
		t.Fatalf("starting the receiver: %v", err)
	}
	r := &receiverRun{proc: p, done: make(chan struct{})}
	go func() {
		r.state, _ = p.Wait()
		close(r.done)
	}()
	t.Cleanup(func() {
		_ = p.Kill()
		select {
		case <-r.done:
		case <-time.After(handoverLifetime):
			t.Errorf("the receiver %d outlived its kill", p.Pid)
		}
		if t.Failed() {
			if out, rerr := os.ReadFile(logPath); rerr == nil && len(out) > 0 {
				t.Logf("the receiver wrote: %s", out)
			}
		}
	})
	return r
}

// acceptOne accepts the receiver's connection on ln within passDeadline.
//
// Goroutine lifecycle: one goroutine accepts once and reports on a buffered
// channel; closing ln, which the caller's cleanup does, ends it otherwise.
func acceptOne(t *testing.T, ln *Listener) *coreipc.Conn {
	t.Helper()
	accepted := make(chan *coreipc.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			accepted <- nil
			return
		}
		accepted <- c
	}()
	select {
	case c := <-accepted:
		if c == nil {
			t.Fatal("Accept failed")
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	case <-time.After(handoverLifetime):
		t.Fatal("the receiver never dialed")
		return nil
	}
}

// TestHandOverAListener pins #936's first criterion with a
// receiving process: a bound TCP listener passed with SendFiles over a
// private socket under the test's own directory is taken by another process
// — this binary run again —, whose Accept completes a connection end to end
// — a ping in, a pong back. The sender's own listener and descriptor are
// closed right after the send, so what accepts is only what crossed. On
// Windows the listener's file is a socket, passed by WSADuplicateSocketW and
// made again in the receiver with WSASocketW. Where the kernel does not name
// the peer (darwin, OpenBSD, illumos, Solaris) the send is asserted refused,
// with nothing passed, and the test stops there: ADR 0148 makes the
// directory the gate on those.
func TestHandOverAListener(t *testing.T) {
	if runHandoverReceiver() {
		return
	}
	t.Parallel()
	//: short: the test's name is in t.TempDir, and sun_path holds 104 bytes on darwin.
	path := filepath.Join(t.TempDir(), "r", "h.sock")
	ln, err := NewListener(&Config{Path: path})
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no TCP on the loopback: %v", err)
	}
	defer tcp.Close()
	addr := tcp.Addr().String()
	listenerFile, err := tcp.(*net.TCPListener).File()
	if err != nil {
		t.Fatalf("the listener has no file: %v", err)
	}
	defer listenerFile.Close()
	receiver := startReceiver(t, path)
	conn := acceptOne(t, ln)
	sender, ok := conn.Conn.(coreipc.FileSender)
	if !ok {
		t.Fatal("a private connection offers no FileSender")
	}
	sendErr := sender.SendFiles([]byte("L"), listenerFile)
	if !conn.Peer.Verified {
		if !errs.HasCode(sendErr, coreipc.CodeFDPassRefused) {
			t.Fatalf("SendFiles to a peer the kernel does not name = %v, want FD_PASS_REFUSED", sendErr)
		}
		t.Skip("this kernel names no peer, so no descriptor is passed (ADR 0148)")
	}
	if sendErr != nil {
		t.Fatalf("SendFiles = %v", sendErr)
	}
	//: the sender lets go of its two: from here the socket listens only
	//: through what the receiving process holds.
	_ = listenerFile.Close()
	_ = tcp.Close()
	if err := conn.SetDeadline(time.Now().Add(handoverLifetime)); err != nil {
		t.Fatalf("SetDeadline = %v", err)
	}
	held := make([]byte, 1)
	if _, err := conn.Read(held); err != nil || held[0] != handoverHeld {
		t.Fatalf("the receiver answered %q, %v; want it to hold the listener", held, err)
	}
	dialer := net.Dialer{Timeout: passDeadline}
	dialed, err := dialer.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatalf("dialing the passed listener at %s: %v", addr, err)
	}
	defer dialed.Close()
	if err := dialed.SetDeadline(time.Now().Add(passDeadline)); err != nil {
		t.Fatalf("SetDeadline = %v", err)
	}
	if _, err := dialed.Write([]byte("ping")); err != nil {
		t.Fatalf("Write = %v", err)
	}
	got := make([]byte, 4)
	if m, err := dialed.Read(got); err != nil || string(got[:m]) != "pong" {
		t.Fatalf("the receiving process answered %q, %v; want pong", got[:m], err)
	}
	select {
	case <-receiver.done:
		if receiver.state == nil || receiver.state.ExitCode() != receiverServed {
			t.Fatalf("the receiver %d ended %v, want %d", receiver.proc.Pid, receiver.state, receiverServed)
		}
	case <-time.After(handoverLifetime):
		t.Fatalf("the receiver %d did not end", receiver.proc.Pid)
	}
}
