//go:build freebsd || dragonfly

// Package rlimit_test — int64 syscall.Rlimit field reader. FreeBSD and DragonFly
// type Rlimit.Cur/Max as int64, so the fields are converted to uint64 to match
// coreproc.LimitValue. Split by build tag (mirroring the production makeRlimit)
// so the cross-platform observability test never converts inline.
package rlimit_test

import "syscall"

// rlimFields returns the soft (Cur) and hard (Max) ceilings of r as uint64;
// FreeBSD/DragonFly type the fields as int64, so they are converted here.
func rlimFields(r syscall.Rlimit) (cur, max uint64) {
	//: Cur/Max are int64 here — convert to the LimitValue uint64 type.
	return uint64(r.Cur), uint64(r.Max)
}
