//go:build linux && (amd64 || arm64 || riscv64 || loong64)

package loop

import (
	stdnet "net"
	"runtime"
	"strconv"
	"syscall"
	"unsafe"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// The zero-copy receive ABI (include/uapi/linux/io_uring.h, Linux 6.15),
// in-tree as the rest of it (ADR 0018). Only the probe issues any of it: no
// RECV_ZC is ever submitted while uring-zcrx is class B.
const (
	// opRecvZC is IORING_OP_RECV_ZC.
	opRecvZC uint8 = 58
	// regZCRXIfq is IORING_REGISTER_ZCRX_IFQ.
	regZCRXIfq uintptr = 32
	// setupCQE32 is IORING_SETUP_CQE32 (5.19): a RECV_ZC completion carries
	// struct io_uring_zcrx_cqe in a CQE's second 16 bytes.
	setupCQE32 uint32 = 1 << 11
	// zcrxAreaShift is IORING_ZCRX_AREA_SHIFT: a completion's offset names
	// its area above it.
	zcrxAreaShift uint64 = 48
	// memRegionTypeUser is IORING_MEM_REGION_TYPE_USER: the refill ring's
	// region is memory the loop mapped.
	memRegionTypeUser uint32 = 1
	// zcrxRQEntries is the refill ring the probe asks for; with its header
	// of two cache lines it fits one page.
	zcrxRQEntries uint32 = 128
	// zcrxRingHeader is struct io_uring's size, the refill ring's head and
	// tail on cache lines of their own, which the region holds before the
	// entries (io_allocate_rbuf_ring).
	zcrxRingHeader uintptr = 128
)

// zcrxRQE is struct io_uring_zcrx_rqe, a refill ring entry: the buffer the
// loop hands back, by offset and length.
type zcrxRQE struct {
	off uint64
	len uint32
	pad uint32
}

// zcrxOffsets is struct io_uring_zcrx_offsets, where the kernel put the
// refill ring's head, tail and entries in its region.
type zcrxOffsets struct {
	head, tail, rqes, resv2 uint32
	resv                    [2]uint64
}

// zcrxAreaReg is struct io_uring_zcrx_area_reg.
type zcrxAreaReg struct {
	addr, len, rqAreaToken uint64
	flags, dmabufFD        uint32
	resv2                  [2]uint64
}

// zcrxIfqReg is struct io_uring_zcrx_ifq_reg, IORING_REGISTER_ZCRX_IFQ's
// argument.
type zcrxIfqReg struct {
	ifIdx, ifRxq, rqEntries, flags uint32
	areaPtr, regionPtr             uint64
	offsets                        zcrxOffsets
	zcrxID, resv2                  uint32
	resv                           [3]uint64
}

// regionDesc is struct io_uring_region_desc.
type regionDesc struct {
	userAddr, size uint64
	flags, id      uint32
	mmapOffset     uint64
	resv           [4]uint64
}

// probeZCRX is uring-zcrx's stage (#980), on loop 0's thread after the
// rings are built: REGISTER_PROBE asks for IORING_OP_RECV_ZC on a throwaway
// ring; with it and an interface named, the queue is registered on another,
// closed at once. The row stays class B whatever it finds, and names the
// stage that stops it.
func (ul *uLoop) probeZCRX() {
	//: the operator's switch: never probed; and one loop registers the
	//: queue, never two at once — loop 0's row is the group's.
	if ul.sw.disabled(corenet.CapabilityURingZCRX) || ul.lp.index != 0 {
		//: disabled, or another loop's to probe.
		return
	}
	//: the kernel has no zero-copy receive.
	if !ul.zcrxOp() {
		ul.rep.set(corenet.CapabilityURingZCRX, statusAbsent, reasonZCRXKernel)
		//: absent.
		return
	}
	z := ul.lp.sz.zcrx
	//: nothing named: nothing to register.
	if z.Interface == "" {
		ul.rep.set(corenet.CapabilityURingZCRX, statusAvailable, reasonZCRXNone)
		//: available.
		return
	}
	step := "register_zcrx_ifq " + z.Interface + " queue " + strconv.Itoa(z.Queue) + ": "
	errno := ul.zcrxRegisterPlayed(z)
	//: refused: the kernel's answer.
	if errno != 0 {
		ul.rep.set(corenet.CapabilityURingZCRX, statusAbsent, step+errno.Error()+"; "+reasonClassB)
		//: absent.
		return
	}
	ul.rep.set(corenet.CapabilityURingZCRX, statusAvailable, step+"accepted on a throwaway ring; "+reasonClassB)
}

// zcrxOp reports whether the kernel supports IORING_OP_RECV_ZC, asked on a
// ring of its own; a test plays a kernel from 6.15, or one before it.
func (ul *uLoop) zcrxOp() bool {
	//: a test plays a kernel.
	switch {
	case ul.hooks.failStage == "zcrx":
		//: before 6.15.
		return false
	case ul.hooks.zcrxKernel:
		//: from 6.15.
		return true
	}
	u, _, errno := openURing(probeEntries, 0)
	//: no ring to ask.
	if errno != 0 {
		//: not judged present.
		return false
	}
	defer u.close()
	ok, errno := u.supports(opRecvZC)
	//: listed and supported.
	return ok && errno == 0
}

// zcrxRegisterPlayed is zcrxRegister, or the answer a test plays.
func (ul *uLoop) zcrxRegisterPlayed(z corenet.ZCRXValue) syscall.Errno {
	//: a test plays the kernel's answer.
	if ul.hooks.zcrxKernel {
		//: played.
		return syscall.Errno(ul.hooks.zcrxErrno)
	}
	//: the kernel's.
	return zcrxRegister(z)
}

// zcrxRegister registers z's queue on a ring of its own, set up as a
// receiving ring must be — DEFER_TASKRUN, SINGLE_ISSUER on this locked
// thread, CQE32 — with an area of z.AreaBytes and a refill ring of one page,
// both mapped here, and closes it: the ring's close releases the queue,
// which the kernel restarts on registration and again on release. It returns
// the kernel's answer: ENODEV for an unknown interface, EPERM without
// CAP_NET_ADMIN, EINVAL before 6.15 or for a queue the driver cannot split,
// EOPNOTSUPP for a driver without the queue API.
func zcrxRegister(z corenet.ZCRXValue) syscall.Errno {
	ifc, err := stdnet.InterfaceByName(z.Interface)
	//: no such interface.
	if err != nil {
		//: as the kernel names it.
		return syscall.ENODEV
	}
	u, _, errno := openURing(probeEntries, setupSingleIssuer|setupDeferTaskrun|setupCQE32)
	//: no ring a receiving ring could be.
	if errno != 0 {
		//: the kernel's answer.
		return errno
	}
	defer u.close()
	area, err := mmapAnon(z.AreaBytes)
	//: no area.
	if err != nil {
		//: named.
		return syscall.ENOMEM
	}
	defer func() { swallowErr(syscall.Munmap(area)) }()
	region, err := mmapAnon(syscall.Getpagesize())
	//: no refill ring.
	if err != nil {
		//: named.
		return syscall.ENOMEM
	}
	defer func() { swallowErr(syscall.Munmap(region)) }()
	//: the refill ring's entries must fit the region the loop mapped.
	if zcrxRingHeader+uintptr(zcrxRQEntries)*unsafe.Sizeof(zcrxRQE{}) > uintptr(len(region)) {
		//: never with one page and 128 entries.
		return syscall.EINVAL
	}
	areaReg := &zcrxAreaReg{addr: uint64(uintptr(unsafe.Pointer(&area[0]))), len: uint64(len(area))}
	rd := &regionDesc{userAddr: uint64(uintptr(unsafe.Pointer(&region[0]))), size: uint64(len(region)), flags: memRegionTypeUser}
	reg := &zcrxIfqReg{
		ifIdx: uint32(ifc.Index), ifRxq: uint32(z.Queue), rqEntries: zcrxRQEntries,
		areaPtr: uint64(uintptr(unsafe.Pointer(areaReg))), regionPtr: uint64(uintptr(unsafe.Pointer(rd))),
	}
	errno = uringRegister(u.fd, regZCRXIfq, unsafe.Pointer(reg), 1)
	runtime.KeepAlive(areaReg)
	runtime.KeepAlive(rd)
	//: the kernel's answer; the close releases the queue.
	return errno
}
