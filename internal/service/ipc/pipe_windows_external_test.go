//go:build windows

package ipc_test

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/ipc"
)

// closeOrFail closes c at the end of the test, failing it when the close
// does.
func closeOrFail(t *testing.T, c io.Closer) {
	t.Helper()
	t.Cleanup(func() {
		if err := c.Close(); err != nil && !errors.Is(err, os.ErrClosed) && !errs.HasCode(err, ipc.CodeClosed) {
			t.Errorf("close: %v", err)
		}
	})
}

// pipeConfig is a configuration whose pipe no other test uses.
func pipeConfig(t *testing.T) *ipc.Config {
	t.Helper()
	return &ipc.Config{Path: filepath.Join(t.TempDir(), "p.sock")}
}

// A client reaches the listener through the named pipe, each end names the
// other's account — this one —, and a line goes both ways.
//
// Goroutine lifecycle: one goroutine accepts once and reports on a buffered
// channel, which the test reads.
func TestAPipeCarriesALineBothWays(t *testing.T) {
	cfg := pipeConfig(t)
	ln, err := ipc.NewListener(cfg)
	if err != nil {
		t.Fatal(err)
	}
	closeOrFail(t, ln)
	type accepted struct {
		c   *ipc.Conn
		err error
	}
	got := make(chan accepted, 1)
	go func() {
		c, err := ln.Accept()
		got <- accepted{c, err}
	}()
	c, err := ipc.Dial(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	closeOrFail(t, c)
	a := <-got
	if a.err != nil {
		t.Fatal(a.err)
	}
	closeOrFail(t, a.c)
	if !c.Peer.Verified || c.Peer.SID == "" || c.Peer.PID != os.Getpid() {
		t.Errorf("the client's view of the listener: %+v", c.Peer)
	}
	if !a.c.Peer.Verified || a.c.Peer.SID != c.Peer.SID || a.c.Peer.PID != os.Getpid() {
		t.Errorf("the listener's view of the client: %+v", a.c.Peer)
	}
	if _, err := c.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(a.c).ReadString('\n')
	if err != nil || line != "ping\n" {
		t.Fatalf("read %q, %v", line, err)
	}
	if _, err := a.c.Write([]byte("pong\n")); err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(c).ReadString('\n'); err != nil || line != "pong\n" {
		t.Fatalf("read %q, %v", line, err)
	}
	if ln.Refused() != 0 {
		t.Errorf("refused %d", ln.Refused())
	}
}

// A second listener on the same path is refused while the first lives:
// FILE_FLAG_FIRST_PIPE_INSTANCE, whoever holds the name.
func TestASecondPipeListenerIsInUse(t *testing.T) {
	cfg := pipeConfig(t)
	ln, err := ipc.NewListener(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ipc.NewListener(cfg); !errs.HasCode(err, ipc.CodeInUse) {
		t.Errorf("second listener: %v, want IN_USE", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := ipc.NewListener(cfg)
	if err != nil {
		t.Fatalf("after close: %v", err)
	}
	if err := again.Close(); err != nil {
		t.Error(err)
	}
}

// Close ends a waiting Accept with CLOSED, and a dial to a pipe nobody
// listens on fails.
//
// Goroutine lifecycle: one goroutine waits in Accept and reports on a
// buffered channel; the Close ends it, which the test waits for.
func TestClosingAPipeListenerEndsItsAccept(t *testing.T) {
	cfg := pipeConfig(t)
	ln, err := ipc.NewListener(cfg)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := ln.Accept()
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errs.HasCode(err, ipc.CodeClosed) {
			t.Errorf("accept after close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Accept did not return after Close")
	}
	if _, err := ipc.Dial(t.Context(), cfg); !errs.HasCode(err, ipc.CodeDialFailed) {
		t.Errorf("dial with nobody listening: %v", err)
	}
}

// A read deadline on a pipe connection expires as on a socket: the handles
// are on the runtime's poller.
//
// Goroutine lifecycle: one goroutine accepts once and holds the connection
// for a second, then closes it; the listener's close at the end of the test
// ends an Accept still waiting.
func TestAPipeReadDeadlineExpires(t *testing.T) {
	cfg := pipeConfig(t)
	ln, err := ipc.NewListener(cfg)
	if err != nil {
		t.Fatal(err)
	}
	closeOrFail(t, ln)
	go func() {
		if c, err := ln.Accept(); err == nil {
			closeOrFail(t, c)
			time.Sleep(time.Second)
		}
	}()
	c, err := ipc.Dial(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	closeOrFail(t, c)
	if err := c.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("read past the deadline: %v", err)
	}
}
