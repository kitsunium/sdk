//go:build unix

// Package sdlisten_test — black-box tests for socket activation (issue #75): the
// activator Prepare + service Files/Listeners round-trip end-to-end without
// systemd (the test binary re-execs itself as the activated child), plus the
// LISTEN_PID-gating and empty-env contracts.
package sdlisten_test

import (
	"context"
	"io"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	svcexec "github.com/kitsunium/sdk/internal/service/proc/exec"
	"github.com/kitsunium/sdk/internal/service/proc/sdlisten"
)

// helperEnv, when set, makes the re-exec'd test binary act as the activated
// child instead of running the suite.
const helperEnv string = "SDK_SDLISTEN_TEST_HELPER"

// helperToken is what the activated child writes back so the parent can confirm
// the passed socket round-tripped.
const helperToken string = "activated"

// drop intentionally discards a best-effort cleanup error in the helper child,
// which has no *testing.T to log through.
func drop(err error) {
	//: read the parameter so the unused-error audit treats this as intentional.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
}

// TestMain dispatches the activated-child role on helperEnv, so the suite can
// spawn itself as a socket-activated process.
func TestMain(m *testing.M) {
	//: the re-exec'd child runs the helper and exits before the suite.
	if os.Getenv(helperEnv) == "1" {
		//: act as the activated service, then exit with its status.
		os.Exit(runActivatedChild())
	}
	//: the normal path runs the test suite.
	os.Exit(m.Run())
}

// runActivatedChild recovers its inherited listener, accepts one connection,
// writes the token, and returns a process exit status.
func runActivatedChild() int {
	lns, err := sdlisten.Listeners(true)
	//: exactly one socket must have been recovered from the activation env.
	if err != nil || len(lns) != 1 {
		//: a recovery failure is a distinct non-zero status for diagnosis.
		return 2
	}
	conn, aerr := lns[0].Accept()
	//: the parent dials immediately, so Accept should not fail.
	if aerr != nil {
		//: an accept failure is its own status.
		return 3
	}
	//: prove the passed socket works by writing the agreed token.
	if _, werr := conn.Write([]byte(helperToken)); werr != nil {
		//: a write failure is its own status.
		return 4
	}
	//: release the connection and report success.
	drop(conn.Close())
	return 0
}

// TestRoundTrip binds a listener, hands it to a re-exec'd child via Prepare, and
// asserts the child accepts a connection on the passed socket — the acceptance
// "round-trip without systemd" contract.
func TestRoundTrip(t *testing.T) {
	self, err := os.Executable()
	//: the round-trip re-execs this test binary as the activated child.
	if err != nil {
		t.Skipf("cannot resolve test binary: %v", err)
	}
	ln, lerr := net.Listen("tcp", "127.0.0.1:0")
	//: a bound listener is the socket to hand off.
	if lerr != nil {
		t.Fatalf("Listen: %v", lerr)
	}
	addr := ln.Addr().String()

	spec := coreproc.Spec{Path: self, Args: []string{"sdlisten-child"}, Env: []string{helperEnv + "=1"}}
	//: Prepare dups the listener into the child spec and sets the activation env.
	if perr := sdlisten.Prepare(&spec, map[string]net.Listener{"http": ln}); perr != nil {
		t.Fatalf("Prepare: %v", perr)
	}
	//: the parent stops listening so only the child's inherited dup accepts.
	if cerr := ln.Close(); cerr != nil {
		t.Logf("parent Listener close: %v", cerr)
	}

	p, serr := svcexec.Start(context.Background(), spec)
	//: release the parent's copies of the passed fds once the child owns its dups.
	closeExtra(t, spec.ExtraFiles)
	//: a spawn failure aborts the round-trip.
	if serr != nil {
		t.Fatalf("Start child: %v", serr)
	}

	//: dial the passed socket; the child must accept and return the token.
	got := dialAndRead(t, addr)
	//: the token proves the inherited socket is the same listener.
	if got != helperToken {
		t.Fatalf("round-trip payload = %q, want %q", got, helperToken)
	}
	//: the child exits 0 after serving one connection.
	if exit, werr := p.Wait(); werr != nil || exit.Code != 0 {
		t.Fatalf("child exit = %d err=%v, want 0/nil", exit.Code, werr)
	}
}

// closeExtra releases the parent's copies of the dup'd activation fds.
func closeExtra(t *testing.T, files []*os.File) {
	t.Helper()
	//: close each parent-side dup; the child holds its own copies.
	for _, f := range files {
		//: a close fault here is benign cleanup detail.
		if cerr := f.Close(); cerr != nil {
			t.Logf("close extra fd: %v", cerr)
		}
	}
}

// dialAndRead connects to addr and returns everything the peer writes.
func dialAndRead(t *testing.T, addr string) string {
	t.Helper()
	conn, derr := net.DialTimeout("tcp", addr, 2*time.Second)
	//: the child's inherited listener must accept the connection.
	if derr != nil {
		t.Fatalf("Dial: %v", derr)
	}
	//: release the connection once read.
	defer func() {
		//: a close fault on the client side is non-actionable.
		if cerr := conn.Close(); cerr != nil {
			t.Logf("client close: %v", cerr)
		}
	}()
	//: read the token the child writes after Accept.
	data, rerr := io.ReadAll(conn)
	//: a read failure means the round-trip did not complete.
	if rerr != nil {
		t.Fatalf("read token: %v", rerr)
	}
	//: the bytes the activated child sent back.
	return string(data)
}

// TestPrepareSetsActivationEnv asserts Prepare appends the listener fd and sets
// LISTEN_FDS / LISTEN_FDNAMES (and omits LISTEN_PID) on the child spec.
func TestPrepareSetsActivationEnv(t *testing.T) {
	t.Parallel()

	ln, lerr := net.Listen("tcp", "127.0.0.1:0")
	//: a bound listener is required to prepare an activation.
	if lerr != nil {
		t.Fatalf("Listen: %v", lerr)
	}
	//: release the listener once Prepare has dup'd it.
	defer func() {
		//: the dup in spec.ExtraFiles keeps the socket alive after this close.
		if cerr := ln.Close(); cerr != nil {
			t.Logf("Listener close: %v", cerr)
		}
	}()

	spec := coreproc.Spec{Path: "/bin/true"}
	//: prepare a single named socket.
	if perr := sdlisten.Prepare(&spec, map[string]net.Listener{"http": ln}); perr != nil {
		t.Fatalf("Prepare: %v", perr)
	}
	//: release the dup'd fd at the end.
	defer closeExtra(t, spec.ExtraFiles)

	//: exactly one socket must have been appended for inheritance.
	if len(spec.ExtraFiles) != 1 {
		t.Fatalf("ExtraFiles = %d, want 1", len(spec.ExtraFiles))
	}
	//: the env must announce one fd named "http", with no LISTEN_PID.
	if !envHas(spec.Env, "LISTEN_FDS=1") || !envHas(spec.Env, "LISTEN_FDNAMES=http") {
		t.Fatalf("Env = %v, want LISTEN_FDS=1 + LISTEN_FDNAMES=http", spec.Env)
	}
	//: a pre-fork activator cannot know the child pid, so LISTEN_PID is omitted.
	if envHasKey(spec.Env, "LISTEN_PID") {
		t.Fatalf("Env carries LISTEN_PID, want it omitted: %v", spec.Env)
	}
}

// TestFilesForeignPidReturnsNothing asserts that fds addressed to a different pid
// (LISTEN_PID mismatch) are ignored — no fds, no error.
func TestFilesForeignPidReturnsNothing(t *testing.T) {
	//: not parallel — it mutates the process environment.
	t.Setenv("LISTEN_FDS", "1")
	//: address the fds to a pid that is not this process.
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()+1))
	files, err := sdlisten.Files(false)
	//: a foreign pid yields no files and no error.
	if err != nil || len(files) != 0 {
		t.Fatalf("Files(foreign pid) = %d files, err=%v, want 0/nil", len(files), err)
	}
}

// TestFilesEmptyEnvReturnsNothing asserts that an absent LISTEN_FDS yields no
// files and no error — the process was simply not socket-activated.
func TestFilesEmptyEnvReturnsNothing(t *testing.T) {
	//: not parallel — it clears the process environment.
	t.Setenv("LISTEN_FDS", "")
	files, err := sdlisten.Files(false)
	//: no activation env means an empty, non-error result.
	if err != nil || len(files) != 0 {
		t.Fatalf("Files(empty env) = %d files, err=%v, want 0/nil", len(files), err)
	}
}

// envHas reports whether env contains the exact entry want.
func envHas(env []string, want string) bool {
	//: an exact KEY=value match means the entry is present.
	return slices.Contains(env, want)
}

// envHasKey reports whether env contains any entry whose key is key.
func envHasKey(env []string, key string) bool {
	//: a "key=" prefix means the variable is set (to anything).
	return slices.ContainsFunc(env, func(kv string) bool {
		//: split off the key half and compare it.
		k, _, _ := strings.Cut(kv, "=")
		//: a key match confirms the variable is present.
		return k == key
	})
}
