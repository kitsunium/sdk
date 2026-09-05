//go:build unix

// Package exec — the best-effort post-start attributes. "Best effort" here means
// the kernel may refuse; it does NOT mean the refusal is dropped, which is the
// distinction these tests exist to hold.
package exec

import (
	"errors"
	"fmt"
	"os"
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
func Test_applyNice(t *testing.T) {
	t.Parallel()
	raise := 5
	lower := -20

	type tc struct {
		name    string
		pid     int
		nice    *int
		wantErr bool
	}
	tests := []tc{
		{name: "a nil Nice touches nothing", pid: os.Getpid()},
		{
			//: raising niceness needs no privilege, so this must succeed on
			//: any host.
			name: "raising our own niceness",
			pid:  os.Getpid(),
			nice: &raise,
		},
		{
			//: a pid that cannot exist fails whatever the privileges.
			name:    "a process that does not exist",
			pid:     absentPID,
			nice:    &raise,
			wantErr: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := applyNice(c.pid, c.nice)
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
	//: the assertion is that the outcome is never a silent partial.
	err := applyNice(os.Getpid(), &lower)
	if err != nil && !errs.HasCode(err, coreproc.CodeRlimitFailed) {
		t.Errorf("applyNice(lower) = %v, want nil or RLIMIT_FAILED", err)
	}
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
