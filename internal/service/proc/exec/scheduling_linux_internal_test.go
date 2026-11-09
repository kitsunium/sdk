//go:build linux

package exec

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// ioprioWhoProcess is IOPRIO_WHO_PROCESS (include/uapi/linux/ioprio.h).
const ioprioWhoProcess uintptr = 1

// statPolicy reads the scheduling policy of pid from /proc/<pid>/stat, field
// 41 — counted after the parenthesised command name, which may hold spaces.
func statPolicy(t *testing.T, pid int) int {
	t.Helper()
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		t.Fatalf("reading /proc/%d/stat = %v", pid, err)
	}
	rest := string(raw[strings.LastIndexByte(string(raw), ')')+2:])
	//: rest starts at field 3 (state); field 41 is index 38.
	fields := strings.Fields(rest)
	policy, err := strconv.Atoi(fields[41-3])
	if err != nil {
		t.Fatalf("field 41 of /proc/%d/stat = %q", pid, fields[41-3])
	}
	return policy
}

// ioprioOf reads the I/O priority of pid with ioprio_get.
func ioprioOf(t *testing.T, pid int) int {
	t.Helper()
	r, _, errno := syscall.Syscall(syscall.SYS_IOPRIO_GET, ioprioWhoProcess, uintptr(pid), 0)
	if errno != 0 {
		t.Fatalf("ioprio_get(%d) = %v", pid, errno)
	}
	return int(r)
}

// Test_Start_scheduling pins #940 on Linux: a child spawned with a policy shows
// it in /proc/<pid>/stat field 41 while it runs, and one spawned with an I/O
// class shows it in ioprio_get; a realtime policy an unprivileged supervisor
// may not grant is RLIMIT_FAILED and the child is torn down; an unknown policy
// is INVALID_SPEC before anything is spawned.
func Test_Start_scheduling(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		sched      coreproc.SchedulingValue
		wantPolicy int
		wantIOPrio int
		wantCode   errs.Code
		root       bool
	}
	tests := []tc{
		{name: "SCHED_IDLE", sched: coreproc.SchedulingValue{Policy: coreproc.SchedIdle}, wantPolicy: 5},
		{name: "SCHED_BATCH", sched: coreproc.SchedulingValue{Policy: coreproc.SchedBatch}, wantPolicy: 3},
		{name: "idle I/O class", sched: coreproc.SchedulingValue{IOClass: coreproc.IOClassIdle}, wantIOPrio: 3 << 13},
		{name: "best-effort level 6", sched: coreproc.SchedulingValue{IOClass: coreproc.IOClassBestEffort, IOPriority: 6}, wantIOPrio: 2<<13 | 6},
		{name: "SCHED_FIFO unprivileged", sched: coreproc.SchedulingValue{Policy: coreproc.SchedFIFO, Priority: 10}, wantCode: coreproc.CodeRlimitFailed, root: true},
		{name: "an unknown policy", sched: coreproc.SchedulingValue{Policy: coreproc.SchedPolicy(4)}, wantCode: coreproc.CodeInvalidSpec},
		{name: "an I/O level past 7", sched: coreproc.SchedulingValue{IOClass: coreproc.IOClassBestEffort, IOPriority: 8}, wantCode: coreproc.CodeInvalidSpec},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: a supervisor with CAP_SYS_NICE is granted what the row expects refused.
		if c.root && os.Geteuid() == 0 {
			t.Skip("root may set a realtime policy")
		}
		sched := c.sched
		p, err := Start(t.Context(), coreproc.Spec{Path: shellPath, Args: []string{"sh", "-c", "exec sleep 30"}, Scheduling: &sched})
		if c.wantCode != 0 {
			if !errs.HasCode(err, c.wantCode) {
				t.Fatalf("Start = %v, want code %v", err, c.wantCode)
			}
			return
		}
		if err != nil {
			t.Fatalf("Start = %v", err)
		}
		defer func() {
			_ = p.Signal(coreproc.Signal(syscall.SIGKILL))
			_, _ = p.Wait()
		}()
		if got := statPolicy(t, p.PID()); got != c.wantPolicy {
			t.Errorf("policy of the child = %d, want %d", got, c.wantPolicy)
		}
		if c.wantIOPrio != 0 {
			if got := ioprioOf(t, p.PID()); got != c.wantIOPrio {
				t.Errorf("ioprio of the child = %#x, want %#x", got, c.wantIOPrio)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
