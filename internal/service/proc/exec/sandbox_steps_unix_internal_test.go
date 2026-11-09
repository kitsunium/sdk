//go:build unix

package exec

import (
	"errors"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_sandboxFailure pins the parent's reading of a sandbox step's report:
// SandboxFailed naming the step and wrapping the errno; a report cut short is
// still SandboxFailed — never the success an empty read would be.
func Test_sandboxFailure(t *testing.T) {
	t.Parallel()
	for step := stepPayload; step <= stepSeccomp; step++ {
		report := sandboxReport(step, syscall.EPERM)
		err := sandboxFailure(report[1:])
		if !errors.Is(err, coreproc.SandboxFailed) || !errors.Is(err, syscall.EPERM) {
			t.Fatalf("step %d: %v, want SandboxFailed wrapping EPERM", step, err)
		}
		var e *errs.Error
		if !errors.As(err, &e) {
			t.Fatalf("step %d: %T is no *errs.Error", step, err)
		}
		if got := fieldValue(e, "step"); got != sandboxStepName(step) {
			t.Errorf("step %d: the error names %q, want %q", step, got, sandboxStepName(step))
		}
	}
	if err := sandboxFailure(nil); !errors.Is(err, coreproc.SandboxFailed) {
		t.Fatalf("a bare tag: %v, want SandboxFailed", err)
	}
	if got := sandboxStepName(200); got != "step 200" {
		t.Errorf("an unknown step is named %q", got)
	}
}

// Test_trampolined pins that a sandbox always spawns through the trampoline —
// its second stage is the trampoline — and a plain spec does not.
func Test_trampolined(t *testing.T) {
	t.Parallel()
	if !trampolined(coreproc.Spec{Path: "/bin/true", Sandbox: &coreproc.SandboxValue{}}) {
		t.Error("a sandboxed spec does not go through the trampoline")
	}
	if trampolined(coreproc.Spec{Path: "/bin/true"}) {
		t.Error("a plain spec goes through the trampoline")
	}
}
