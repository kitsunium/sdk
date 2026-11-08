package signal

import (
	"encoding/binary"
	"syscall"
	"unsafe"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// The Linux siginfo_t rt_sigqueueinfo(2) reads (include/uapi/asm-generic/
// siginfo.h), laid out by hand because the standard library declares none.
const (
	// siginfoSize is SI_MAX_SIZE: the kernel copies 128 bytes whatever the arch.
	siginfoSize int = 128
	// siSignoOffset is si_signo, first on every arch.
	siSignoOffset int = 0
	// siQueue is SI_QUEUE (-1, as the kernel's 32-bit int writes it), the
	// si_code sigqueue(3) sends: a value from a process, which the kernel
	// accepts from a sender that is not the target.
	siQueue uint32 = 1<<32 - 1
	// pointerSize is the width of the pointer _sifields holds, its alignment.
	pointerSize int = int(unsafe.Sizeof(uintptr(0)))
	// wideWord is the pointer width at which si_value holds 64 bits.
	wideWord int = 8
	// siRTPidOffset is _sifields._rt._pid: after three ints, aligned to the
	// pointer the union holds — 16 on a 64-bit arch, 12 on a 32-bit one.
	siRTPidOffset int = (3*4 + pointerSize - 1) / pointerSize * pointerSize
	// siRTUidOffset is _rt._uid.
	siRTUidOffset int = siRTPidOffset + 4
	// siRTValueOffset is _rt._sigval, a pointer-sized union.
	siRTValueOffset int = siRTPidOffset + 8
)

// sendValue queues sig with value through rt_sigqueueinfo(2).
func sendValue(target Target, sig coreproc.Signal, value int) error {
	var info [siginfoSize]byte
	order := binary.NativeEndian
	order.PutUint32(info[siSignoOffset:], uint32(int32(sig.Int())))
	order.PutUint32(info[siCodeOffset:], siQueue)
	order.PutUint32(info[siRTPidOffset:], uint32(int32(syscall.Getpid())))
	order.PutUint32(info[siRTUidOffset:], uint32(syscall.Getuid()))
	//: the union is as wide as a pointer: the whole int on 64 bits.
	if pointerSize == wideWord {
		order.PutUint64(info[siRTValueOffset:], uint64(int64(value)))
	} else {
		order.PutUint32(info[siRTValueOffset:], uint32(int32(value)))
	}
	//: rt_sigqueueinfo(tgid, sig, &info).
	_, _, errno := syscall.Syscall(syscall.SYS_RT_SIGQUEUEINFO,
		uintptr(target), uintptr(sig.Int()), uintptr(unsafe.Pointer(&info[0])))
	//: ESRCH for a reaped pid, EPERM for another account's, EINVAL for a bad signal.
	if errno != 0 {
		return sendFailed(errno, target, sig)
	}
	//: queued.
	return nil
}
