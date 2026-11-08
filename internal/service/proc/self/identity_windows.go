package self

import (
	"syscall"
	"time"
)

// kernelStart reads the process's creation time from GetProcessTimes.
func kernelStart() (time.Time, bool) {
	var creation, exit, kernel, user syscall.Filetime
	process, err := syscall.GetCurrentProcess()
	//: the pseudo-handle, then the times.
	if err == nil {
		err = syscall.GetProcessTimes(process, &creation, &exit, &kernel, &user)
	}
	//: refused.
	if err != nil {
		return time.Time{}, false
	}
	//: the kernel's word.
	return time.Unix(0, creation.Nanoseconds()), true
}

// pidNamespace has no Windows equivalent.
func pidNamespace() (inode uint64, nsPid1 bool) {
	//: none.
	return 0, false
}
