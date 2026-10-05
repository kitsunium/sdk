//go:build solaris

package childwait

// anyChildPID is the pid argument that makes wait4 collect ANY child here: 0.
//
// illumos and Solaris keep SunOS 4 semantics in libc's wait4: a negative pid
// names the process group -pid and 0 names every child (illumos-gate,
// usr/src/lib/libc/port/gen/waitpid.c, "Emulate undocumented 4.x semantics").
// The -1 every other Unix reads as "any child" therefore asks for process
// group 1 and answers ECHILD while this process has live children. waitpid(-1)
// keeps the POSIX meaning on these kernels, but the stdlib syscall package
// exports only Wait4 there, and golang.org/x/sys is not an option (ADR 0016).
const anyChildPID int = 0
