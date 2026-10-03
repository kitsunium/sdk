//go:build !windows && !solaris

package entitlement

import "syscall"

// makeFifo creates a named pipe at path with mkfifo(2), which the stdlib
// syscall package exports on every Unix this SDK targets except illumos and
// Solaris; fifo_mknod_internal_test.go serves those two.
func makeFifo(path string, perm uint32) error {
	//: the plain POSIX call where the stdlib offers it.
	return syscall.Mkfifo(path, perm)
}
