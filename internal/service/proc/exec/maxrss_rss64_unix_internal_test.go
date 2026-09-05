//go:build unix && !386 && !arm && !mips && !mipsle

// Package exec — the 64-bit peak-RSS read.
package exec

import (
	"syscall"
	"testing"
)

// Test_maxRSSKB pins the pass-through on a 64-bit GOARCH.
//
// The file exists only because Rusage.Maxrss is int64 here and int32 on 32-bit
// targets, so the 32-bit sibling widens while this one must not cast at all. The
// unit is kilobytes on every Unix — reporting bytes here would inflate every
// figure a supervisor logs by a factor of 1024, which looks plausible enough to
// go unnoticed.
func Test_maxRSSKB(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   int64
	}
	tests := []tc{
		{"zero", 0},
		{"a small figure", 1024},
		{"a realistic figure", 128 * 1024},
		{"a value past the 32-bit range", 1 << 40},
		{"the maximum", 1<<63 - 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ru := &syscall.Rusage{}
		ru.Maxrss = c.in

		if got := maxRSSKB(ru); got != c.in {
			t.Errorf("maxRSSKB(%d) = %d, want %d", c.in, got, c.in)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
