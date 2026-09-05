// Package exec — the central wrap helpers. Every one of them restates a
// coreproc sentinel's fields so a syscall cause inherits them, and every one is
// tested the same way: the sentinel's code must survive, the caller's fields
// must survive, and the exit status must be the one a supervisor reads.
package exec

import (
	"errors"
	"os"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// wrapCase is one scenario shared by every wrapper test.
type wrapCase struct {
	name  string
	cause error
	//: set when the cause is itself an *errs.Error. These wrappers wrap the
	//: CAUSE, so origin-wins leaves the deepest error's reason and exit status
	//: in place — the wrap's code is still reachable through the chain, but the
	//: rendered reason is the cause's. Every cause reaching these helpers in
	//: production is a syscall or stdlib error, so the case is latent; it is
	//: pinned here so a future typed cause is a deliberate choice rather than a
	//: surprise.
	typedCause bool
	fields     []errs.FieldValue
}

// wrapCases are the causes every wrapper has to survive: a plain error, a typed
// one (the case origin-wins exists for), and a nil one.
func wrapCases() []wrapCase {
	//: a typed cause carrying its OWN dotted-quad code is what would hijack the
	//: sentinel if the wrap direction were reversed.
	foreign := errs.Define(errs.Code(0x00_02_1E_01), "FOREIGN_REASON",
		"a foreign public message", "a foreign private message")
	return []wrapCase{
		{name: "a plain cause", cause: os.ErrPermission},
		{name: "a typed cause", cause: foreign, typedCause: true},
		{name: "a nil cause"},
		{
			name:   "a cause with fields",
			cause:  os.ErrNotExist,
			fields: []errs.FieldValue{errs.String("path", "/usr/bin/missing"), errs.Int("pid", 42)},
		},
	}
}

// assertWrapped checks the three properties every wrapper promises.
func assertWrapped(t *testing.T, got error, c wrapCase, code errs.Code, reason string, exit int) {
	t.Helper()
	if got == nil {
		t.Fatal("the wrapper returned nil")
	}
	//: the sentinel is the origin, so its code survives even a typed cause.
	if !errs.HasCode(got, code) {
		t.Errorf("wrapped = %v, want code %v", got, code)
	}
	//: a typed cause keeps its own reason and exit status (origin-wins); the
	//: wrapper's own values are only observable over a plain cause.
	if !c.typedCause {
		if r, _ := errs.ReasonOf(got); r != reason {
			t.Errorf("reason = %q, want %q", r, reason)
		}
		//: the exit status is what a supervisor reads to classify the failure.
		if e := errs.ExitCodeOf(got); e != exit {
			t.Errorf("exit code = %d, want %d", e, exit)
		}
	}
	//: the cause stays reachable, or an operator cannot see WHY it failed.
	if c.cause != nil && !errors.Is(got, c.cause) {
		t.Errorf("wrapped = %v, want it to wrap %v", got, c.cause)
	}
	//: the caller's annotations are never dropped to make room for the wrap.
	if len(errs.FieldsOf(got)) < len(c.fields) {
		t.Errorf("fields = %v, want at least the %d supplied", errs.FieldsOf(got), len(c.fields))
	}
}

// Test_wrapSpawn pins the wrapper for a fork/exec cause.
//
// A spawn failure is the one a supervisor most needs to classify: the binary is
// missing, the interpreter is not there, or the kernel refused the fork. Losing
// the cause would leave all three reading identically.
func Test_wrapSpawn(t *testing.T) {
	t.Parallel()
	tests := wrapCases()
	runCase := func(t *testing.T, c wrapCase) {
		t.Helper()
		assertWrapped(t, wrapSpawn(c.cause, c.fields...), c, coreproc.CodeSpawnFailed, "SPAWN_FAILED", exitOSErr)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_wrapWait pins the wrapper for a wait4 host fault.
//
// wait4 failing is a host fault, not a child fault — the child may well have
// exited cleanly — so it must never be confused with a non-zero exit status.
func Test_wrapWait(t *testing.T) {
	t.Parallel()
	tests := wrapCases()
	runCase := func(t *testing.T, c wrapCase) {
		t.Helper()
		assertWrapped(t, wrapWait(c.cause, c.fields...), c, coreproc.CodeWaitFailed, "WAIT_FAILED", exitOSErr)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_wrapSignal pins the wrapper for a kill(2) cause.
//
// A failed signal usually means the process already went away, which a caller
// often treats as success; keeping the cause is what lets them tell that apart
// from a permission denial.
func Test_wrapSignal(t *testing.T) {
	t.Parallel()
	tests := wrapCases()
	runCase := func(t *testing.T, c wrapCase) {
		t.Helper()
		assertWrapped(t, wrapSignal(c.cause, c.fields...), c, coreproc.CodeSignalFailed, "SIGNAL_FAILED", exitOSErr)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_wrapStop pins the wrapper for a process group that survived escalation.
//
// Reaching this error means SIGKILL was sent and something is STILL running,
// which is a decision point rather than a fault: the caller has to choose
// between waiting longer and giving up, and it needs the typed code to tell
// this apart from a signal that simply could not be delivered.
func Test_wrapStop(t *testing.T) {
	t.Parallel()
	tests := wrapCases()
	runCase := func(t *testing.T, c wrapCase) {
		t.Helper()
		assertWrapped(t, wrapStop(c.cause, c.fields...), c, coreproc.CodeStopFailed, "STOP_FAILED", exitOSErr)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_wrapRlimit pins the wrapper for a resource-limit failure.
//
// The child is already forked when a limit is applied, so this failure means a
// process is running WITHOUT the ceiling that was asked for — which is exactly
// the state a caller must not mistake for success.
func Test_wrapRlimit(t *testing.T) {
	t.Parallel()
	tests := wrapCases()
	runCase := func(t *testing.T, c wrapCase) {
		t.Helper()
		assertWrapped(t, wrapRlimit(c.cause, c.fields...), c, coreproc.CodeRlimitFailed, "RLIMIT_FAILED", exitOSErr)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_wrapCgroupUnavailable pins the wrapper for a missing or undelegated cgroup path.
//
// This is a deployment fact rather than a bug, so it carries EX_UNAVAILABLE: a
// supervisor reading the exit status can tell 'this host cannot do that' from
// 'this program is broken'.
func Test_wrapCgroupUnavailable(t *testing.T) {
	t.Parallel()
	tests := wrapCases()
	runCase := func(t *testing.T, c wrapCase) {
		t.Helper()
		assertWrapped(t, wrapCgroupUnavailable(c.cause, c.fields...), c, coreproc.CodeCgroupUnavailable, "CGROUP_UNAVAILABLE", exitUnavailable)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_wrapStdioCapture pins the wrapper for a capture-writer failure.
//
// The child ran fine; it was the caller's own sink that failed. EX_IOERR says so,
// and conflating it with a spawn failure would send the operator to the wrong
// half of the system.
func Test_wrapStdioCapture(t *testing.T) {
	t.Parallel()
	tests := wrapCases()
	runCase := func(t *testing.T, c wrapCase) {
		t.Helper()
		assertWrapped(t, wrapStdioCapture(c.cause, c.fields...), c, coreproc.CodeStdioCaptureFailed, "STDIO_CAPTURE_FAILED", exitIOErr)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_wrapUnknownUser pins the wrapper for an unresolvable user.
//
// EX_NOUSER is what tells an init system the unit file names an account that does
// not exist on this host — a configuration error it can report without guessing.
func Test_wrapUnknownUser(t *testing.T) {
	t.Parallel()
	tests := wrapCases()
	runCase := func(t *testing.T, c wrapCase) {
		t.Helper()
		assertWrapped(t, wrapUnknownUser(c.cause, c.fields...), c, coreproc.CodeUnknownUser, "UNKNOWN_USER", exitNoUser)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_wrapUnknownGroup pins the wrapper for an unresolvable group.
//
// Same as the user case, and kept separate on purpose: a unit that names a valid
// user and a missing group should not be reported as a missing user.
func Test_wrapUnknownGroup(t *testing.T) {
	t.Parallel()
	tests := wrapCases()
	runCase := func(t *testing.T, c wrapCase) {
		t.Helper()
		assertWrapped(t, wrapUnknownGroup(c.cause, c.fields...), c, coreproc.CodeUnknownGroup, "UNKNOWN_GROUP", exitNoUser)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
