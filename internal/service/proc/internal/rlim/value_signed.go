//go:build freebsd || dragonfly

package rlim

import "syscall"

// Make builds a syscall.Rlimit from a uint64 soft/hard pair. On FreeBSD and
// DragonFly Cur/Max are int64, so the pair is converted; the conversion is
// value-preserving for real limits, and LimitInfinity (^uint64(0)) wraps to
// int64(-1) = RLIM_INFINITY, the kernel's "no limit" sentinel.
func Make(soft, hard uint64) syscall.Rlimit {
	//: FreeBSD/DragonFly type Rlimit.Cur/Max as int64 — convert; ^uint64(0) → -1.
	return syscall.Rlimit{Cur: int64(soft), Max: int64(hard)}
}
