//go:build unix

// Package exec — the best-effort post-start attributes. "Best effort" here means
// the kernel may refuse; it does NOT mean the refusal is dropped, which is the
// distinction these tests exist to hold.
package exec

import (
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"runtime"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// absentPID is a pid chosen to be absent on any realistic host, so the refusal
// paths fail deterministically without needing privileges.
const absentPID int = 0x7fff_fffe

// Test_applyNice pins that a nil Nice is a genuine no-op and a refusal is
// reported. Lowering niceness needs privilege, so a silent drop here would mean
// a service that asked to be deprioritised quietly running at normal priority —
// invisible until it starves something.
//
// Every niceness is applied to a disposable child (niceChild), never to the
// test process: an unprivileged process cannot lower its niceness back, and a
// target computed from the niceness read earlier became a refused LOWERING
// once Test_applyPostStart, or the previous run of `go test -count=N`, had
// raised it further — on Linux, where niceness is per thread, the read and the
// write did not even reach the same thread.
func Test_applyNice(t *testing.T) {
	t.Parallel()
	lower := -20

	type tc struct {
		name string
		// pid is the target; zero means a fresh disposable child.
		pid int
		// delta is the niceness asked for, relative to the child's own; nil
		// asks for none.
		delta   *int
		wantErr bool
	}
	five := 5
	tests := []tc{
		{name: "a nil Nice touches nothing"},
		//: raising niceness needs no privilege, so this must succeed on any
		//: host.
		{name: "raising a child's niceness", delta: &five},
		//: a pid that cannot exist fails whatever the privileges.
		{name: "a process that does not exist", pid: absentPID, delta: &five, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		pid := c.pid
		if pid == 0 {
			pid = niceChild(t)
		}
		var nice *int
		var before int
		if c.delta != nil {
			//: relative to the niceness the child inherited: a runner that
			//: starts at nice 15 (a capped CI slice) cannot go back to 5. An
			//: absent pid has none, so it is asked the test process's.
			base := pid
			if c.wantErr {
				base = os.Getpid()
			}
			raised := raisedNice(t, base, *c.delta)
			nice = &raised
		}
		if !c.wantErr {
			before = niceOf(t, pid)
		}
		err := applyNice(pid, nice)
		if c.wantErr {
			if !errs.HasCode(err, coreproc.CodeRlimitFailed) {
				t.Fatalf("applyNice(%s) = %v, want RLIMIT_FAILED", c.name, err)
			}
			//: the pid rides along, or a supervisor cannot say WHICH child it
			//: failed to reprioritise.
			if !hasField(err, "pid") {
				t.Errorf("the error carries no pid: %v", errs.FieldsOf(err))
			}
			return
		}
		if err != nil {
			t.Fatalf("applyNice(%s) = %v, want nil", c.name, err)
		}
		//: the niceness the child now has is the one asked for, or the one it
		//: had when nothing was asked.
		want := before
		if nice != nil {
			want = *nice
		}
		if got := niceOf(t, pid); got != want {
			t.Errorf("the child's niceness = %d, want %d", got, want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: lowering niceness from an unprivileged process is refused by the kernel,
	//: and that refusal must be reported rather than swallowed. Where the test
	//: DOES have the privilege the call succeeds, which is equally correct — so
	//: the assertion is that the outcome is never a silent partial. On a child:
	//: a privileged run must not leave the test process at -20 either.
	err := applyNice(niceChild(t), &lower)
	if err != nil && !errs.HasCode(err, coreproc.CodeRlimitFailed) {
		t.Errorf("applyNice(lower) = %v, want nil or RLIMIT_FAILED", err)
	}
}

// niceChild starts a disposable child for a test to reprioritise in place of
// the test process, and returns its pid. The child is a shell blocked reading
// a pipe the test holds; the cleanup closes the pipe, the shell reads EOF and
// exits on its own, and the cleanup reaps it — no signal, so nothing depends
// on a signal NetBSD up to 10.1 discards during execve (PR kern/58091).
func niceChild(t *testing.T) int {
	t.Helper()
	cmd := osexec.Command(shellPath, "-c", "read x")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the disposable child: %v", err)
	}
	t.Cleanup(func() {
		if cerr := stdin.Close(); cerr != nil {
			t.Logf("closing the child's stdin: %v", cerr)
		}
		//: read fails at EOF, so the shell exits 1: only the reap matters.
		if werr := cmd.Wait(); werr != nil {
			var exitErr *osexec.ExitError
			if !errors.As(werr, &exitErr) {
				t.Errorf("reaping the disposable child: %v", werr)
			}
		}
	})
	return cmd.Process.Pid
}

// niceOf is pid's current niceness, read with getpriority(2).
func niceOf(t *testing.T, pid int) int {
	t.Helper()
	prio, err := syscall.Getpriority(prioProcess, pid)
	if err != nil {
		t.Fatalf("getpriority(%d): %v", pid, err)
	}
	//: Linux's raw getpriority(2) answers 20 - nice so the value is never
	//: negative; the BSDs and macOS answer the niceness itself.
	if runtime.GOOS == "linux" {
		return 20 - prio
	}
	return prio
}

// raisedNice is pid's current niceness raised by delta, capped at the
// kernel's 19: a value applyNice may set on pid without privilege whatever
// niceness it inherited.
func raisedNice(t *testing.T, pid, delta int) int {
	t.Helper()
	return min(niceOf(t, pid)+delta, 19)
}

// Test_applyOOMScoreAdj pins the same contract on the procfs write. A nil
// pointer leaves the inherited bias; anything else must either land or be
// reported, because a service that asked to be the OOM killer's first choice
// and silently was not is a production surprise nobody can see coming.
func Test_applyOOMScoreAdj(t *testing.T) {
	t.Parallel()
	neutral := 0
	outOfRange := 5000

	type tc struct {
		name    string
		pid     int
		adj     *int
		wantErr bool
	}
	tests := []tc{
		{name: "a nil adjustment touches nothing", pid: os.Getpid()},
		{
			name:    "a process that does not exist",
			pid:     absentPID,
			adj:     &neutral,
			wantErr: true,
		},
		{
			//: the kernel validates the -1000..1000 range, so an out-of-range
			//: value is refused by procfs rather than by us.
			name:    "a value outside the kernel's range",
			pid:     os.Getpid(),
			adj:     &outOfRange,
			wantErr: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := applyOOMScoreAdj(c.pid, c.adj)
		if c.wantErr {
			if !errs.HasCode(err, coreproc.CodeRlimitFailed) {
				t.Fatalf("applyOOMScoreAdj(%s) = %v, want RLIMIT_FAILED", c.name, err)
			}
			if !hasField(err, "pid") {
				t.Errorf("the error carries no pid: %v", errs.FieldsOf(err))
			}
			return
		}
		if err != nil {
			t.Fatalf("applyOOMScoreAdj(%s) = %v, want nil", c.name, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_containsESRCH pins the "already gone" detector. It decides whether a
// failed signal is reported as an error or treated as success, so a false
// negative turns a normal race — the child exited between the check and the
// kill — into a spurious failure.
func Test_containsESRCH(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		err  error
		want bool
	}
	tests := []tc{
		{"the bare errno", syscall.ESRCH, true},
		{"a wrapped errno", fmt.Errorf("signalling: %w", syscall.ESRCH), true},
		{"a doubly wrapped errno", fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", syscall.ESRCH)), true},
		{"a syscall error wrapper", os.NewSyscallError("kill", syscall.ESRCH), true},
		{"a different errno", syscall.EPERM, false},
		{"a plain error", errors.New("gone"), false},
		{"no error at all", nil, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := containsESRCH(c.err); got != c.want {
			t.Errorf("containsESRCH(%v) = %v, want %v", c.err, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// hasField reports whether err carries a field under key.
func hasField(err error, key string) bool {
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == key {
			return true
		}
	}
	return false
}
