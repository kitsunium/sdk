//go:build linux

package loop

import (
	"syscall"
	"unsafe"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// cpuMaskBytes is the CPU mask pinThread hands sched_setaffinity: one bit a
// CPU up to maxAffinityCPU.
const cpuMaskBytes int = (maxAffinityCPU + 1) / 8

// pin pins the loop's locked thread to its CPU, when Affinity names one
// (#857), and keeps the kernel's refusal for the report: a loop the kernel
// refuses serves unpinned. It runs on the loop's thread, after LockOSThread
// and before the loop reports ready.
func (lp *loop) pin() {
	lp.tid = syscall.Gettid()
	//: nothing asked.
	if lp.cpu < 0 {
		//: unpinned.
		return
	}
	lp.pinErr = pinThread(lp.cpu)
}

// pinThread sets the calling thread's CPU mask to cpu alone.
func pinThread(cpu int) error {
	var mask [cpuMaskBytes]byte
	mask[cpu/8] = 1 << (cpu % 8)
	_, _, errno := syscall.RawSyscall(syscall.SYS_SCHED_SETAFFINITY, 0, uintptr(cpuMaskBytes), uintptr(unsafe.Pointer(&mask)))
	//: pinned.
	if errno == 0 {
		//: no error.
		return nil
	}
	//: EINVAL for a CPU offline or outside the process's set, EPERM under a
	//: filter.
	return errno
}

// reportAffinity records the loop-affinity row once every loop reported
// ready: on when every loop's thread is pinned, missing — a fault, the group
// asked — with the first refusal, and unprobed when Affinity names nothing.
func reportAffinity(rep *capReport, loops []*loop) {
	asked := false
	//: each loop, in order.
	for _, lp := range loops {
		//: not pinned by request.
		if lp.cpu < 0 {
			continue
		}
		asked = true
		//: the first refusal decides the row.
		if lp.pinErr != nil {
			rep.set(corenet.CapabilityLoopAffinity, statusMissing, "sched_setaffinity: "+lp.pinErr.Error())
			//: missing.
			return
		}
	}
	//: no Affinity.
	if !asked {
		rep.set(corenet.CapabilityLoopAffinity, statusUnprobed, "no Affinity")
		//: unprobed.
		return
	}
	rep.set(corenet.CapabilityLoopAffinity, statusOn, "")
}
