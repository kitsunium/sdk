// Package process_test — black-box tests for the public facade: the alias
// identity, the ergonomic signal constants, and the Start delegation including
// the typed-error contract that holds on every platform.
package process_test

import (
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/process"
)

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

	_, err := process.Start(t.Context(), process.Spec{})
	//: the facade must pass the central INVALID_SPEC code straight through.
	if !errs.HasCode(err, coreproc.CodeInvalidSpec) {
		t.Fatalf("Start(empty) err = %v, want CodeInvalidSpec", err)
	}
}
