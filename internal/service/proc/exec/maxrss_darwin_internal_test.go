//go:build darwin

// Package exec — darwin's peak-RSS read, in bytes.
package exec

import (
	"syscall"
	"testing"
)

// Test_maxRSSKB_darwin pins the conversion of darwin's ru_maxrss, which its
// getrusage(2) reports in bytes, into the KiB the port documents. A pass-through
// here overstates every darwin peak 1024 times (issue #357).
func Test_maxRSSKB_darwin(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   int64
		want int64
	}
	tests := []tc{
		{"zero", 0, 0},
		{"one byte rounds up to one KiB", 1, 1},
		{"exactly one KiB", 1024, 1},
		{"64 MiB", 64 << 20, 64 << 10},
		{"64 MiB and a byte rounds up", 64<<20 + 1, 64<<10 + 1},
		{"the maximum", 1<<63 - 1, 1 << 53},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ru := &syscall.Rusage{}
		ru.Maxrss = c.in

		if got := maxRSSKB(ru); got != c.want {
			t.Errorf("maxRSSKB(%d) = %d, want %d", c.in, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
