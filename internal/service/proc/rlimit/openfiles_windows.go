package rlimit

import (
	"syscall"
	"unsafe"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The kernel32 entry points OpenFiles calls.
var (
	modKernel32                = syscall.NewLazyDLL("kernel32.dll")
	procGetProcessHandleCount  = modKernel32.NewProc("GetProcessHandleCount")
	procGetCurrentProcessFiles = modKernel32.NewProc("GetCurrentProcess")
)

// exitOSErr is sysexits.h EX_OSERR (71), the exit status RLIMIT_FAILED carries.
const exitOSErr int = 71

// openFiles counts the caller's open HANDLES — files, but also events,
// threads, keys: Windows keeps no separate count of files.
func openFiles() (int, error) {
	self, _, _ := procGetCurrentProcessFiles.Call()
	var count uint32
	r1, _, errno := procGetProcessHandleCount.Call(self, uintptr(unsafe.Pointer(&count)))
	//: refused.
	if r1 == 0 {
		return 0, errs.Wrap(errno, errs.WrapParams{
			Code:     coreproc.CodeRlimitFailed,
			Reason:   "RLIMIT_FAILED",
			Public:   "Could not read the resource limit",
			Private:  "service/proc/rlimit.OpenFiles: GetProcessHandleCount failed",
			ExitCode: exitOSErr,
		})
	}
	//: the handle count.
	return int(count), nil
}
