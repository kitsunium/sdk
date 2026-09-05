//go:build freebsd || dragonfly

// Package rlimit — int64 syscall.Rlimit field reader. FreeBSD and DragonFly
// type Rlimit.Cur/Max as int64, so the fields are converted to uint64 to match
// coreproc.LimitValue. Split by build tag (mirroring the production makeRlimit)
// so the cross-platform observability test never converts inline.
package rlimit

import (
	"syscall"
	"testing"
)

// rlimFields returns the soft (Cur) and hard (Max) ceilings of r as uint64;
// FreeBSD/DragonFly type the fields as int64, so they are converted here.
func rlimFields(r syscall.Rlimit) (cur, max uint64) {
	//: Cur/Max are int64 here — convert to the LimitValue uint64 type.
	return uint64(r.Cur), uint64(r.Max)
}

// Test_makeRlimit pins the int64 constructor. FreeBSD and DragonFly type
// Rlimit.Cur/Max as int64 while coreproc.LimitValue is uint64, so this is the
// one place the two widths meet — and the one place a ceiling could silently
// change sign on the way to the kernel.
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
