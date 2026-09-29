//go:build solaris

package kit

import "syscall"

// makeFifo creates a named pipe at path on illumos and Solaris (the solaris
// build tag selects both). Their stdlib syscall package has no Mkfifo, and
// their libc's mkfifo(3C) is itself mknod(2) of an S_IFIFO node, which the
// stdlib does export and which needs no privilege for a FIFO.
func makeFifo(path string, perm uint32) error {
	//: mkfifo(3C)'s own definition, spelled out.
	return syscall.Mknod(path, syscall.S_IFIFO|perm, 0)
}
