// Package process_test — black-box tests for the public facade: the alias
// identity, the ergonomic signal constants, and the Start delegation including
// the typed-error contract that holds on every platform.
package process_test

import (
	"context"
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/process"
)

// shPath is the shell used by the live-spawn facade test.
const shPath = "/bin/sh"

// requireShell skips the calling test when the host cannot run the fixture.
func requireShell(t *testing.T) {
	t.Helper()
	//: the live spawn is Unix-only behaviour.
	if runtime.GOOS == "windows" {
		//: nothing to run where Start returns UnsupportedPlatform.
		t.Skip("facade spawn test requires a Unix host")
	}
	//: a missing shell means the fixture command cannot run.
	if _, err := os.Stat(shPath); err != nil {
		//: skip rather than fail when /bin/sh is absent.
		t.Skipf("%s not present: %v", shPath, err)
	}
}

// TestSignalConstantsMatchCore asserts the facade signal constants equal the
// underlying platform numbers — the alias guarantee.
func TestSignalConstantsMatchCore(t *testing.T) {
	t.Parallel()

	type sigCase struct {
		name string
		got  process.Signal
		want syscall.Signal
	}
	//: each re-exported constant must equal its platform signal number.
	cases := []sigCase{
		{name: "SIGTERM", got: process.SIGTERM, want: syscall.SIGTERM},
		{name: "SIGKILL", got: process.SIGKILL, want: syscall.SIGKILL},
		{name: "SIGINT", got: process.SIGINT, want: syscall.SIGINT},
		{name: "SIGHUP", got: process.SIGHUP, want: syscall.SIGHUP},
		{name: "SIGQUIT", got: process.SIGQUIT, want: syscall.SIGQUIT},
	}

	runCase := func(t *testing.T, tc sigCase) {
		t.Helper()
		//: the constant's int value must match the platform signal number.
		if tc.got.Int() != int(tc.want) {
			t.Fatalf("%s = %d, want %d", tc.name, tc.got.Int(), int(tc.want))
		}
	}

	for _, tc := range cases {
		//: subtest per signal isolates a mismatch to one constant.
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestStartInvalidSpecDelegates asserts the facade forwards the typed
// InvalidSpec from the service layer unchanged on every platform.
func TestStartInvalidSpecDelegates(t *testing.T) {
	t.Parallel()

	_, err := process.Start(context.Background(), process.Spec{})
	//: the facade must pass the central INVALID_SPEC code straight through.
	if !errs.HasCode(err, coreproc.CodeInvalidSpec) {
		t.Fatalf("Start(empty) err = %v, want CodeInvalidSpec", err)
	}
}

// TestStartAndStop exercises the full facade path: spawn, group-stop, and Wait.
func TestStartAndStop(t *testing.T) {
	t.Parallel()
	requireShell(t)

	spec := process.Spec{
		Path:    shPath,
		Args:    []string{"sh", "-c", "sleep 1000 & wait"},
		Setpgid: true,
	}
	p, err := process.Start(context.Background(), spec)
	//: a clean spawn through the facade must not error.
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	//: the handle must report a positive leader pid.
	if p.PID() <= 0 {
		t.Fatalf("PID() = %d, want > 0", p.PID())
	}
	//: a graceful group stop must succeed within the grace window.
	if sErr := p.Stop(context.Background(), 2*time.Second, process.SIGTERM); sErr != nil {
		t.Fatalf("Stop: %v", sErr)
	}
	//: Wait must reap the stopped group without a host fault.
	if _, wErr := p.Wait(); wErr != nil {
		t.Fatalf("Wait: %v", wErr)
	}
}
