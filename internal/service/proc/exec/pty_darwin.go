//go:build darwin

package exec

import (
	"bytes"
	"os"
	"syscall"
	"unsafe"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// ptmxPath is the multiplexer a new terminal is allocated from.
const ptmxPath string = "/dev/ptmx"

// ptyNameLen is the buffer TIOCPTYGNAME fills (128, sys/ttycom.h).
const ptyNameLen int = 128

// openPTY allocates a pseudo-terminal as posix_openpt, grantpt, unlockpt and
// ptsname do in libc: /dev/ptmx, TIOCPTYGRANT, TIOCPTYUNLK and TIOCPTYGNAME,
// then the slave opened without becoming the supervisor's controlling
// terminal.
func openPTY() (master, slave *os.File, err error) {
	master, err = os.OpenFile(ptmxPath, os.O_RDWR|syscall.O_NOCTTY, 0)
	//: no multiplexer, or no terminal left.
	if err != nil {
		//: the central PTY_FAILED.
		return nil, nil, wrapPTYFailed(err, errs.String("path", ptmxPath))
	}
	//: a failure past this point releases the master.
	defer func() {
		//: the allocation failed past the master: release it.
		if err != nil {
			swallowErr(master.Close())
		}
	}()
	//: grant, then unlock, the slave.
	for _, req := range []uint{syscall.TIOCPTYGRANT, syscall.TIOCPTYUNLK} {
		//: each ioctl takes no argument.
		if rErr := ioctlFile(master, req, nil); rErr != nil {
			//: the central PTY_FAILED.
			return nil, nil, wrapPTYFailed(rErr, errs.Int("ioctl", int(req)))
		}
	}
	buf := make([]byte, ptyNameLen)
	//: the slave's path.
	if gErr := ioctlFile(master, syscall.TIOCPTYGNAME, unsafe.Pointer(&buf[0])); gErr != nil {
		//: the central PTY_FAILED.
		return nil, nil, wrapPTYFailed(gErr, errs.String("ioctl", "TIOCPTYGNAME"))
	}
	name := string(buf[:bytes.IndexByte(buf, 0)])
	slave, err = os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	//: the slave refused to open.
	if err != nil {
		//: the central PTY_FAILED.
		return nil, nil, wrapPTYFailed(err, errs.String("path", name))
	}
	//: both sides of a fresh terminal.
	return master, slave, nil
}
