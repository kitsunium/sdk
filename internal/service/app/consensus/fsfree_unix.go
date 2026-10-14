//go:build linux || darwin

package consensus

import "syscall"

// fsFree returns the bytes an unprivileged writer may still use on the
// filesystem holding dir, -1 when unknown.
func fsFree(dir string) int64 {
	var s syscall.Statfs_t
	if err := syscall.Statfs(dir, &s); err != nil {
		return -1
	}
	return int64(s.Bavail) * int64(s.Bsize)
}
