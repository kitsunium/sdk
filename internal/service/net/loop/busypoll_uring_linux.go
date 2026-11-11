//go:build linux && (amd64 || arm64 || riscv64 || loong64)

package loop

import (
	"syscall"
	"unsafe"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// regNAPI is IORING_REGISTER_NAPI (6.9). Its unregister, 28, is never
// issued: closing the ring frees what it registered (io_napi_free).
const regNAPI uintptr = 27

// uringNAPI is struct io_uring_napi as 6.9 lays it out: 6.13 names pad[0]
// opcode and the first half of resv op_param, both zero for the dynamic
// tracking this backend asks for.
type uringNAPI struct {
	busyPollTo uint32
	prefer     uint8
	pad        [3]uint8
	resv       uint64
}

// registerNAPI sets u's busy-poll window, in microseconds, and returns the
// one the kernel held before, which it writes back.
func (u *uring) registerNAPI(usecs uint32, prefer bool) (uringNAPI, syscall.Errno) {
	arg := uringNAPI{busyPollTo: usecs}
	//: the device's interrupt held off while the loop polls.
	if prefer {
		arg.prefer = 1
	}
	errno := uringRegister(u.fd, regNAPI, unsafe.Pointer(&arg), 1)
	//: what the kernel held before, or its refusal.
	return arg, errno
}

// probeBusyPoll is busy-poll's stage: IORING_REGISTER_NAPI on a throwaway
// ring (EINVAL before 6.9), then, opted into, on ring 0, the ring the
// thread waits on and so the one whose wait polls. It runs on the loop's
// thread, after the rings are built; a switched-off row issues nothing.
func (ul *uLoop) probeBusyPoll() {
	//: the operator's switch: never probed.
	if ul.sw.disabled(corenet.CapabilityBusyPoll) {
		//: disabled.
		return
	}
	errno := ul.napiProbe()
	//: the kernel refused the stage.
	if errno != 0 {
		ul.rep.found(ul.sw, corenet.CapabilityBusyPoll, false, "register_napi: "+errno.Error())
		//: absent, or missing when asked for.
		return
	}
	//: present, not asked for.
	if !ul.sw.wants(corenet.CapabilityBusyPoll) {
		ul.rep.found(ul.sw, corenet.CapabilityBusyPoll, true, "")
		//: available.
		return
	}
	plan := ul.lp.sz.busyPoll
	callHook(ul.hooks.busyPoll, "register_napi")
	//: the serving ring refused what the probe's took.
	if _, errno := ul.rings[0].u.registerNAPI(plan.usecs, true); errno != 0 {
		ul.rep.found(ul.sw, corenet.CapabilityBusyPoll, false, "register_napi ring 0: "+errno.Error())
		//: missing.
		return
	}
	ul.rep.set(corenet.CapabilityBusyPoll, statusOn, reasonBusyPollOn)
}

// napiProbe registers a zero window on a ring of its own, closed at once: a
// kernel before 6.9 answers EINVAL. A test plays that kernel.
func (ul *uLoop) napiProbe() syscall.Errno {
	//: a test plays a kernel before 6.9.
	if ul.hooks.failStage == "busy_poll" {
		//: as it would answer.
		return syscall.EINVAL
	}
	u, _, errno := openURing(probeEntries, 0)
	//: no ring for the probe.
	if errno != 0 {
		//: the kernel's answer.
		return errno
	}
	defer u.close()
	callHook(ul.hooks.busyPoll, "register_napi")
	_, errno = u.registerNAPI(0, false)
	//: zero once accepted.
	return errno
}
