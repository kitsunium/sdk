//go:build unix && !linux && !freebsd

package exec

import (
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_Start_schedulingRefused pins #940's refusal rows: a policy other than
// SchedOther, or an I/O class, has no stdlib path here and is refused with
// UNSUPPORTED_PLATFORM before spawning, while the zero value and Nice still
// spawn.
func Test_Start_schedulingRefused(t *testing.T) {
	t.Parallel()
	for _, sched := range []coreproc.SchedulingValue{{Policy: coreproc.SchedIdle}, {IOClass: coreproc.IOClassIdle}} {
		_, err := Start(t.Context(), coreproc.Spec{Path: shellPath, Args: []string{"sh", "-c", "exit 0"}, Scheduling: &sched})
		if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
			t.Errorf("Start(%+v) = %v, want UNSUPPORTED_PLATFORM", sched, err)
		}
	}
	//: a child that outlives the spawn: setpriority on one already gone is refused
	//: on illumos and Solaris, and this row is about Nice reaching a live child.
	p, err := Start(t.Context(), coreproc.Spec{Path: shellPath, Args: []string{"sh", "-c", "exec sleep 30"}, Scheduling: &coreproc.SchedulingValue{}, Nice: new(1)})
	if err != nil {
		t.Fatalf("Start(zero Scheduling, Nice 1) = %v", err)
	}
	//: the test's own child, ended at once; its exit status is not this row's subject.
	if err := p.Signal(coreproc.Signal(syscall.SIGKILL)); err != nil {
		t.Fatalf("Signal = %v", err)
	}
	if _, err := p.Wait(); err != nil {
		t.Fatalf("Wait = %v", err)
	}
}
