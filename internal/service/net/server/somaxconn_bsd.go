//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package server

import (
	"runtime"
	"syscall"
)

// hostSomaxconn is the kernel's listen-queue clamp — kern.ipc.somaxconn on
// darwin, FreeBSD and DragonFly, kern.somaxconn on NetBSD and OpenBSD; zero
// when it cannot be read.
func hostSomaxconn() int {
	name := "kern.ipc.somaxconn"
	//: the two that spell it without the ipc.
	if runtime.GOOS == "netbsd" || runtime.GOOS == "openbsd" {
		name = "kern.somaxconn"
	}
	n, err := syscall.SysctlUint32(name)
	//: unreadable counts as unknown.
	if err != nil {
		return 0
	}
	return int(n)
}
