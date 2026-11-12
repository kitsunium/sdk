//go:build linux

package consensus

import (
	"io"
	"math/bits"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"
)

// rwfDsync is RWF_DSYNC of linux/fs.h (Linux 4.7), which the syscall
// package does not name: the write returns once its range is durable, as
// if fdatasync(2) followed it.
const rwfDsync uintptr = 0x2

// The kernel's __NR_pwritev2 per architecture family (see sysPwritev2).
const (
	// nrPwritev2AMD64 is __NR_pwritev2 of arch/x86/entry/syscalls (x86-64).
	nrPwritev2AMD64 uintptr = 328
	// nrPwritev2I386 is __NR_pwritev2 of the 32-bit x86 table.
	nrPwritev2I386 uintptr = 379
	// nrPwritev2ARM is __NR_pwritev2 of arch/arm/tools/syscall.tbl.
	nrPwritev2ARM uintptr = 393
	// nrPwritev2Generic is __NR_pwritev2 of include/uapi/asm-generic/unistd.h,
	// which arm64, riscv64 and loong64 share.
	nrPwritev2Generic uintptr = 287
)

// sysPwritev2 is __NR_pwritev2 on this architecture, 0 where the SDK does
// not know it: there the log syncs with fdatasync. The numbers are the
// kernel's tables (arch/x86/entry/syscalls, include/uapi/asm-generic/
// unistd.h, arch/arm/tools/syscall.tbl); the syscall package names none.
var sysPwritev2 = pwritev2Number(runtime.GOARCH)

// pwritev2Number is pwritev2's number on arch, 0 when unknown.
func pwritev2Number(arch string) uintptr {
	switch arch {
	case "amd64":
		return nrPwritev2AMD64
	case "386":
		return nrPwritev2I386
	case "arm":
		return nrPwritev2ARM
	case "arm64", "riscv64", "loong64":
		return nrPwritev2Generic
	}
	return 0
}

// rwfDsyncProbe writes one byte flagged RWF_DSYNC to a fresh file in dir
// and removes it: the reason the kernel cannot, empty when it can.
func rwfDsyncProbe(dir string) string {
	if sysPwritev2 == 0 {
		return "pwritev2(2) is not known on " + runtime.GOARCH
	}
	path := filepath.Join(dir, rwfDsyncProbeName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "probe: " + err.Error()
	}
	// Closed first, then removed: the probe file is nothing to keep.
	defer func() {
		abandon(f.Close())
		abandon(os.Remove(path))
	}()
	ok, err := writeDsync(f, []byte{0}, 0)
	switch {
	case err != nil:
		return "probe: " + err.Error()
	case !ok:
		return "the kernel refuses pwritev2(2) with RWF_DSYNC"
	}
	return ""
}

// writeDsync writes b at off in f with pwritev2(2) flagged RWF_DSYNC,
// retried on EINTR and continued after a short write: it returns once
// every byte of b is durable. ok is false, nothing written, when the
// kernel answers ENOSYS (no pwritev2, before 4.6), EOPNOTSUPP (no
// RWF_DSYNC, before 4.7) or EINVAL to the first call.
func writeDsync(f *os.File, b []byte, off int64) (ok bool, err error) {
	fd := f.Fd()
	for done := 0; done < len(b); {
		pos := uint64(off) + uint64(done)
		iov := syscall.Iovec{Base: &b[done]}
		iov.SetLen(len(b) - done)
		n, _, errno := syscall.Syscall6(sysPwritev2, fd, uintptr(unsafe.Pointer(&iov)), 1,
			uintptr(pos), uintptr(pos>>(bits.UintSize/2)>>(bits.UintSize/2)), rwfDsync)
		runtime.KeepAlive(b)
		retry, unsupported, verr := dsyncOutcome(done == 0, n, errno)
		switch {
		case retry:
			continue
		case unsupported:
			return false, nil
		case verr != nil:
			return false, verr
		}
		done += int(n)
	}
	return true, nil
}

// dsyncOutcome judges one answer of pwritev2(2): retry on EINTR; unsupported
// when the kernel has no pwritev2 or no RWF_DSYNC, which only the first call
// of a write can say, since a later one follows a call that worked; else the
// error the call failed with, io.ErrShortWrite for a call that wrote nothing.
func dsyncOutcome(first bool, n uintptr, errno syscall.Errno) (retry, unsupported bool, err error) {
	switch {
	case errno == syscall.EINTR:
		return true, false, nil
	case first && (errno == syscall.ENOSYS || errno == syscall.EOPNOTSUPP || errno == syscall.EINVAL):
		return false, true, nil
	case errno != 0:
		return false, false, errno
	case n == 0:
		return false, false, io.ErrShortWrite
	}
	return false, false, nil
}
