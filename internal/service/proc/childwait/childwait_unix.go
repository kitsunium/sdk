//go:build unix

package childwait

import (
	"syscall"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// signalledCode is ExitValue.Code for a child a signal ended: it has no exit
// status of its own.
const signalledCode int = -1

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
	//: delegate to the process-wide ledger, with nobody observing.
	return book.reapAny()
}

// ReapAnyObserved is ReapAny with an observer: observe, when not nil, hears of
// every child the sweep collects — its pid, its status and whether a claim
// owned it — after the hand-off and under the sweep lock, before ReapAny would
// return. It is the one moment an orphan's status exists: the observer is told
// and nothing is stored (ADR 0093). It must not block, and must not keep
// status, which the next sweep reuses.
func ReapAnyObserved(observe func(pid int, status *StatusValue, owned bool)) (pid int, err error) {
	//: delegate to the process-wide ledger.
	return book.reapAny(observe)
}

// reapAny collects one child under the sweep lock and hands its status over
// before the lock is released; each observer that is not nil hears of it
// after the hand-off and before the lock is released. None is the plain
// sweep, which ReapAny and the spawn and sweep benchmarks make.
func (l *ledger) reapAny(observers ...func(pid int, status *StatusValue, owned bool)) (pid int, err error) {
	//: an owner whose wait saw ECHILD takes this lock to wait for the hand-off,
	//: so it must be held from before the collection until after it.
	l.sweeping.Lock()
	//: release once the hand-off (if any) is done.
	defer l.sweeping.Unlock()
	//: the sweep's own buffer, guarded by the sweep lock held above: a status
	//: an observer may be shown never escapes to the heap.
	status := &l.collected
	*status = StatusValue{}
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
	owned := l.deliver(pid, status)
	//: the one moment an orphan's status exists: told, never stored (ADR 0093).
	for _, observe := range observers {
		//: a nil observer hears nothing.
		if observe != nil {
			observe(pid, status, owned)
		}
	}
	//: report the collected pid.
	return pid, nil
}

// Exit reads the status in the words a Process's Wait reports: the exit code,
// or -1, the terminating signal and whether it dumped core, and the CPU times,
// peak RSS and counters of the rusage — so a child nobody claimed is described
// as an owned one is.
func (s *StatusValue) Exit() coreproc.ExitValue {
	out := coreproc.ExitValue{Code: signalledCode}
	//: a normal exit reports a 0–255 status code.
	if s.WaitStatus.Exited() {
		out.Code = s.WaitStatus.ExitStatus()
	}
	//: a signalled death records the terminating signal, code stays -1.
	if s.WaitStatus.Signaled() {
		out.Signaled = true
		out.Signal = coreproc.Signal(s.WaitStatus.Signal())
		//: whether that death left a core image — a crash to collect, not a kill.
		out.CoreDumped = s.WaitStatus.CoreDump()
	}
	out.UserTime = time.Duration(s.Rusage.Utime.Nano())
	out.SystemTime = time.Duration(s.Rusage.Stime.Nano())
	out.MaxRSS = MaxRSSKB(&s.Rusage)
	out.Usage = UsageOf(&s.Rusage)
	//: the outcome, as exec's own wait would read it.
	return out
}
