//go:build freebsd || dragonfly

package consensus

import "syscall"

// fsFree returns the bytes an unprivileged writer may still use on the
// filesystem holding dir, -1 when unknown. FreeBSD and DragonFly name the
// fields as Linux does, with a signed block count.
func fsFree(dir string) int64 {
	var s syscall.Statfs_t
	if err := syscall.Statfs(dir, &s); err != nil || s.Bavail < 0 {
		return -1
	}
	return s.Bavail * int64(s.Bsize)
}
