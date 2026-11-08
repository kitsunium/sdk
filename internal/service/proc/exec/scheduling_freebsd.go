//go:build freebsd

package exec

import (
	"syscall"
	"unsafe"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// rtprio(2) function and priority types, from FreeBSD's sys/rtprio.h:
// RTP_SET, RTP_PRIO_REALTIME, RTP_PRIO_IDLE, and RTP_PRIO_FIFO
// (RTP_PRIO_REALTIME | PRI_FIFO_BIT).
const (
	rtpSet          uintptr = 1
	rtpPrioRealtime uint16  = 2
	rtpPrioIdle     uint16  = 4
	rtpPrioFIFO     uint16  = 10
)

// rtprioValue is struct rtprio: the type and the priority within it.
type rtprioValue struct {
	kind uint16
	prio uint16
}

// checkSchedulingPlatform refuses what rtprio(2) cannot express: SchedBatch,
// and every I/O class — FreeBSD has no per-process I/O priority.
func checkSchedulingPlatform(sched *coreproc.SchedulingValue) error {
	//: no batch policy and no I/O classes here.
	if sched.Policy == coreproc.SchedBatch || sched.IOClass != coreproc.IOClassNone {
		//: refused before the spawn.
		return coreproc.UnsupportedPlatform
	}
	//: FIFO, RR, Idle and Other are expressible.
	return nil
}

// applyScheduling maps SchedFIFO, SchedRR and SchedIdle onto rtprio(RTP_SET)
// on the live pid; SchedOther leaves the inherited class. A refusal is
// RLIMIT_FAILED.
func applyScheduling(pid int, sched *coreproc.SchedulingValue) error {
	//: nothing requested, or the normal class requested.
	if sched == nil || sched.Policy == coreproc.SchedOther {
		//: the inherited scheduling stands.
		return nil
	}
	rtp := rtprioValue{kind: rtpPrioRealtime, prio: uint16(sched.Priority)}
	//: the realtime flavour, or the idle class.
	switch sched.Policy {
	case coreproc.SchedFIFO:
		rtp.kind = rtpPrioFIFO
	case coreproc.SchedIdle:
		rtp.kind = rtpPrioIdle
	default:
		//: SchedRR is RTP_PRIO_REALTIME.
	}
	_, _, errno := syscall.Syscall(syscall.SYS_RTPRIO, rtpSet, uintptr(pid), uintptr(unsafe.Pointer(&rtp)))
	//: the kernel refused the class or its priority.
	if errno != 0 {
		//: the central RLIMIT_FAILED, naming the policy.
		return wrapRlimit(errno, errs.Int("pid", pid), errs.String("policy", sched.Policy.String()))
	}
	//: applied.
	return nil
}
