package exec

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// Open returns a handle on a process this one did not spawn, held through a
// kernel handle so that no later signal reaches another process that reuses the
// pid: a pidfd on Linux 5.4 and later, a process HANDLE on Windows. Its Signal and
// SignalHandle act through that handle; SignalGroup reaches the process alone,
// since a group addressed by number is not race-free; Stop signals, watches the
// handle for the exit within the grace, then kills. It implements ProcessHandle
// and ContextWaiter, and its Wait and WaitContext are refused with WaitFailed:
// only a parent collects an exit status. A pid that does not exist is
// SignalFailed wrapping ESRCH; a Linux kernel without pidfd is
// HandleUnavailable; every other platform is UnsupportedPlatform.
func Open(pid int) (proc coreproc.Process, err error) {
	//: no process has a pid below 1: refused before any kernel call.
	if pid < 1 {
		//: the bare sentinel: a contract violation, no cause.
		return nil, coreproc.InvalidSpec
	}
	//: the platform's handle on the pid.
	return openProcess(pid)
}
