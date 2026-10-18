//go:build openbsd && !mips64

package consensus

import "syscall"

// fsFree returns the bytes an unprivileged writer may still use on the
// filesystem holding dir, -1 when unknown. OpenBSD's statfs(2) prefixes its
// fields with f_.
func fsFree(dir string) int64 {
	var s syscall.Statfs_t
	if err := syscall.Statfs(dir, &s); err != nil || s.F_bavail < 0 {
		return -1
	}
	return s.F_bavail * int64(s.F_bsize)
}
