//go:build linux

package exec

import (
	"os"
	"strconv"
	"syscall"
	"unsafe"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// ptmxPath is the multiplexer a new terminal is allocated from, and ptsDir
// the directory its slave side appears in.
const (
	ptmxPath string = "/dev/ptmx"
	ptsDir   string = "/dev/pts/"
)

// openPTY allocates a pseudo-terminal: /dev/ptmx opened without becoming the
// supervisor's controlling terminal, its slave unlocked (TIOCSPTLCK 0) and
// numbered (TIOCGPTN), then /dev/pts/<n> opened the same way. Both are
// close-on-exec; the child gets the slave through dup2.
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
	var unlock int32
	//: a new slave is locked until its master says otherwise.
	if uErr := ioctlFile(master, syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); uErr != nil {
		//: the central PTY_FAILED.
		return nil, nil, wrapPTYFailed(uErr, errs.String("ioctl", "TIOCSPTLCK"))
	}
	var n uint32
	//: the slave's number under /dev/pts.
	if nErr := ioctlFile(master, syscall.TIOCGPTN, unsafe.Pointer(&n)); nErr != nil {
		//: the central PTY_FAILED.
		return nil, nil, wrapPTYFailed(nErr, errs.String("ioctl", "TIOCGPTN"))
	}
	name := ptsDir + strconv.FormatUint(uint64(n), 10)
	slave, err = os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	//: the slave refused to open.
	if err != nil {
		//: the central PTY_FAILED.
		return nil, nil, wrapPTYFailed(err, errs.String("path", name))
	}
	//: both sides of a fresh terminal.
	return master, slave, nil
}
