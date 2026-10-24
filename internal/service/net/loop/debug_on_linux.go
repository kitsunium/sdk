//go:build linux && loopdebug

package loop

import "syscall"

// loopDebug reports whether the loopdebug build tag compiled the thread
// check in.
const loopDebug = true

// threadCheck returns the loopdebug check that the caller runs on thread tid.
func threadCheck(tid int) func() bool {
	//: Gettid is the thread, whatever goroutine runs on it.
	return func() bool { return syscall.Gettid() == tid }
}
