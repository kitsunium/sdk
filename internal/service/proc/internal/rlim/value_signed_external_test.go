//go:build freebsd || dragonfly

// Package rlim_test — the int64 constructor, on FreeBSD and DragonFly, whose
// kernels type Rlimit.Cur/Max as int64.
package rlim_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/service/proc/internal/rlim"
)

// TestMakeConvertsThePairToTheSignedFields pins the one place the two widths
// meet: coreproc.LimitValue is uint64 and these kernels' fields are int64, so a
// real ceiling must keep its value through the conversion, and LimitInfinity
// (^uint64(0)) must become int64(-1) — RLIM_INFINITY — rather than the ceiling
// silently changing sign into something else on its way to the kernel.
func TestMakeConvertsThePairToTheSignedFields(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		soft     uint64
		hard     uint64
		wantSoft int64
		wantHard int64
	}
	tests := []tc{
		{"a bounded pair", 1024, 4096, 1024, 4096},
		{"an equal pair", 512, 512, 512, 512},
		{"the zero pair", 0, 0, 0, 0},
		{"an infinite ceiling is RLIM_INFINITY", ^uint64(0), ^uint64(0), -1, -1},
		{"an infinite hard ceiling over a bounded soft one", 1024, ^uint64(0), 1024, -1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := rlim.Make(c.soft, c.hard)
		if got.Cur != c.wantSoft || got.Max != c.wantHard {
			t.Errorf("Make(%d, %d) = (%d, %d), want (%d, %d)",
				c.soft, c.hard, got.Cur, got.Max, c.wantSoft, c.wantHard)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
