//go:build unix

// Package sdlisten_test — black-box tests for socket activation (issue #75): the
// activator Prepare + service Files/Listeners round-trip end-to-end without
// systemd (the test binary re-execs itself as the activated child), plus the
// LISTEN_PID-gating and empty-env contracts.
package sdlisten_test

import (
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
// "round-trip without systemd" contract, and the only test here that proves the
// activator and service halves agree about which fd carries which socket.
func TestRoundTrip(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: the protocol name the socket is passed under; the child recovers by
		//: position, so any name must work.
		socket string
	}
	tests := []tc{
		{"a socket named http", "http"},
		{"a socket named metrics", "metrics"},
		{"an unnamed socket", ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		self, err := os.Executable()
		//: a test binary that cannot locate itself is a broken host, not a
		//: reason to quietly pass: the round trip re-execs it as the child.
		if err != nil {
			t.Fatalf("resolving the test binary: %v", err)
		}
		ln, lerr := net.Listen("tcp", "127.0.0.1:0")
		//: a bound listener is the socket to hand off.
		if lerr != nil {
			t.Fatalf("Listen: %v", lerr)
		}
		addr := ln.Addr().String()

		spec := coreproc.Spec{Path: self, Args: []string{"sdlisten-child"}, Env: []string{helperEnv + "=1"}}
		//: Prepare dups the listener into the child spec and sets the env.
		if perr := sdlisten.Prepare(&spec, map[string]net.Listener{c.socket: ln}); perr != nil {
			t.Fatalf("Prepare: %v", perr)
		}
		//: the parent stops listening so only the child's inherited dup accepts.
		if cerr := ln.Close(); cerr != nil {
			t.Logf("parent Listener close: %v", cerr)
		}

		p, serr := svcexec.Start(t.Context(), spec)
		//: release the parent's copies once the child owns its dups.
		closeExtra(t, spec.ExtraFiles)
		if serr != nil {
			t.Fatalf("Start child: %v", serr)
		}

		//: dial the passed socket; the child must accept and return the token.
		if got := dialAndRead(t, addr); got != helperToken {
			t.Fatalf("round-trip payload = %q, want %q", got, helperToken)
		}
		//: the child exits 0 after serving one connection.
		if exit, werr := p.Wait(); werr != nil || exit.Code != 0 {
			t.Fatalf("child exit = %d err=%v, want 0/nil", exit.Code, werr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
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

// TestPrepare asserts the activator half sets up exactly what the service half
// reads back: the sockets appended for inheritance, the count and names in the
// env, and — deliberately — NO LISTEN_PID.
//
// The omission is the subtle part. A pre-fork activator cannot know the child's
// pid, so writing one would either be wrong or force a post-fork rewrite of the
// child's environment; the service side accepts an absent LISTEN_PID from a
// trusted parent instead.
func TestPrepare(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: how many listeners to hand over, and under which names.
		names   []string
		wantFDs int
		//: the LISTEN_FDNAMES value expected, which is the names in SORTED
		//: order — the fd↔name pairing has to be deterministic.
		wantNames string
	}
	tests := []tc{
		{"nothing to pass", nil, 0, ""},
		{"a single named socket", []string{"http"}, 1, "http"},
		{"two sockets, already sorted", []string{"http", "metrics"}, 2, "http:metrics"},
		{"two sockets, declared out of order", []string{"metrics", "http"}, 2, "http:metrics"},
		{"three sockets", []string{"z", "a", "m"}, 3, "a:m:z"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		named := make(map[string]net.Listener, len(c.names))
		for _, name := range c.names {
			ln, lerr := net.Listen("tcp", "127.0.0.1:0")
			if lerr != nil {
				t.Fatalf("Listen: %v", lerr)
			}
			//: the dup in spec.ExtraFiles keeps the socket alive after this.
			defer func() {
				if cerr := ln.Close(); cerr != nil {
					t.Logf("Listener close: %v", cerr)
				}
			}()
			named[name] = ln
		}

		spec := coreproc.Spec{Path: "/bin/true", Env: []string{"LISTEN_FDS=99", "PATH=/usr/bin"}}
		if perr := sdlisten.Prepare(&spec, named); perr != nil {
			t.Fatalf("Prepare: %v", perr)
		}
		defer closeExtra(t, spec.ExtraFiles)

		if len(spec.ExtraFiles) != c.wantFDs {
			t.Fatalf("ExtraFiles = %d, want %d", len(spec.ExtraFiles), c.wantFDs)
		}
		//: an empty listener set leaves the caller's env exactly as it was.
		if c.wantFDs == 0 {
			if !envHas(spec.Env, "LISTEN_FDS=99") {
				t.Errorf("Prepare rewrote the env for an empty listener set: %v", spec.Env)
			}
			return
		}
		if !envHas(spec.Env, "LISTEN_FDS="+strconv.Itoa(c.wantFDs)) {
			t.Errorf("Env = %v, want LISTEN_FDS=%d", spec.Env, c.wantFDs)
		}
		if !envHas(spec.Env, "LISTEN_FDNAMES="+c.wantNames) {
			t.Errorf("Env = %v, want LISTEN_FDNAMES=%s", spec.Env, c.wantNames)
		}
		//: the caller's stale LISTEN_FDS must have been replaced, not doubled.
		if envHas(spec.Env, "LISTEN_FDS=99") {
			t.Errorf("Env still carries the caller's stale LISTEN_FDS: %v", spec.Env)
		}
		//: unrelated entries survive untouched.
		if !envHas(spec.Env, "PATH=/usr/bin") {
			t.Errorf("Prepare dropped an unrelated env entry: %v", spec.Env)
		}
		//: a pre-fork activator cannot know the child pid, so it is omitted.
		if envHasKey(spec.Env, "LISTEN_PID") {
			t.Errorf("Env carries LISTEN_PID, want it omitted: %v", spec.Env)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestFiles pins the gating: a set that is absent, empty, or addressed to
// another process yields nothing AND no error, while a malformed count is a
// typed failure.
//
// The distinction matters at startup. "Not socket-activated" is the normal case
// for a binary run from a shell and must not be an error, but "the activator
// wrote a garbled LISTEN_FDS" is a fault nobody else will report.
func TestFiles(t *testing.T) {
	//: not parallel — every case mutates the process environment.
	type tc struct {
		name      string
		listenFDs string
		listenPID string
		wantErr   bool
	}
	tests := []tc{
		{name: "no activation environment at all"},
		{name: "an explicitly empty count", listenFDs: ""},
		{name: "a zero count", listenFDs: "0"},
		{
			name:      "a set addressed to another process",
			listenFDs: "1",
			listenPID: strconv.Itoa(os.Getpid() + 1),
		},
		{
			//: a malformed LISTEN_PID is treated as a mismatch rather than as
			//: permission to read fds that may belong to someone else.
			name:      "a non-numeric pid",
			listenFDs: "1",
			listenPID: "not-a-pid",
		},
		{name: "a non-numeric count", listenFDs: "many", wantErr: true},
		{name: "a negative count", listenFDs: "-1", wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		t.Setenv("LISTEN_FDS", c.listenFDs)
		t.Setenv("LISTEN_PID", c.listenPID)

		files, err := sdlisten.Files(false)
		if c.wantErr {
			//: a broken activator must be reported, not absorbed.
			if err == nil {
				t.Fatalf("Files with %s = nil, want an error", c.name)
			}
			return
		}
		if err != nil {
			t.Fatalf("Files with %s = %v, want nil", c.name, err)
		}
		if len(files) != 0 {
			t.Errorf("Files with %s recovered %d descriptors", c.name, len(files))
			closeExtra(t, files)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// TestListeners pins that the listener view inherits the same gating as Files —
// it is the entry point most services call, so a difference between the two
// would be a difference nobody notices until production.
func TestListeners(t *testing.T) {
	//: not parallel — every case mutates the process environment.
	type tc struct {
		name      string
		listenFDs string
		wantErr   bool
	}
	tests := []tc{
		{name: "no activation environment at all"},
		{name: "a zero count", listenFDs: "0"},
		{name: "a non-numeric count", listenFDs: "many", wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		t.Setenv("LISTEN_FDS", c.listenFDs)
		t.Setenv("LISTEN_PID", "")

		lns, err := sdlisten.Listeners(false)
		if c.wantErr {
			if err == nil {
				t.Fatalf("Listeners with %s = nil, want an error", c.name)
			}
			return
		}
		if err != nil {
			t.Fatalf("Listeners with %s = %v, want nil", c.name, err)
		}
		if len(lns) != 0 {
			t.Errorf("Listeners with %s recovered %d listeners", c.name, len(lns))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// TestWithNames pins the grouped view under the same gating. Grouping by name
// is what lets one activator hand a service several sockets for one protocol,
// so an empty set must be an empty map rather than a nil no-one can range over
// safely.
func TestWithNames(t *testing.T) {
	//: not parallel — every case mutates the process environment.
	type tc struct {
		name      string
		listenFDs string
		wantErr   bool
	}
	tests := []tc{
		{name: "no activation environment at all"},
		{name: "a zero count", listenFDs: "0"},
		{name: "a non-numeric count", listenFDs: "many", wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		t.Setenv("LISTEN_FDS", c.listenFDs)
		t.Setenv("LISTEN_PID", "")

		byName, err := sdlisten.WithNames(false)
		if c.wantErr {
			if err == nil {
				t.Fatalf("WithNames with %s = nil, want an error", c.name)
			}
			return
		}
		if err != nil {
			t.Fatalf("WithNames with %s = %v, want nil", c.name, err)
		}
		if len(byName) != 0 {
			t.Errorf("WithNames with %s recovered %d groups", c.name, len(byName))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
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
