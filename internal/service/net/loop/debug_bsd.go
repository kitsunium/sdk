//go:build darwin || freebsd || openbsd || netbsd || dragonfly

package loop

// loopDebug is false on kqueue, whatever the build tags: package syscall has
// no thread identifier on darwin and the BSDs, and a raw syscall for one is
// refused by OpenBSD's pinned system calls (ADR 0222). The loopdebug check
// runs on Linux's lanes.
const loopDebug = false

// threadCheck compiles no check on kqueue.
func threadCheck(_ int) func() bool {
	//: no check.
	return nil
}
