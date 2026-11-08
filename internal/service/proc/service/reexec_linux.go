//go:build linux && (amd64 || arm64)

package service

import (
	"context"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// handoverEnv names the payload's descriptor in the incoming image's
	// environment, under ADR 0219's reserved prefix.
	handoverEnv string = "__KITSUNIUM_SDK_PROC_HANDOVER"
	// oPath is O_PATH: a descriptor naming a file, for execveat and fstat
	// alone (include/uapi/asm-generic/fcntl.h, amd64 and arm64 alike).
	oPath int = 0x200000
	// mfdCloexec and mfdAllowSealing are memfd_create's flags.
	mfdCloexec, mfdAllowSealing uintptr = 0x1, 0x2
	// fAddSeals and fGetSeals are fcntl's seal commands (F_LINUX_SPECIFIC_BASE
	// + 9 and + 10).
	fAddSeals, fGetSeals uintptr = 1033, 1034
	// sealsWhole are F_SEAL_SEAL, F_SEAL_SHRINK, F_SEAL_GROW and
	// F_SEAL_WRITE: the payload can change no more.
	sealsWhole uintptr = 0x1 | 0x2 | 0x4 | 0x8
	// fdDir lists this process's descriptors.
	fdDir string = "/proc/self/fd"
)

// The re-exec's test seams (export_internal_test): the version reexec
// writes, and a look at the payload once it is written and before it reaches
// the image — what a racing writer or a bad image would leave.
var (
	versionSeam   func() uint16        = func() uint16 { return handoverVersion }
	tamperPayload func(payload []byte) = func([]byte) {}
)

// openImage opens path O_PATH and close-on-exec.
func openImage(path string) (*os.File, error) {
	fd, err := syscall.Open(path, oPath|syscall.O_CLOEXEC, 0)
	//: no such file, or refused.
	if err != nil {
		//: REEXEC_FAILED naming the path.
		return nil, reexecFailed(err, errs.String("path", path))
	}
	//: the image.
	return os.NewFile(uintptr(fd), path), nil
}

// reexecFailed is REEXEC_FAILED with its cause beside it.
func reexecFailed(cause error, fields ...errs.FieldValue) error {
	//: the sentinel first: the origin errs.Wrap keeps.
	return errs.Wrap(joinCause(coresvc.ReexecFailed, cause), errs.WrapParams{}, fields...)
}

// reexec writes h to a sealed memfd and executes image with it; it returns
// only when the kernel refused, every descriptor close-on-exec again.
func reexec(h *HandoverState, image *os.File, argv, env []string) error {
	payload, files, err := h.encode(versionSeam())
	//: a descriptor fstat cannot read.
	if err != nil {
		//: HANDOVER_MISMATCH.
		return err
	}
	tamperPayload(payload)
	memfd, err := memfdPayload(payload)
	//: no memfd, or a payload it would not take.
	if err != nil {
		//: REEXEC_FAILED.
		return reexecFailed(err)
	}
	defer func() { gone(syscall.Close(memfd)) }()
	fds := []int{memfd}
	//: each descriptor the payload names.
	for _, f := range files {
		rec, ierr := fileIdentity(f)
		//: closed meanwhile.
		if ierr != nil {
			//: HANDOVER_MISMATCH.
			return ierr
		}
		fds = append(fds, rec.fd)
	}
	imageFD, err := fdOf(image)
	//: no descriptor to run.
	if err != nil {
		//: REEXEC_FAILED.
		return reexecFailed(err)
	}
	envv := append(slices.DeleteFunc(slices.Clone(env), isHandoverEnv), handoverEnv+"="+strconv.Itoa(memfd))
	//: the exec, or the kernel's refusal.
	return execWith(fds, "/proc/self/fd/"+strconv.Itoa(imageFD), argv, envv)
}

// execWith seals every descriptor close-on-exec, then — under ForkLock, so
// no fork in between inherits them — lifts the flag on keep alone and
// executes path; when the kernel refuses, keep is close-on-exec again.
// syscall.Exec runs the execve under the runtime's exec lock (no thread is
// created meanwhile, golang/go#19546) and gives the image back the
// descriptor limit this process started with; /proc/self/fd/N names the
// O_PATH descriptor's file as execveat(N, "", AT_EMPTY_PATH) does.
func execWith(keep []int, path string, argv, envv []string) error {
	//: a table that cannot be listed cannot be sealed.
	if err := sealAll(); err != nil {
		//: REEXEC_FAILED.
		return reexecFailed(err)
	}
	syscall.ForkLock.Lock()
	//: the descriptors handed over, inheritable.
	for _, fd := range keep {
		gone(setFDFlags(fd, 0))
	}
	err := syscall.Exec(path, argv, envv)
	//: refused: close-on-exec again before any fork.
	for _, fd := range keep {
		gone(setFDFlags(fd, syscall.FD_CLOEXEC))
	}
	syscall.ForkLock.Unlock()
	//: REEXEC_FAILED.
	return reexecFailed(err, errs.String("path", path))
}

// isHandoverEnv reports whether an assignment is the payload's variable.
func isHandoverEnv(kv string) bool {
	//: the variable, whatever its value.
	return strings.HasPrefix(kv, handoverEnv+"=")
}

// memfdPayload writes payload to a new memfd and seals it whole.
func memfdPayload(payload []byte) (int, error) {
	name, err := syscall.BytePtrFromString("kitsunium-handover")
	//: a name with a NUL.
	if err != nil {
		//: its error.
		return -1, err
	}
	r, _, errno := syscall.Syscall(sysMemfdCreate, uintptr(unsafe.Pointer(name)), mfdCloexec|mfdAllowSealing, 0)
	//: no memfd on this kernel.
	if errno != 0 {
		//: the errno.
		return -1, errno
	}
	fd := int(r)
	//: written whole, then sealed.
	for off := 0; off < len(payload); {
		n, werr := syscall.Pwrite(fd, payload[off:], int64(off))
		//: refused.
		if werr != nil {
			gone(syscall.Close(fd))
			//: its error.
			return -1, werr
		}
		off += n
	}
	//: sealed: written no more.
	if _, _, errno = syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), fAddSeals, sealsWhole); errno != 0 {
		gone(syscall.Close(fd))
		//: the errno.
		return -1, errno
	}
	//: the payload's descriptor.
	return fd, nil
}

// setFDFlags sets fd's descriptor flags: FD_CLOEXEC, or 0 to lift it.
func setFDFlags(fd int, flags uintptr) error {
	_, _, errno := syscall.RawSyscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_SETFD, flags)
	//: set.
	if errno == 0 {
		//: no error.
		return nil
	}
	//: the errno.
	return errno
}

// cloexec reports whether fd is close-on-exec; false for a closed one.
func cloexec(fd int) (open, on bool) {
	r, _, errno := syscall.RawSyscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0)
	//: closed.
	if errno != 0 {
		//: neither.
		return false, false
	}
	//: open, and its flag.
	return true, r&syscall.FD_CLOEXEC != 0
}

// listFDs lists this process's descriptors from 3 on.
func listFDs() ([]int, error) {
	entries, err := os.ReadDir(fdDir)
	//: no listing.
	if err != nil {
		//: its error.
		return nil, err
	}
	var out []int
	//: each entry.
	for _, e := range entries {
		fd, aerr := strconv.Atoi(e.Name())
		//: a descriptor past stdio.
		if aerr == nil && fd >= firstPassedFD {
			out = append(out, fd)
		}
	}
	//: the descriptors.
	return out, nil
}

// sealAll marks every descriptor from 3 on close-on-exec.
func sealAll() error {
	fds, err := listFDs()
	//: no listing.
	if err != nil {
		//: its error.
		return err
	}
	//: each one.
	for _, fd := range fds {
		syscall.CloseOnExec(fd)
	}
	//: sealed.
	return nil
}

// fdOf is f's descriptor number, lent without os.File.Fd, which would turn
// a pollable descriptor blocking.
func fdOf(f *os.File) (int, error) {
	fd := -1
	raw, err := f.SyscallConn()
	//: none.
	if err != nil {
		//: its error.
		return -1, err
	}
	//: the number.
	return fd, raw.Control(func(v uintptr) { fd = int(v) })
}

// takeHandover reads the payload the outgoing image left: none, or one
// accepted whole, or one refused with nothing left behind.
func takeHandover(ctx context.Context, grace time.Duration) (*HandoverState, error) {
	v, ok := os.LookupEnv(handoverEnv)
	//: no hand-over.
	if !ok {
		//: nothing.
		return nil, nil
	}
	gone(os.Unsetenv(handoverEnv))
	h, err := acceptHandover(v)
	//: refused: nothing inherited stays open, no child unsupervised.
	if err != nil {
		closeInherited(nil)
		stopped := stopChildren(ctx, grace)
		fields := make([]errs.FieldValue, 0, len(stopped))
		//: each child stopped.
		for _, pid := range stopped {
			fields = append(fields, errs.Int("pid", pid))
		}
		//: the refusal, naming what was stopped.
		return nil, errs.Wrap(err, errs.WrapParams{}, fields...)
	}
	//: accepted.
	return h, nil
}

// acceptHandover reads the payload at the descriptor v names and opens every
// descriptor it lists, checked by fstat; any other inherited descriptor is
// closed.
func acceptHandover(v string) (*HandoverState, error) {
	fd, err := strconv.Atoi(v)
	//: no descriptor named.
	if err != nil || fd < firstPassedFD {
		//: HANDOVER_INVALID.
		return nil, handoverInvalid("no payload descriptor", errs.String("env", v))
	}
	payload, err := readPayload(fd)
	//: not a sealed payload.
	if err != nil {
		//: HANDOVER_INVALID.
		return nil, err
	}
	units, records, err := decodeHandover(payload)
	//: not a payload this image reads.
	if err != nil {
		//: HANDOVER_INVALID.
		return nil, err
	}
	opened, err := openRecords(records)
	//: a descriptor that is not the one recorded.
	if err != nil {
		//: HANDOVER_MISMATCH.
		return nil, err
	}
	keep := make([]int, 0, len(records))
	//: each one listed.
	for _, r := range records {
		keep = append(keep, r.fd)
	}
	closeInherited(keep)
	//: the hand-over.
	return resolveRefs(units, opened), nil
}

// readPayload reads the payload from a memfd sealed whole, and closes it.
func readPayload(fd int) ([]byte, error) {
	defer func() { gone(syscall.Close(fd)) }()
	var st syscall.Stat_t
	//: no such descriptor, or not a file.
	if err := syscall.Fstat(fd, &st); err != nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		//: HANDOVER_INVALID.
		return nil, handoverInvalid("the payload descriptor is no file", errs.Int("fd", fd))
	}
	seals, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), fGetSeals, 0)
	//: a payload someone could still change.
	if errno != 0 || seals&sealsWhole != sealsWhole {
		//: HANDOVER_INVALID.
		return nil, handoverInvalid("the payload is not sealed whole", errs.Int("fd", fd))
	}
	//: too large to be read.
	if st.Size > int64(maxHandoverBytes) {
		//: HANDOVER_INVALID.
		return nil, handoverInvalid("a payload past its bound", errs.Int64("bytes", st.Size))
	}
	buf := make([]byte, st.Size)
	//: read whole.
	for off := 0; off < len(buf); {
		n, err := syscall.Pread(fd, buf[off:], int64(off))
		//: refused, or cut short.
		if err != nil || n == 0 {
			//: HANDOVER_INVALID.
			return nil, handoverInvalid("the payload could not be read", errs.Int("fd", fd))
		}
		off += n
	}
	//: the payload.
	return buf, nil
}

// openRecords checks each listed descriptor against its record — open,
// inherited, the same type, device and inode — and opens it close-on-exec;
// the first that is not is HANDOVER_MISMATCH, every one opened closed again.
func openRecords(records []fileRecord) (map[int]*os.File, error) {
	opened := make(map[int]*os.File, len(records))
	//: each record.
	for _, r := range records {
		//: not the object recorded.
		if err := checkRecord(r); err != nil {
			closeFiles(slices.Collect(mapIntValues(opened)))
			//: HANDOVER_MISMATCH naming it.
			return nil, err
		}
		syscall.CloseOnExec(r.fd)
		opened[r.index] = os.NewFile(uintptr(r.fd), "handover-"+strconv.Itoa(r.fd))
	}
	//: every one.
	return opened, nil
}

// checkRecord compares a descriptor with what the outgoing image recorded.
func checkRecord(r fileRecord) error {
	open, on := cloexec(r.fd)
	//: closed, or close-on-exec: the exec did not hand it.
	if !open || on {
		//: HANDOVER_MISMATCH.
		return mismatch(syscall.EBADF, r.fd)
	}
	var st syscall.Stat_t
	//: fstat refused.
	if err := syscall.Fstat(r.fd, &st); err != nil {
		//: HANDOVER_MISMATCH.
		return mismatch(err, r.fd)
	}
	//: another object.
	if st.Mode&syscall.S_IFMT != r.mode || uint64(st.Dev) != r.dev || st.Ino != r.ino {
		//: HANDOVER_MISMATCH.
		return mismatch(syscall.ESTALE, r.fd)
	}
	//: the one recorded.
	return nil
}

// mapIntValues lists opened descriptors.
func mapIntValues(m map[int]*os.File) func(func(*os.File) bool) {
	//: the iterator.
	return func(yield func(*os.File) bool) {
		//: each descriptor.
		for _, f := range m {
			//: until the caller stops.
			if !yield(f) {
				return
			}
		}
	}
}

// closeInherited closes every descriptor from 3 on that is not
// close-on-exec and not in keep: what an exec handed this image that nobody
// named. The runtime's own descriptors are close-on-exec.
func closeInherited(keep []int) {
	fds, err := listFDs()
	//: no listing: nothing known to close.
	if err != nil {
		return
	}
	//: each descriptor.
	for _, fd := range fds {
		open, on := cloexec(fd)
		//: inherited and named by nobody.
		if open && !on && !slices.Contains(keep, fd) {
			gone(syscall.Close(fd))
		}
	}
}

// stopChildren stops in order every child of this process — SIGTERM, the
// grace, SIGKILL — and reaps it: after a refused hand-over nothing
// supervises them. It returns their pids.
func stopChildren(ctx context.Context, grace time.Duration) []int {
	pids := childrenOf(os.Getpid())
	var procs []handedProc
	//: each child.
	for _, pid := range pids {
		procs = append(procs, handedProc{pid: pid, child: true})
	}
	stopHanded(ctx, grace, procs)
	//: the children stopped.
	return pids
}

// childrenOf lists the processes whose parent is parent, from /proc.
func childrenOf(parent int) []int {
	entries, err := os.ReadDir("/proc")
	//: no /proc.
	if err != nil {
		//: none known.
		return nil
	}
	var out []int
	//: each process.
	for _, e := range entries {
		pid, aerr := strconv.Atoi(e.Name())
		//: a child of parent.
		if ppid, ok := parentOf(pid); aerr == nil && ok && ppid == parent {
			out = append(out, pid)
		}
	}
	//: the children.
	return out
}
