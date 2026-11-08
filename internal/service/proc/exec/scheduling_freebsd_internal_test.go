//go:build freebsd

package exec

import (
	"syscall"
	"testing"
	"unsafe"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// rtpLookup is RTP_LOOKUP from FreeBSD's sys/rtprio.h.
const rtpLookup uintptr = 0

// Test_Start_schedulingFreeBSD pins #940 on FreeBSD: SchedIdle is applied
// through rtprio(2) and read back with RTP_LOOKUP; SchedBatch and any I/O class
// are refused with UNSUPPORTED_PLATFORM before spawning.
func Test_Start_schedulingFreeBSD(t *testing.T) {
	t.Parallel()
	for _, sched := range []coreproc.SchedulingValue{{Policy: coreproc.SchedBatch}, {IOClass: coreproc.IOClassIdle}} {
		_, err := Start(t.Context(), coreproc.Spec{Path: shellPath, Args: []string{"sh", "-c", "exit 0"}, Scheduling: &sched})
		if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
			t.Errorf("Start(%+v) = %v, want UNSUPPORTED_PLATFORM", sched, err)
		}
	}
	p, err := Start(t.Context(), coreproc.Spec{
		Path: shellPath, Args: []string{"sh", "-c", "exec sleep 30"},
		Scheduling: &coreproc.SchedulingValue{Policy: coreproc.SchedIdle, Priority: 10},
	})
	if err != nil {
		t.Fatalf("Start(SchedIdle) = %v", err)
	}
	defer func() {
		_ = p.Signal(coreproc.Signal(syscall.SIGKILL))
		_, _ = p.Wait()
	}()
	var rtp rtprioValue
	if _, _, errno := syscall.Syscall(syscall.SYS_RTPRIO, rtpLookup, uintptr(p.PID()), uintptr(unsafe.Pointer(&rtp))); errno != 0 {
		t.Fatalf("rtprio(RTP_LOOKUP) = %v", errno)
	}
	if rtp.kind != rtpPrioIdle || rtp.prio != 10 {
		t.Errorf("rtprio of the child = %+v, want idle at 10", rtp)
	}
}
