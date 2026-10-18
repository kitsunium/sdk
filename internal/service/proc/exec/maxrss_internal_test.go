// Package exec — the peak-RSS unit conversion, on every platform.
package exec

import "testing"

// Test_peakKiB pins the arithmetic that turns a raw ru_maxrss into KiB. It is
// platform-neutral on purpose: only darwin's kernel reports bytes, and a
// conversion tested only there would be tested only on a macOS runner.
func Test_peakKiB(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		raw         int64
		unitsPerKiB int64
		want        int64
	}
	tests := []tc{
		{"kilobytes pass through: zero", 0, 1, 0},
		{"kilobytes pass through: a figure", 65536, 1, 65536},
		{"kilobytes pass through: the maximum", 1<<63 - 1, 1, 1<<63 - 1},
		{"kilobytes pass through: a negative figure is untouched", -1, 1, -1},
		{"bytes: zero", 0, 1024, 0},
		{"bytes: a negative figure reads as none", -1, 1024, 0},
		{"bytes: one byte rounds up", 1, 1024, 1},
		{"bytes: one KiB less a byte rounds up", 1023, 1024, 1},
		{"bytes: exactly one KiB", 1024, 1024, 1},
		{"bytes: one KiB and a byte", 1025, 1024, 2},
		{"bytes: 64 MiB", 64 << 20, 1024, 64 << 10},
		{"bytes: the maximum does not overflow", 1<<63 - 1, 1024, 1 << 53},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := peakKiB(c.raw, c.unitsPerKiB); got != c.want {
			t.Errorf("peakKiB(%d, %d) = %d, want %d", c.raw, c.unitsPerKiB, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
