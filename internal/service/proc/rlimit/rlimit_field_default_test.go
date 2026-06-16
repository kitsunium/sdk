//go:build unix && !freebsd && !dragonfly

// Package rlimit_test — uint64 syscall.Rlimit field reader. On every Unix target
// except FreeBSD/DragonFly the kernel types Rlimit.Cur/Max as uint64, so the
// fields are returned directly. Split by build tag (mirroring the production
// makeRlimit) so the cross-platform observability test never converts inline.
package rlimit_test

import "syscall"

// rlimFields returns the soft (Cur) and hard (Max) ceilings of r as uint64; on
// these targets the fields are already uint64, so no conversion is needed.
func rlimFields(r syscall.Rlimit) (cur, max uint64) {
	//: Cur/Max are uint64 here — hand them back unchanged.
	return r.Cur, r.Max
}
