//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package exec

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The wait for a process record the kernel states valid (#1453). NetBSD
// answers the record of a process inside its execve(2) as a zombie's, with
// p_uvalid 0 and no start, until the execve ends: execve holds p_reflock as a
// writer from execve_loadvm to the end of execve_runproc (kern_exec.c) and
// sysctl_doeproc reads a process whose p_reflock it cannot take as a reader as
// a zombie (kern_proc.c). OpenBSD writes p_uvalid 0 for a zombie alone.
const (
	// recordPoll is the pause, on the injected clock, between two reads of a
	// record not valid yet.
	recordPoll time.Duration = 5 * time.Millisecond
	// recordWait bounds the pauses: a record still not valid once they add up
	// to it refuses the adoption, never adopts the process unverified.
	recordWait time.Duration = time.Second
)

// procRecord is what the kernel's process record says of a process: when it
// started, as the kernel states it, its real and effective users, and whether
// the kernel stated the start at all (bsdabi's valid, p_uvalid where the
// record has one).
type procRecord struct {
	start      int64
	ruid, euid int
	valid      bool
}

// recordSource is where attachProcess reads a process's record from: the
// read itself, the probe that tells a process gone from one inside its
// execve, and the clock the pauses between two reads run on. Production uses
// kernelRecords; a test hands a scripted read.
type recordSource struct {
	// read asks the kernel for the record of a pid.
	read func(pid int) (procRecord, error)
	// alive reports nil while the kernel still finds a live process at the
	// pid, the classified refusal once it does not.
	alive func(pid int) error
	// clk is what the pauses wait on.
	clk clock.Waiter
}

// await reads the record of pid until the kernel states it valid. A record
// not valid yet is read again every recordPoll on s.clk, for recordWait at
// most; meanwhile a process the kernel no longer finds alive — a zombie, whose
// record is never valid — ends the wait with the probe's refusal, the one the
// knotes would give. Past the bound the process is ADOPTION_REFUSED: its
// users could not be read, and nothing is adopted unverified. A read that
// fails returns its error at once.
func (s recordSource) await(pid int) (procRecord, error) {
	//: one read, then one more after each pause, until recordWait of pauses.
	for waited := time.Duration(0); ; waited += recordPoll {
		rec, err := s.read(pid)
		//: a record the kernel filled, or a read that failed.
		if err != nil || rec.valid {
			//: the record, or the read's refusal.
			return rec, err
		}
		//: the kernel finds no live process: a zombie, never valid.
		if err = s.alive(pid); err != nil {
			//: SIGNAL_FAILED (ESRCH) or HANDLE_UNAVAILABLE, as the knotes say.
			return procRecord{}, err
		}
		//: the bound spent, the record still without a start.
		if waited >= recordWait {
			//: refused, never adopted unverified.
			return procRecord{}, wrapAdoptionRefused(nil, errs.Int("pid", pid), errs.Int64("bound_ms", recordWait.Milliseconds()),
				errs.String("why", "the process record stayed not valid (p_uvalid 0) past the bound"))
		}
		s.clk.Sleep(recordPoll)
	}
}
