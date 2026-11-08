//go:build unix

package exec

import (
	"os"
	"strconv"
	"sync/atomic"
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// firstSealedFD is the lowest descriptor the seal marks: 0, 1 and 2 are the
// child's stdio, which forkExec installs with dup2 whatever their flags.
const firstSealedFD int = 3

// sealInherited marks every descriptor of the supervisor from firstSealedFD up
// close-on-exec before a spawn, unless spec.FDs asks to inherit them (#1089). Go's
// fork/exec installs the child's stdio, ExtraFiles and handshake pipe with
// dup2, which clears the flag on the child's copy, and leaves every other
// descriptor without FD_CLOEXEC open in the child: an activation socket a
// systemd unit received, a descriptor the shell that started the supervisor
// leaked, a raw syscall.Dup. The mark closes nothing and is process-wide.
func sealInherited(spec coreproc.Spec) error {
	//: the caller relies on implicit inheritance, as fork/exec does.
	if spec.FDs == coreproc.FDsInherited {
		//: today's table, nothing marked.
		return nil
	}
	//: the platform's cheapest complete mark.
	return sealFDs()
}

// sealWith marks the table with closeRange — one close_range(2) with
// CLOSE_RANGE_CLOEXEC — and falls back to the walk of fdDir when the kernel
// answers that it has no such call or no such flag (ENOSYS before Linux 5.9 or
// FreeBSD 13, EINVAL on Linux 5.9 and 5.10, which lack the flag), remembering
// the answer in missing so later spawns go straight to the walk.
func sealWith(closeRange func() syscall.Errno, missing *atomic.Bool) error {
	//: a kernel already found without the call is walked directly.
	if missing.Load() {
		//: the slower, complete fallback.
		return sealByWalk(fdDir)
	}
	errno := closeRange()
	//: one syscall marked the whole table.
	if errno == 0 {
		//: sealed.
		return nil
	}
	//: a refusal other than "no such call or flag" is a real fault.
	if errno != syscall.ENOSYS && errno != syscall.EINVAL {
		//: the spawn does not run with a table it could not seal.
		return wrapSpawn(errno, errs.Int("fd", firstSealedFD))
	}
	//: this kernel will answer the same next time.
	missing.Store(true)
	//: mark what the walk lists instead.
	return sealByWalk(fdDir)
}

// sealByWalk marks close-on-exec every descriptor from firstSealedFD up that
// dir lists — /proc/self/fd on Linux, illumos and Solaris, /dev/fd on darwin
// and the BSDs. syscall.CloseOnExec routes through libc where the kernel
// refuses a raw fcntl (OpenBSD), and ignores a descriptor closed since the
// listing. The directory's own descriptor, opened close-on-exec, is listed
// too and marked again harmlessly.
func sealByWalk(dir string) error {
	entries, err := os.ReadDir(dir)
	//: without the listing nothing says which descriptors are open.
	if err != nil {
		//: the spawn does not run with a table it could not seal.
		return wrapSpawn(err, errs.String("fd", dir))
	}
	//: every listed descriptor past stdio.
	for _, entry := range entries {
		fd, aErr := strconv.Atoi(entry.Name())
		//: "." and "..", and stdio, are not the seal's.
		if aErr != nil || fd < firstSealedFD {
			//: skip it.
			continue
		}
		//: the flag alone: the supervisor keeps the descriptor.
		syscall.CloseOnExec(fd)
	}
	//: every listed descriptor is marked.
	return nil
}
