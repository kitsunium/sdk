//go:build windows

package exec

import (
	"os"
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// duplicateSameAccess is DUPLICATE_SAME_ACCESS (winnt.h).
const duplicateSameAccess uint32 = 0x00000002

// Handle returns a duplicate of the process HANDLE the runtime holds
// (os.Process.WithHandle, DuplicateHandle with the same access), owned by the
// caller, who closes it. It reports false once the child is reaped.
func (h *handle) Handle() (*os.File, bool) {
	var (
		dup *os.File
		ok  bool
	)
	hErr := h.proc.WithHandle(func(ph uintptr) {
		//: the caller's own HANDLE on the same process.
		dup, ok = duplicateProcessHandle(ph)
	})
	//: a waited or released process has no HANDLE left.
	if hErr != nil {
		//: nothing to hand out.
		return nil, false
	}
	//: the duplicate, when DuplicateHandle granted it.
	return dup, ok
}

// SignalHandle delivers sig through the process HANDLE the runtime holds —
// os.Process.Signal never reopens a pid on Windows — so it is Signal.
func (h *handle) SignalHandle(sig coreproc.Signal) error {
	//: the same HANDLE-addressed path.
	return h.Signal(sig)
}

// duplicateProcessHandle duplicates a process HANDLE within this process.
func duplicateProcessHandle(ph uintptr) (*os.File, bool) {
	self, err := syscall.GetCurrentProcess()
	//: the pseudo-handle is always available; a failure is unexpected.
	if err != nil {
		//: nothing to hand out.
		return nil, false
	}
	var dup syscall.Handle
	//: a duplicate with the same rights, not inheritable.
	if err := syscall.DuplicateHandle(self, syscall.Handle(ph), self, &dup, 0, false, duplicateSameAccess); err != nil {
		//: the kernel refused the duplicate.
		return nil, false
	}
	//: the caller owns it; Close is CloseHandle.
	return os.NewFile(uintptr(dup), "process"), true
}
