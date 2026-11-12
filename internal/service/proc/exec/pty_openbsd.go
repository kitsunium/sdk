//go:build openbsd

package exec

import (
	"bytes"
	"os"
	"syscall"
	"unsafe"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// ptmPath is the pseudo-terminal multiplexer ptm(4) allocates a terminal from.
const ptmPath string = "/dev/ptm"

// ptmGet is PTMGET, _IOR('t', 1, struct ptmget) from sys/ttycom.h: the
// direction bit, the 40-byte argument, group 't' and command 1.
const ptmGet uint = 0x40287401

// ptmget is struct ptmget from sys/ttycom.h: the master's and the slave's
// descriptors, opened by the kernel, and their device names.
type ptmget struct {
	cfd, sfd int32
	cn, sn   [16]byte
}

// openPTY allocates a pseudo-terminal with ptm(4)'s PTMGET, which opens both
// sides in one ioctl. syscall.Syscall routes SYS_IOCTL through libc's ioctl on
// OpenBSD (Go issue 63900), the only path pinsyscalls admits since 7.5. The
// kernel opens the two descriptors without close-on-exec, so they are marked
// under the fork lock, before a concurrent spawn can inherit them; the master
// is made non-blocking so the runtime's poller owns it and Close interrupts a
// Read.
func openPTY() (master, slave *os.File, err error) {
	ptm, err := os.OpenFile(ptmPath, os.O_RDWR|syscall.O_CLOEXEC, 0)
	//: no multiplexer.
	if err != nil {
		//: the central PTY_FAILED.
		return nil, nil, wrapPTYFailed(err, errs.String("path", ptmPath))
	}
	//: the multiplexer is only needed for the one ioctl.
	defer func() { swallowErr(ptm.Close()) }()
	var pm ptmget
	syscall.ForkLock.RLock()
	gErr := ioctlFile(ptm, ptmGet, unsafe.Pointer(&pm))
	//: the kernel opened both sides: keep them from a concurrent child.
	if gErr == nil {
		syscall.CloseOnExec(int(pm.cfd))
		syscall.CloseOnExec(int(pm.sfd))
	}
	syscall.ForkLock.RUnlock()
	//: no terminal left.
	if gErr != nil {
		//: the central PTY_FAILED.
		return nil, nil, wrapPTYFailed(gErr, errs.String("ioctl", "PTMGET"))
	}
	//: the poller takes only a non-blocking descriptor.
	if nErr := syscall.SetNonblock(int(pm.cfd), true); nErr != nil {
		swallowErr(syscall.Close(int(pm.cfd)))
		swallowErr(syscall.Close(int(pm.sfd)))
		//: the central PTY_FAILED.
		return nil, nil, wrapPTYFailed(nErr, errs.String("syscall", "fcntl"))
	}
	master = os.NewFile(uintptr(pm.cfd), cString(pm.cn[:]))
	slave = os.NewFile(uintptr(pm.sfd), cString(pm.sn[:]))
	//: both sides of a fresh terminal.
	return master, slave, nil
}

// cString is the NUL-terminated name the kernel wrote into b.
func cString(b []byte) string {
	name, _, _ := bytes.Cut(b, []byte{0})
	//: the name up to its NUL, or the whole field without one.
	return string(name)
}
