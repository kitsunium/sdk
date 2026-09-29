//go:build unix

// Package childwait — the Unix half: the status wait4 reports, and the one
// wait4 for any child in the SDK (wait4(-1); wait4(0) on illumos and Solaris,
// see waitany_solaris.go).
package childwait

import "syscall"

// StatusValue is what wait4 collected for one child: the termination status
// word and the resource usage reported with it — exactly what the owner's own
// wait would have returned.
type StatusValue struct {
	// WaitStatus is the child's termination status (exit code or signal).
	WaitStatus syscall.WaitStatus
	// Rusage is the resource usage wait4 reported for the child.
	Rusage syscall.Rusage
}

// ReapAny performs one non-blocking wait4 for any child — the only one in the
// SDK, and wait4(-1) everywhere but illumos and Solaris, where libc spells it
// wait4(0) — and, before returning, hands the status of a claimed child to its
// claim. It returns what wait4 returned: a positive pid for a collected child,
// 0 when children exist but none has exited, or -1 and the errno (ECHILD when
// there are no children at all). Callers loop on it to drain.
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
	var status StatusValue
	pid, err = syscall.Wait4(anyChildPID, &status.WaitStatus, syscall.WNOHANG, &status.Rusage)
	//: a failed call: nothing collected. On illumos and Solaris the stdlib hands
	//: libc's 32-bit -1 back without sign extension (4294967295); int32 reads it
	//: as -1 there and changes nothing anywhere else.
	if int32(pid) == -1 {
		//: the errno for the caller to classify, and POSIX's -1 beside it.
		return -1, err
	}
	//: children exist and none has exited — nothing to hand over. POSIX gives
	//: errno a meaning only after a -1, and the illumos/Solaris wrapper reports
	//: it whatever the call returned, so a call that succeeded carries none.
	if pid == 0 {
		//: nothing ready; the sweep is drained.
		return 0, nil
	}
	//: a child was collected — give its status to whoever claimed it.
	l.deliver(pid, &status)
	//: report the collected pid.
	return pid, nil
}
