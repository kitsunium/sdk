//go:build freebsd || dragonfly

// Package rlimit — the int64 syscall.Rlimit constructor. FreeBSD and DragonFly
// type Rlimit.Cur/Max as int64 (their rlim_t is __int64_t), unlike every other
// Unix target. Split into its own build-tagged file so applyOne (rlimit_unix.go,
// //go:build unix && !linux) constructs an Rlimit portably.
package rlimit

import "syscall"

// makeRlimit builds a syscall.Rlimit from a uint64 soft/hard pair. On FreeBSD and
// DragonFly Cur/Max are int64, so the pair is converted; the conversion is
// value-preserving for real limits, and LimitInfinity (^uint64(0)) wraps to
// int64(-1) = RLIM_INFINITY, the kernel's "no limit" sentinel.
func makeRlimit(soft, hard uint64) syscall.Rlimit {
	//: FreeBSD/DragonFly type Rlimit.Cur/Max as int64 — convert; ^uint64(0) → -1.
	return syscall.Rlimit{Cur: int64(soft), Max: int64(hard)}
}
