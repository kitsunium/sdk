//go:build (linux && !mips && !mipsle && !mips64 && !mips64le) || freebsd

package exec

import (
	"sync/atomic"
	"syscall"
)

// closeRangeCloexec is CLOSE_RANGE_CLOEXEC, the close_range(2) flag that marks
// the range close-on-exec instead of closing it: 1<<2 in Linux's
// include/uapi/linux/close_range.h and FreeBSD's sys/unistd.h alike.
const closeRangeCloexec uintptr = 1 << 2

// closeRangeLast is the ~0U close_range(2) reads as "up to the highest
// descriptor": the call takes unsigned ints.
const closeRangeLast uintptr = uintptr(^uint32(0))

// closeRangeAbsent records that this kernel refused close_range with
// CLOSE_RANGE_CLOEXEC, so every later seal walks the descriptor directory.
var closeRangeAbsent atomic.Bool

// sealFDs marks the whole table from firstSealedFD up in one close_range(2)
// call, falling back to the walk on a kernel without it.
func sealFDs() error {
	//: one syscall where the kernel has it.
	return sealWith(markRangeCloexec, &closeRangeAbsent)
}

// markRangeCloexec issues close_range(3, ~0U, CLOSE_RANGE_CLOEXEC) and returns
// its errno, zero on success. The stdlib exports no number for it, so
// sysCloseRange is cited per kernel (ADR 0018).
func markRangeCloexec() syscall.Errno {
	_, _, errno := syscall.Syscall(sysCloseRange, uintptr(firstSealedFD), closeRangeLast, closeRangeCloexec)
	//: ENOSYS / EINVAL send the caller to the walk.
	return errno
}
