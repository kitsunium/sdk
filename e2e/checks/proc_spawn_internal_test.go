// Package checks — the process, rlimit and signal conformance checks.
package checks

import (
	"errors"
	"os"
	"testing"

	"github.com/kitsunium/sdk/e2e/harness"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	perrs "github.com/kitsunium/sdk/pkg/v1/errs"
)

// Test_shArgv pins the argv every spawn check hands the kernel.
//
// argv[0] is not the path — execve takes both, and a shell that receives the
// script where it expects argv[0] runs the wrong thing while still exiting 0.
// Centralising the vector is what keeps every check off a hand-written literal
// that could get that wrong once.
func Test_shArgv(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// script is the shell program the check wants run.
		script string
	}
	tests := []tc{
		{name: "a simple command", script: "exit 7"},
		{name: "a command with arguments", script: "echo hello"},
		{name: "a compound command", script: "( sleep 1 & ) ; exit 0"},
		{name: "an empty script", script: ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := shArgv(c.script)

		//: argv0, -c, script — the conventional non-login shell invocation.
		if len(got) != 3 {
			t.Fatalf("shArgv(%q) = %v, want three elements", c.script, got)
		}
		if got[0] != "sh" {
			t.Errorf("argv[0] = %q, want \"sh\"", got[0])
		}
		//: without -c the shell reads the script as a FILE NAME and exits
		//: reporting it missing, which several checks would read as a spawn
		//: failure rather than as a broken invocation.
		if got[1] != "-c" {
			t.Errorf("argv[1] = %q, want \"-c\"", got[1])
		}
		if got[2] != c.script {
			t.Errorf("argv[2] = %q, want %q", got[2], c.script)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_unsupportedProc pins the ONE sentinel that separates "this platform has
// no such mechanic" from "the SDK is broken here".
//
// Every proc check branches on it, so a matcher that answered true too widely
// would turn real regressions into expected degradations — the failure mode a
// conformance run exists to prevent, reported as a success.
func Test_unsupportedProc(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// err is what the SDK returned.
		err error
		// want is whether the platform legitimately lacks the mechanic.
		want bool
	}
	tests := []tc{
		{
			name: "the off-platform contract",
			err:  perrs.Wrap(coreproc.UnsupportedPlatform, perrs.WrapParams{}), want: true,
		},
		//: a real defect must NOT be read as an expected degradation.
		{name: "a cgroup write failure", err: perrs.Wrap(coreproc.CgroupWriteFailed, perrs.WrapParams{})},
		{name: "a spawn failure", err: perrs.Wrap(coreproc.SpawnFailed, perrs.WrapParams{})},
		{name: "an untyped error", err: errors.New("boom")},
		{name: "no error at all"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := unsupportedProc(c.err); got != c.want {
			t.Fatalf("unsupportedProc(%v) = %v, want %v — a matcher that is too wide "+
				"reports a regression as an expected degradation", c.err, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_haveShell pins that the shell probe agrees with the filesystem.
//
// A probe that answered true on a host with no /bin/sh would turn an
// environmental gap into a string of spawn FAILURES, which is the difference
// between "this image is minimal" and "the SDK is broken on this platform".
func Test_haveShell(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
	}
	tests := []tc{
		{name: "the host's shell"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, statErr := os.Stat(shellPath)
		want := statErr == nil

		if got := haveShell(); got != want {
			t.Fatalf("haveShell() = %v but stat(%s) = %v — an environmental gap "+
				"would be reported as a string of spawn failures", got, shellPath, statErr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// The spawn, rlimit and signal checks reach the kernel, so what they are pinned
// on here is the property that holds on EVERY host: the check runs to completion
// and produces a row the runner can tally. Whether that row is a Pass, an
// off-platform degradation or an environmental skip is the conformance lane's
// question — a laptop without /bin/sh must not turn any of them red.

// Test_processExitCode pins that the exit-status check produces a usable row.
// Start/Wait reporting the child's status is the most basic thing a process API
// does, and the row is where a kernel that gets it wrong shows up.
func Test_processExitCode(t *testing.T) {
	t.Parallel()
	if problem := wellFormedRow(processExitCode(), processDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_processStdioCapture pins the stdio-capture row. A capture that silently
// delivers nothing looks exactly like a child that printed nothing.
func Test_processStdioCapture(t *testing.T) {
	t.Parallel()
	if problem := wellFormedRow(processStdioCapture(), processDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_processGroupStop pins the group-aware stop row. Stopping the leader and
// leaving the group behind is the failure this check exists to catch, and it is
// invisible from the parent unless something looks.
func Test_processGroupStop(t *testing.T) {
	t.Parallel()
	if problem := wellFormedRow(processGroupStop(), processDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_captureShell pins the shared spawn helper every rlimit check is built on.
//
// It returns a terminal Result and a done flag, and the flag is what each check
// branches on: a helper that reported done with no Result — or done false with
// no output — would make the caller either return an empty row or carry on with
// an output it never got.
func Test_captureShell(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// script is the shell program the helper runs.
		script string
	}
	tests := []tc{
		{name: "a command that prints", script: "echo hello"},
		{name: "a command that prints nothing", script: "true"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, res, done := captureShell(rlimitDomain, "probe", c.script, nil, nil)

		if !done {
			//: the helper ran the shell, so the output is the caller's to assert
			//: on and the Result must be the zero value it did not fill.
			if res.Status != "" {
				t.Fatalf("captureShell reported done=false with a Result: %+v", res)
			}
			//: a shell that printed something must hand it back verbatim.
			if c.script == "echo hello" && got == "" {
				t.Fatal("captureShell reported success with no output")
			}
			return
		}
		//: a terminal Result short-circuits the caller, so it has to be a row the
		//: runner can tally rather than a zero value.
		if problem := wellFormedRow(res, rlimitDomain); problem != nil {
			t.Fatalf("the short-circuit Result: %v", problem)
		}
		//: short-circuiting with a PASS would let a check report success without
		//: ever comparing anything.
		if res.Status == harness.Pass {
			t.Fatalf("captureShell short-circuited with a PASS: %+v", res)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_rlimitNoFile pins the open-file ceiling row — proof the re-exec
// trampoline ran setrlimit in the fresh child, which cannot be observed from the
// parent at all.
func Test_rlimitNoFile(t *testing.T) {
	t.Parallel()
	if problem := wellFormedRow(rlimitNoFile(), rlimitDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_rlimitUmask pins the umask row on the same terms.
func Test_rlimitUmask(t *testing.T) {
	t.Parallel()
	if problem := wellFormedRow(rlimitUmask(), rlimitDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_rlimitCore pins the core-dump ceiling row. A core limit that does not
// take is how a crash writes a full memory image — secrets included — to disk.
func Test_rlimitCore(t *testing.T) {
	t.Parallel()
	if problem := wellFormedRow(rlimitCore(), rlimitDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_signalParse pins the name/number round trip. It is pure translation, so
// it must PASS everywhere: a signal whose name and number disagree makes every
// log line about it wrong.
func Test_signalParse(t *testing.T) {
	t.Parallel()
	if problem := passingRow(signalParse(), signalDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_signalRelay pins Relay's termination contract row. The check drives it
// with a pre-closed source so no signal is ever delivered to this process, which
// is what makes it safe to run here at all.
func Test_signalRelay(t *testing.T) {
	t.Parallel()
	if problem := wellFormedRow(signalRelay(), signalDomain); problem != nil {
		t.Fatal(problem)
	}
}
