//go:build unix && !freebsd && !dragonfly

package rlim

import "syscall"

// Make builds a syscall.Rlimit from a uint64 soft/hard pair. On these targets
// Cur/Max are uint64, so the pair maps verbatim and LimitInfinity
// (^uint64(0)) lands as the kernel's RLIM_INFINITY.
func Make(soft, hard uint64) syscall.Rlimit {
	//: these targets type Rlimit.Cur/Max as uint64 — assign the pair directly.
	return syscall.Rlimit{Cur: soft, Max: hard}
}

// Pair reads a syscall.Rlimit back as the soft/hard pair Make built it from:
// verbatim on these targets.
func Pair(limit syscall.Rlimit) (soft, hard uint64) {
	//: the fields are already uint64.
	return limit.Cur, limit.Max
}
