//go:build unix && !linux && !freebsd

package exec

import (
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
	p, err := Start(t.Context(), coreproc.Spec{Path: shellPath, Args: []string{"sh", "-c", "exit 0"}, Scheduling: &coreproc.SchedulingValue{}, Nice: new(1)})
	if err != nil {
		t.Fatalf("Start(zero Scheduling, Nice 1) = %v", err)
	}
	if _, err := p.Wait(); err != nil {
		t.Fatalf("Wait = %v", err)
	}
}
