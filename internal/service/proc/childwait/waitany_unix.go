//go:build unix && !solaris

// Package childwait — the non-blocking "any child" wait4 on every Unix but
// illumos and Solaris, whose libc gives the pid argument other meanings
// (waitany_solaris.go).
package childwait

import "syscall"

// waitAnyChild performs one non-blocking wait4(-1): -1 is "any child" on
// Linux, darwin and the BSDs. It returns the collected pid, 0 when children
// exist but none has exited, or -1 and the errno.
func waitAnyChild(status *StatusValue) (pid int, err error) {
	//: the stdlib wrapper already answers in exactly that shape here.
	return syscall.Wait4(-1, &status.WaitStatus, syscall.WNOHANG, &status.Rusage)
}
