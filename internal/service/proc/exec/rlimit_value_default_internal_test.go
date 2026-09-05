//go:build unix && !freebsd && !dragonfly

// Package exec — the uint64 syscall.Rlimit constructor.
package exec

import "testing"

// Test_makeRlimit pins that the soft/hard pair crosses into the kernel struct
// unchanged, LimitInfinity included.
//
// The file is build-tagged because FreeBSD and DragonFly type Rlimit.Cur/Max as
// int64 while coreproc.LimitValue is uint64. On these targets both sides are
// uint64, so ^uint64(0) has to land as the kernel's RLIM_INFINITY rather than
// being clamped — a clamp would turn "no limit" into the largest finite one,
// which is a ceiling the caller never asked for.
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
		{"an infinite soft ceiling", ^uint64(0), ^uint64(0)},
		{"an infinite hard ceiling over a bounded soft one", 1024, ^uint64(0)},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := makeRlimit(c.soft, c.hard)
		if got.Cur != c.soft {
			t.Errorf("Cur = %d, want %d", got.Cur, c.soft)
		}
		if got.Max != c.hard {
			t.Errorf("Max = %d, want %d", got.Max, c.hard)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
