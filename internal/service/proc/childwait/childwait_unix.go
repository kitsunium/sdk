//go:build unix

// Package childwait — the Unix half: the status wait4 reports, and the one
// wait4(-1) in the SDK.
package childwait

import "syscall"

// Status is what wait4 collected for one child: the termination status word
// and the resource usage reported with it — exactly what the owner's own wait
// would have returned.
type Status struct {
	// WaitStatus is the child's termination status (exit code or signal).
	WaitStatus syscall.WaitStatus
	// Rusage is the resource usage wait4 reported for the child.
	Rusage syscall.Rusage
}

// ReapAny performs one non-blocking wait4(-1) — the only one in the SDK — and,
// before returning, hands the status of a claimed child to its claim. It
// returns what wait4 returned: a positive pid for a collected child, 0 when
// children exist but none has exited, or the errno (ECHILD when there are no
// children at all). Callers loop on it to drain.
func ReapAny() (pid int, err error) {
	//: delegate to the process-wide ledger.
	return book.reapAny()
}

// reapAny collects one child under the sweep lock and hands its status over
// before the lock is released.
func (l *ledger) reapAny() (pid int, err error) {
	//: an owner whose wait saw ECHILD takes this lock to wait for the hand-off,
	//: so it must be held from before the collection until after it.
	l.sweeping.Lock()
	//: release once the hand-off (if any) is done.
	defer l.sweeping.Unlock()
	var status Status
	pid, err = syscall.Wait4(-1, &status.WaitStatus, syscall.WNOHANG, &status.Rusage)
	//: an errno, or nothing ready: nothing was collected, nothing to hand over.
	if err != nil || pid <= 0 {
		//: report wait4's answer verbatim for the caller to classify.
		return pid, err
	}
	//: a child was collected — give its status to whoever claimed it.
	l.deliver(pid, status)
	//: report the collected pid.
	return pid, nil
}
