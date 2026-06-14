//go:build unix && !freebsd && !dragonfly

// Package exec — the uint64 syscall.Rlimit constructor. On every Unix target
// except FreeBSD/DragonFly the kernel types Rlimit.Cur/Max as uint64, matching
// coreproc.LimitValue exactly. Split into its own build-tagged file so the
// trampoline (trampoline_unix.go, //go:build unix) can construct an Rlimit
// portably without referencing a platform-specific field type inline.
package exec

import "syscall"

// makeRlimit builds a syscall.Rlimit from a uint64 soft/hard pair. On these
// targets Cur/Max are uint64, so the pair maps verbatim and LimitInfinity
// (^uint64(0)) lands as the kernel's RLIM_INFINITY.
func makeRlimit(soft, hard uint64) syscall.Rlimit {
	//: these targets type Rlimit.Cur/Max as uint64 — assign the pair directly.
	return syscall.Rlimit{Cur: soft, Max: hard}
}
