//go:build solaris

// Package childwait — the non-blocking "any child" wait4 on illumos and
// Solaris (the solaris build tag selects both).
package childwait

import "syscall"

// anyChildPID is the pid argument that makes wait4 collect ANY child here.
//
// illumos and Solaris keep SunOS 4 semantics in libc's wait4: a negative pid
// names the process group -pid and 0 names every child (illumos-gate,
// usr/src/lib/libc/port/gen/waitpid.c, "Emulate undocumented 4.x semantics").
// The -1 every other Unix reads as "any child" therefore asks for process
// group 1 and answers ECHILD while this process has live children. waitpid(-1)
// keeps the POSIX meaning on these kernels, but the stdlib syscall package
// exports only Wait4 there, and golang.org/x/sys is not an option (ADR 0016).
const anyChildPID int = 0

// waitAnyChild performs one non-blocking wait4 for any child, returning the
// collected pid, 0 when children exist but none has exited, or -1 and the errno.
//
// The stdlib wrapper needs two corrections on these kernels. It returns libc's
// 32-bit pid_t from a 64-bit register without sign extension, so the -1 of a
// failed call reads 4294967295; converting through int32 restores it. And it
// reports errno whatever the call returned, while POSIX gives errno a meaning
// only when the call returned -1; so the error is kept for a failed call only.
func waitAnyChild(status *StatusValue) (pid int, err error) {
	pid, err = syscall.Wait4(anyChildPID, &status.WaitStatus, syscall.WNOHANG, &status.Rusage)
	//: a failed call, whatever width the stdlib handed its -1 back in.
	if int32(pid) == -1 {
		//: the failure and its errno, as every other Unix reports them.
		return -1, err
	}
	//: a collected pid or 0; errno means nothing after a call that succeeded.
	return pid, nil
}
