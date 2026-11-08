//go:build linux || darwin || freebsd

package exec

import (
	"cmp"
	"os"
	"syscall"
	"unsafe"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// winsize is struct winsize from sys/ttycom.h and asm-generic/termios.h.
type winsize struct {
	rows, cols, xpixel, ypixel uint16
}

// buildPTYStdio allocates a pseudo-terminal (openPTY, per kernel), sizes it
// when size is set, and wires its slave side as the child's fd 0, 1 and 2.
// The parent closes the slave after the spawn; the master stays, for TTY.
func buildPTYStdio(s *stdioState, size *coreproc.PTYValue) error {
	master, slave, err := openPTY()
	//: no terminal to give the child.
	if err != nil {
		//: the typed PTY_FAILED from the allocation.
		return err
	}
	s.opened = append(s.opened, master, slave)
	s.ptyMaster = master
	//: the caller's initial size, before the child's first instruction.
	if size != nil {
		//: a refused size fails the spawn rather than start it at 0x0.
		if wErr := setWinsize(master, *size); wErr != nil {
			//: the typed PTY_FAILED.
			return wErr
		}
	}
	//: one terminal for all three streams.
	s.files = [stdioFdCount]*os.File{slave, slave, slave}
	//: the parent's copy of the slave goes once the child holds its own.
	s.closeChild = append(s.closeChild, slave)
	//: a terminal-wired state.
	return nil
}

// checkPTY accepts StdioPTY: this kernel has pseudo-terminals.
func checkPTY(_ coreproc.Spec) error {
	//: nothing to refuse.
	return nil
}

// ioctlFile issues ioctl(req, arg) on f's descriptor without taking it out of
// the runtime's poller (File.Fd would set it blocking, and a Read on the
// master could then no longer be interrupted by Close).
func ioctlFile(f *os.File, req uint, arg unsafe.Pointer) error {
	rc, err := f.SyscallConn()
	//: a closed file has no descriptor to reach.
	if err != nil {
		//: the closed-file error.
		return err
	}
	var opErr error
	cErr := rc.Control(func(fd uintptr) {
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(req), uintptr(arg))
		//: a non-zero errno is the kernel's refusal.
		if errno != 0 {
			opErr = errno
		}
	})
	//: the descriptor unreachable, or the kernel's refusal, or nil.
	return cmp.Or(cErr, opErr)
}

// setWinsize sets the terminal's window size (TIOCSWINSZ).
func setWinsize(master *os.File, size coreproc.PTYValue) error {
	ws := new(winsize{rows: size.Rows, cols: size.Cols})
	//: the kernel stores it and signals the foreground group SIGWINCH.
	if err := ioctlFile(master, syscall.TIOCSWINSZ, unsafe.Pointer(ws)); err != nil {
		//: the central PTY_FAILED, naming the size.
		return wrapPTYFailed(err, errs.Int("rows", int(size.Rows)), errs.Int("cols", int(size.Cols)))
	}
	//: the new size is in force.
	return nil
}

// TTY returns the master side of the child's pseudo-terminal, which the caller
// owns and closes, or nil for a child started without StdioPTY.
func (h *handle) TTY() *os.File {
	//: the master buildPTYStdio kept.
	return h.stdio.ptyMaster
}

// Resize sets the window size of the child's terminal; the kernel sends it
// SIGWINCH. A child without a terminal is PTY_FAILED.
func (h *handle) Resize(rows, cols uint16) error {
	//: no terminal to resize.
	if h.stdio.ptyMaster == nil {
		//: the central PTY_FAILED.
		return wrapPTYFailed(nil, errs.Int("pid", h.pid), errs.String("why", "not started with StdioPTY"))
	}
	//: TIOCSWINSZ on the master.
	return setWinsize(h.stdio.ptyMaster, coreproc.PTYValue{Rows: rows, Cols: cols})
}

// Size reads the window size of the child's terminal (TIOCGWINSZ).
func (h *handle) Size() (coreproc.PTYValue, error) {
	//: no terminal to read.
	if h.stdio.ptyMaster == nil {
		//: the central PTY_FAILED.
		return coreproc.PTYValue{}, wrapPTYFailed(nil, errs.Int("pid", h.pid), errs.String("why", "not started with StdioPTY"))
	}
	var ws winsize
	//: the size the kernel holds now.
	if err := ioctlFile(h.stdio.ptyMaster, syscall.TIOCGWINSZ, unsafe.Pointer(&ws)); err != nil {
		//: the central PTY_FAILED.
		return coreproc.PTYValue{}, wrapPTYFailed(err, errs.Int("pid", h.pid))
	}
	//: rows and columns; the pixel fields are not the port's.
	return coreproc.PTYValue{Rows: ws.rows, Cols: ws.cols}, nil
}
