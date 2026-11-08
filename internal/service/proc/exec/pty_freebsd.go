//go:build freebsd

package exec

import (
	"os"
	"strconv"
	"syscall"
	"unsafe"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// ptsDir is the directory a terminal's slave side appears in.
const ptsDir string = "/dev/pts/"

// openPTY allocates a pseudo-terminal with posix_openpt(2) — a syscall on
// FreeBSD, where grantpt and unlockpt have nothing to do — and opens the slave
// TIOCGPTN numbers. The master is made non-blocking before it is wrapped, so
// the runtime's poller owns it and Close interrupts a Read.
func openPTY() (master, slave *os.File, err error) {
	fd, _, errno := syscall.Syscall(syscall.SYS_POSIX_OPENPT, uintptr(syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC), 0, 0)
	//: no terminal left.
	if errno != 0 {
		//: the central PTY_FAILED.
		return nil, nil, wrapPTYFailed(errno, errs.String("syscall", "posix_openpt"))
	}
	//: the poller takes only a non-blocking descriptor.
	if nErr := syscall.SetNonblock(int(fd), true); nErr != nil {
		swallowErr(syscall.Close(int(fd)))
		//: the central PTY_FAILED.
		return nil, nil, wrapPTYFailed(nErr, errs.String("syscall", "fcntl"))
	}
	master = os.NewFile(fd, "/dev/ptmx")
	//: a failure past this point releases the master.
	defer func() {
		//: the allocation failed past the master: release it.
		if err != nil {
			swallowErr(master.Close())
		}
	}()
	var n int32
	//: the slave's number under /dev/pts.
	if gErr := ioctlFile(master, syscall.TIOCGPTN, unsafe.Pointer(&n)); gErr != nil {
		//: the central PTY_FAILED.
		return nil, nil, wrapPTYFailed(gErr, errs.String("ioctl", "TIOCGPTN"))
	}
	name := ptsDir + strconv.Itoa(int(n))
	slave, err = os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	//: the slave refused to open.
	if err != nil {
		//: the central PTY_FAILED.
		return nil, nil, wrapPTYFailed(err, errs.String("path", name))
	}
	//: both sides of a fresh terminal.
	return master, slave, nil
}
