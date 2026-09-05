//go:build unix && !freebsd && !dragonfly && !linux

// Package rlimit — uint64 syscall.Rlimit field reader. On every Unix target
// except FreeBSD/DragonFly the kernel types Rlimit.Cur/Max as uint64, so the
// fields are returned directly. Split by build tag (mirroring the production
// makeRlimit) so the cross-platform observability test never converts inline.
package rlimit

import (
	"syscall"
	"testing"
)

// rlimFields returns the soft (Cur) and hard (Max) ceilings of r as uint64; on
// these targets the fields are already uint64, so no conversion is needed.
func rlimFields(r syscall.Rlimit) (cur, max uint64) {
	//: Cur/Max are uint64 here — hand them back unchanged.
	return r.Cur, r.Max
}

// Test_makeRlimit pins the uint64 constructor. The soft/hard pair crosses from
// coreproc.LimitValue into the kernel struct here, and on these targets both
// sides are uint64, so the values must arrive unchanged — a conversion slip
// would silently clamp a ceiling.
func Test_makeRlimit(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		soft uint64
		hard uint64
	}
	tests := []tc{
		{"a bounded pair", 1024, 4096},
		{"an equal pair", 512, 512},
		{"the zero pair", 0, 0},
		{"the maximum value", ^uint64(0), ^uint64(0)},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := makeRlimit(c.soft, c.hard)
		cur, maxv := rlimFields(got)
		if cur != c.soft || maxv != c.hard {
			t.Errorf("makeRlimit(%d, %d) = (%d, %d)", c.soft, c.hard, cur, maxv)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
