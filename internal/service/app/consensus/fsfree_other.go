//go:build !(linux || darwin || windows || freebsd || dragonfly || (openbsd && !mips64))

package consensus

// fsFree is unknown on this platform: NetBSD, illumos and Solaris answer
// statvfs(2), which the standard library's syscall package does not wrap.
func fsFree(string) int64 { return -1 }
